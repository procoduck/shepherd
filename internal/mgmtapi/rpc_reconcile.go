package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/merge"
	"shepherd/internal/reconcile"
)

// beaconStaleAfter mirrors internal/agentapi's beaconInventoryExpireAfter (5m,
// unexported there): a beacon_inventory row older than this is due for the
// sweeper's expiry, so reconciliation reports it as stale — still positive
// evidence a component WAS running, not proof it still is.
const beaconStaleAfter = 5 * time.Minute

// managedControllerPrefix is the controller_path prefix a merge-assembled
// pipeline reports under (merge declare-wraps each as `pipe_<sanitized>`).
// Reconciliation compares observed components only within this managed
// namespace: a `pipe_*` path observed but not served is real, actionable drift
// (a disabled/deleted pipeline a collector is still running). Root-level
// components — Alloy's root controller, where the beacon baseline and any BYO
// top-level components run — are not individually distinguishable via
// alloy_component_controller_running_components and are deliberately out of
// scope, which also keeps the baseline from false-flagging (see the plan).
const managedControllerPrefix = "pipe_"

// GetReconciliation returns a collector's declared-vs-served-vs-observed drift
// findings (#110). Org viewer; the per-id ownership check is loadOwnedCollector.
// Computed on demand — no reconciliation state is stored.
func (s *FleetService) GetReconciliation(ctx context.Context, req *connect.Request[mgmtv1.GetReconciliationRequest]) (*connect.Response[mgmtv1.GetReconciliationResponse], error) {
	id, err := s.loadOwnedCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	orgID, _ := parseUUID(req.Msg.GetOrgId()) // already validated == owner by loadOwnedCollector

	coll, err := s.store.Queries.GetCollectorByID(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("collector not found"))
	}
	cluster, _ := s.store.Queries.GetClusterByID(ctx, coll.ClusterID) //nolint:errcheck // empty cluster name only affects matcher matching, degrades safely

	served, excluded, err := s.reconcileServed(ctx, orgID, coll.ID.String(), coll.Role, cluster.Name, coll.Labels)
	if err != nil {
		s.logger.Warn("reconcile: building served set failed", "collector_id", id.String(), "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to reconcile served pipelines"))
	}
	observed, err := s.reconcileObserved(ctx, id)
	if err != nil {
		s.logger.Warn("reconcile: building observed set failed", "collector_id", id.String(), "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to reconcile observed components"))
	}

	findings, err := reconcile.Compare(reconcile.Declared{Role: coll.Role}, served, observed)
	if err != nil {
		// The only error Compare returns is an unrecognized declared role — a
		// data-integrity bug (a collector persisted with a role signals.Policies
		// doesn't know), not a routine drift finding.
		s.logger.Error("reconcile: comparing failed", "collector_id", id.String(), "role", coll.Role, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("collector has an unrecognized role"))
	}
	// Exclusions first: a pipeline aimed at this collector that is not running
	// on it is the finding an operator most needs, and before M2 it was
	// visible only as a comment in the served config.
	findings = append(reconcile.ExclusionFindings(reconcile.Declared{Role: coll.Role}, excluded), findings...)
	// A rejection leads (#297): while the collector refuses its served config
	// it runs the previous one, so every other served<->observed comparison
	// is about a config that is not running.
	if rej, ok := s.rejectionFinding(ctx, id); ok {
		findings = append([]reconcile.Finding{rej}, findings...)
	}
	return connect.NewResponse(&mgmtv1.GetReconciliationResponse{Findings: findingsToProto(findings)}), nil
}

// rejectionFinding reports a collector that rejected its served config
// (#297): its newest live instance presents FAILED (presentInstanceStatus, the
// rule the collector views use, so an inactive instance's FAILED does not
// count). The load error names a line of the MERGED config; when the status
// was reported against the config served now, internal/merge maps that line
// back to the pipeline whose declare block holds it. Served content is read
// as stored — the span map is recovered from the bytes, never written next to
// them. A status recorded without a hash (an agent predating #115) is taken
// to be about the served config, as the collector views take it.
//
// Hash equality is the only attribution rule. A status about an earlier hash
// is still reported (Alloy re-sends only a changed error, so the collector is
// most likely still failing), but no "probably pipeline X" is offered: the
// earlier config's bytes are not stored, so nothing cheap proves the line
// still lands in the same block — any change above it (another pipeline,
// the header's revision list) shifts every line after it.
func (s *FleetService) rejectionFinding(ctx context.Context, collectorID pgtype.UUID) (reconcile.Finding, bool) {
	rows, err := s.store.Queries.ListCollectorInstancesByCollector(ctx, collectorID)
	if err != nil {
		s.logger.Warn("reconcile: listing instances for rejection check", "collector_id", collectorID.String(), "err", err)
		return reconcile.Finding{}, false
	}
	if len(rows) == 0 {
		return reconcile.Finding{}, false
	}
	row := rows[0]
	status, errMsg := presentInstanceStatus(instanceOutcome{
		status: row.RemoteConfigStatus, errMsg: row.RemoteConfigError,
		statusHash: row.StatusHash, servedHash: row.ServedHash, lastSeen: row.LastSeen,
	}, s.inactiveAfter, s.now())
	if status != "FAILED" {
		return reconcile.Finding{}, false
	}
	rej := reconcile.Rejection{
		AboutCurrent: row.StatusHash.String == "" || row.StatusHash.String == row.ServedHash.String,
	}
	if !rej.AboutCurrent {
		return reconcile.RejectionFinding(rej), true
	}
	line, _, ok := merge.ErrorPosition(errMsg)
	if !ok {
		return reconcile.RejectionFinding(rej), true
	}
	rej.MergedLine = line
	cache, err := s.store.Queries.GetServeCache(ctx, collectorID)
	if err != nil {
		// Still a rejection worth reporting; it just cannot be attributed.
		s.logger.Warn("reconcile: reading served config for rejection attribution", "collector_id", collectorID.String(), "err", err)
		return reconcile.RejectionFinding(rej), true
	}
	if loc, found := merge.Locate(cache.Content, line); found {
		rej.PipelineName = loc.PipelineName
		rej.PipelineLine = loc.PipelineLine
	}
	return reconcile.RejectionFinding(rej), true
}

