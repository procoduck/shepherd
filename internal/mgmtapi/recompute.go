package mgmtapi

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/serve"
)

// The eager serve path: after a write changes what an org's collectors are
// served, recompute and store every collector's served config instead of
// waiting for its next poll. internal/agentapi's GetConfig is the lazy path;
// both go through serve.ComputeServed, serve.Store and serve.RecordFailure.

// RecomputeOrgCaches is recomputeOrgCaches for callers outside this package:
// internal/gitsync runs it after a sync changes the org's git pipelines
// (server.go wires it with gitsync.WithRecompute), the eager recompute the
// API's own pipeline and repo-link deletes do.
func (s *PipelineService) RecomputeOrgCaches(ctx context.Context, orgID pgtype.UUID) {
	s.recomputeOrgCaches(ctx, orgID)
}

// recomputeOrgCaches assembles and writes the serve cache for all collectors in an org.
// Called as a background goroutine after pipeline enable/disable/update/delete.
// A collector whose recompute fails keeps its previous entry and is reported
// through serve.RecordFailure, exactly as the lazy path reports it.
func (s *PipelineService) recomputeOrgCaches(ctx context.Context, orgID pgtype.UUID) {
	// Optional prewarm: use the same conditional cache path as lazy GetConfig.
	// It is panic-safe and caller-invisible; failures are logged and never affect HTTP responses.
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("recomputeOrgCaches panicked", "org_id", orgID.String(), "panic", r)
		}
	}()
	collectors, err := s.store.Queries.ListCollectorsWithClusterByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("recomputeOrgCaches: listing collectors failed", "err", err)
		return
	}
	org, _ := s.store.Queries.GetOrgByID(ctx, orgID) //nolint:errcheck // an org lookup failure degrades to no admin labels below
	localAttrs := localAttrsByOrg(ctx, s.store.Queries, orgID, org.AllowLocalAttributeMatching)
	// The dirty generation of every cache row, read BEFORE the pipelines are
	// loaded: UpsertServeCacheConditional is a compare-and-swap on it, so a
	// mark that lands after this read (a newer enable/disable/restore/delete)
	// makes this recompute's write a no-op for that collector and the
	// recompute that observed the newer mark writes instead. Reading it after
	// the pipelines would let stale content pass as current. A collector with
	// no row yet has generation 0.
	seqRows, err := s.store.Queries.ListServeCacheSeqByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("recomputeOrgCaches: reading cache generations failed", "err", err)
		return
	}
	expectedSeq := make(map[pgtype.UUID]int64, len(seqRows))
	for _, r := range seqRows {
		expectedSeq[r.CollectorID] = r.DirtySeq
	}
	// ForMerge, not ByOrg: only the former joins repo_links to carry
	// repo_link_collector_id, and merge.MatchesPipeline matches a git pipeline
	// SOLELY by that field. Loading them without it makes every git pipeline
	// match nothing -- which in stage 3 validates a merged config the collector
	// will never actually be served, and in recompute writes a serve cache with
	// the org's GitOps pipelines silently removed.
	enabledPipelines, err := s.store.Queries.ListEnabledPipelinesForMerge(ctx, orgID)
	if err != nil {
		s.logger.Error("recomputeOrgCaches: listing pipelines failed", "err", err)
		return
	}
	// The same row -> merge.Pipeline conversion agentapi's lazy recompute
	// uses, current revision included (#296): the two paths must serve
	// byte-identical config or the hash moves whenever a poll switches path.
	mergePipelines := serve.PipelinesFromRows(enabledPipelines)
	for i := range collectors {
		c := collectors[i]

		// internal/serve.ComputeServed is the single merge -> append-baseline
		// -> hash -> Stage-1-validate implementation this eager path shares
		// with internal/agentapi's lazy recompute (docs/gateway-tier-plan.md
		// §10), role enforcement included.
		served, err := serve.ComputeServed(ctx,
			serve.Deps{Schema: s.schema, BeaconBaseline: s.beaconBaseline},
			serve.Collector{ID: c.ID.String(), Cluster: c.ClusterName, Role: c.Role, AdminLabels: adminLabelsIfAllowed(org.AllowLabelMatching, c.Labels), LocalAttrs: localAttrs[c.ID.String()]},
			mergePipelines,
		)
		if err != nil {
			serve.RecordFailure(ctx, s.store, s.logger, serve.Failure{
				CollectorID: c.ID, OrgID: orgID, DirtySeq: expectedSeq[c.ID], Path: serve.PathEager, Err: err,
			})
			continue
		}
		for _, ex := range served.Exclusions {
			// Visible per docs/gateway-tier-plan.md §8 rule 2: a pipeline
			// excluded for a role/signal mismatch is never silent. The
			// generated header (served.Content) already names it too; this
			// log line is what makes it discoverable without opening the
			// merged config.
			s.logger.Warn("recomputeOrgCaches: pipeline excluded (role/signal mismatch)",
				"collector_id", c.ID.String(), "role", c.Role, "pipeline", ex.PipelineName, "reason", ex.Reason)
		}
		if served.BaselineErr != nil {
			s.logger.Error("recomputeOrgCaches: appending beacon baseline pipeline failed; serving without it",
				"collector_id", c.ID.String(), "err", served.BaselineErr)
		}

		_, written, storeErr := serve.Store(ctx, s.store.Queries, c.ID, expectedSeq[c.ID], served)
		if storeErr != nil {
			serve.RecordFailure(ctx, s.store, s.logger, serve.Failure{
				CollectorID: c.ID, OrgID: orgID, DirtySeq: expectedSeq[c.ID], Path: serve.PathEager, Err: storeErr,
			})
			continue
		}
		if !written {
			// Superseded: the row was re-marked after this recompute read
			// its generation, or another recompute already served this
			// generation. The recompute that saw the newer mark writes.
			s.logger.Debug("recomputeOrgCaches: write superseded by a newer mark", "collector_id", c.ID.String())
		}
	}
}