// reconcileServed rebuilds the pipeline set a collector SHOULD be served now —
// the desired served state — by replicating internal/merge's match + role/signal
// enforcement (enforce.go), the same loop that also derives each pipeline's
// signals. Neither serve.Result nor merge.AssembleResult exposes the included
// set, hence the replication.
//
// Because this set is enforcement-clean by construction, reconcile's
// declared<->served check (role_signal_mismatch) never fires against it — that
// contradiction only exists for a stale/unenforced serve_cache, which this
// desired-state view deliberately does not read. The actionable signal this
// surface produces is therefore served<->observed drift: a collector running a
// managed pipeline that its desired served set no longer contains (a disabled or
// deleted pipeline it has not yet dropped).
//
// Pipelines that match but that enforcement excludes are returned separately
// (excluded), each as merge.RoleExclusion reports it — the same per-pipeline
// check merge.Assemble ran when it wrote the served config — so the
// reconciliation tab can name them (M2) instead of reading "in sync".
//
// The label set is built exactly as the serve paths build it (#139): admin
// labels and local_attributes join cluster/role only when the org's matching
// flags allow it. Using cluster/role alone here would leave out a pipeline the
// collector reaches through a label — one it is correctly running — and report
// it as drift.
func (s *FleetService) reconcileServed(ctx context.Context, orgID pgtype.UUID, collectorID, role, cluster string, rawLabels json.RawMessage) ([]reconcile.ServedPipeline, []reconcile.ExcludedPipeline, error) {
	eps, err := s.store.Queries.ListEnabledPipelinesForMerge(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	org, _ := s.store.Queries.GetOrgByID(ctx, orgID) //nolint:errcheck // an org lookup failure degrades to cluster/role-only matching, as on the serve paths
	localAttrs := localAttrsByOrg(ctx, s.store.Queries, orgID, org.AllowLocalAttributeMatching)
	cl := merge.BuildCollectorLabels(collectorID, cluster, role, adminLabelsIfAllowed(org.AllowLabelMatching, rawLabels), localAttrs[collectorID])
	var served []reconcile.ServedPipeline
	var excluded []reconcile.ExcludedPipeline
	for i := range eps {
		ep := eps[i]
		var m []string
		if json.Unmarshal(ep.Matchers, &m) != nil {
			continue
		}
		p := merge.Pipeline{
			ID: ep.ID.String(), Name: ep.Name, Contents: ep.Contents,
			Matchers: m, Source: ep.Source,
			RepoLinkCollectorID: repoLinkCollectorID(ep.RepoLinkCollectorID),
		}
		matched, matchErr := merge.MatchesPipeline(p, cl)
		if matchErr != nil || !matched {
			continue
		}
		// The enforcement merge.Assemble applies, per pipeline: an excluded
		// pipeline (signals the role refuses, or signals that could not be
		// derived — fail-safe) is not served, and is reported as excluded.
		// RoleCheck also hands back the signal set it derived, so a served
		// pipeline's signals are not derived a second time.
		sig, ex, isExcluded := merge.RoleCheck(p, role, s.schema)
		if isExcluded {
			excluded = append(excluded, reconcile.ExcludedPipeline{
				Name: ex.PipelineName, Reason: ex.Reason, Disallowed: ex.Disallowed, Unproven: ex.Unproven,
			})
			continue
		}
		served = append(served, reconcile.ServedPipeline{
			Name:           p.Name,
			ControllerPath: managedControllerPrefix + merge.SanitizeName(p.Name),
			Signals:        sig,
		})
	}
	return served, excluded, nil
}

// reconcileObserved reads the collector's beacon inventory (attributed via the
// shepherd_collector_id the baseline stamps) into the reconcile Observed set,
// scoped to the managed namespace (see managedControllerPrefix).
func (s *FleetService) reconcileObserved(ctx context.Context, collectorID pgtype.UUID) (reconcile.Observed, error) {
	rows, err := s.store.Queries.ListBeaconInventoryByCollector(ctx, collectorID)
	if err != nil {
		return reconcile.Observed{}, err
	}
	cutoff := time.Now().Add(-beaconStaleAfter)
	var comps []reconcile.ObservedComponent
	for i := range rows {
		r := rows[i]
		if !strings.HasPrefix(r.ComponentName, managedControllerPrefix) {
			continue
		}
		comps = append(comps, reconcile.ObservedComponent{
			ControllerPath: r.ComponentName,
			Healthy:        r.Healthy,
			Stale:          r.LastSeen.Valid && r.LastSeen.Time.Before(cutoff),
		})
	}
	return reconcile.Observed{Components: comps}, nil
}

func findingsToProto(findings []reconcile.Finding) []*mgmtv1.Finding {
	out := make([]*mgmtv1.Finding, 0, len(findings))
	for i := range findings {
		f := findings[i]
		out = append(out, &mgmtv1.Finding{
			Kind:           string(f.Kind),
			Sources:        []string{string(f.Sources[0]), string(f.Sources[1])},
			Summary:        f.Summary,
			PipelineName:   f.PipelineName,
			ControllerPath: f.ControllerPath,
			Stale:          f.Stale,
		})
	}
	return out
}
