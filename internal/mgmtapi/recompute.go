package mgmtapi

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/merge"
	"shepherd/internal/serve"
	"shepherd/internal/store/sqlc"
)

// The eager serve path: after a write changes what an org's collectors are
// served, recompute and store every collector's served config instead of
// waiting for its next poll. internal/agentapi's GetConfig is the lazy path;
// both go through serve.ComputeServed, serve.Store and serve.RecordFailure.

// RecomputeOrgCaches is recomputeOrgCaches for callers outside this package.
func (s *PipelineService) RecomputeOrgCaches(ctx context.Context, orgID pgtype.UUID) {
	s.recomputeOrgCaches(ctx, orgID)
}

// RecomputeCollectorCache is recomputeCollectorCache for callers outside this
// package: internal/gitsync runs it after a sync changes a link's git
// pipelines (server.go wires it with gitsync.WithRecompute). A git pipeline
// is served to its link's collector only, so no other collector is touched.
func (s *PipelineService) RecomputeCollectorCache(ctx context.Context, orgID, collectorID pgtype.UUID) {
	s.recomputeCollectorCache(ctx, orgID, collectorID)
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
	in, err := s.loadOrgServeInputs(ctx, orgID)
	if err != nil {
		s.logger.Error("recomputeOrgCaches: loading serve inputs failed", "org_id", orgID.String(), "err", err)
		return
	}
	for i := range in.collectors {
		s.recomputeCollector(ctx, in, in.collectors[i])
	}
}

// recomputeCollectorCache is recomputeOrgCaches narrowed to one collector,
// the eager recompute for a write whose effect is confined to a single
// collector (DeleteRepoLink and a git sync: a repo link belongs to exactly one
// collector).
// It loads the same org-wide inputs -- the dirty generations read before the
// pipelines -- and runs the identical per-collector step, so the bytes and
// hash it stores are the ones recomputeOrgCaches would have stored for that
// collector. Every other collector's cache row is left untouched.
func (s *PipelineService) recomputeCollectorCache(ctx context.Context, orgID, collectorID pgtype.UUID) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("recomputeCollectorCache panicked",
				"org_id", orgID.String(), "collector_id", collectorID.String(), "panic", r)
		}
	}()
	in, err := s.loadOrgServeInputs(ctx, orgID)
	if err != nil {
		s.logger.Error("recomputeCollectorCache: loading serve inputs failed",
			"org_id", orgID.String(), "collector_id", collectorID.String(), "err", err)
		return
	}
	for i := range in.collectors {
		if in.collectors[i].ID == collectorID {
			s.recomputeCollector(ctx, in, in.collectors[i])
			return
		}
	}
	// Gone from the org since the write (deleted, or its cluster released):
	// this org has nothing to serve it.
	s.logger.Debug("recomputeCollectorCache: collector not in org",
		"org_id", orgID.String(), "collector_id", collectorID.String())
}

// orgServeInputs is what the eager recompute reads once per org and shares
// across that org's collectors.
type orgServeInputs struct {
	orgID              pgtype.UUID
	collectors         []sqlc.ListCollectorsWithClusterByOrgRow
	allowLabelMatching bool
	localAttrs         map[string]map[string]string
	// expectedSeq is each cache row's dirty generation, read before the
	// pipelines were loaded; a collector with no row yet has generation 0.
	expectedSeq map[pgtype.UUID]int64
	pipelines   []merge.Pipeline
	// loadErr is why the pipelines could not be loaded in their served form
	// (a strict org's pinned import that cannot be inlined): every
	// collector's recompute fails with it and keeps its last good config.
	loadErr error
}

func (s *PipelineService) loadOrgServeInputs(ctx context.Context, orgID pgtype.UUID) (*orgServeInputs, error) {
	collectors, err := s.store.Queries.ListCollectorsWithClusterByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing collectors: %w", err)
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
		return nil, fmt.Errorf("reading cache generations: %w", err)
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
	// The served form (serve.LoadPipelines), as the lazy path loads it: a
	// strict org's pinned import.git modules inlined. One that cannot be
	// inlined fails every collector's recompute (loadErr), never serves raw.
	pipelines, loadErr := serve.LoadPipelines(ctx, s.store, orgID, s.requireOwned)
	return &orgServeInputs{
		loadErr:            loadErr,
		orgID:              orgID,
		collectors:         collectors,
		allowLabelMatching: org.AllowLabelMatching,
		localAttrs:         localAttrs,
		expectedSeq:        expectedSeq,
		// The same row -> merge.Pipeline conversion agentapi's lazy recompute
		// uses, current revision included (#296): the two paths must serve
		// byte-identical config or the hash moves whenever a poll switches path.
		pipelines: pipelines,
	}, nil
}

// recomputeCollector computes and stores one collector's served config from
// its org's shared inputs: the single per-collector step of the eager path,
// shared by recomputeOrgCaches and recomputeCollectorCache. A failure keeps
// the collector's previous entry and is reported through serve.RecordFailure.
func (s *PipelineService) recomputeCollector(ctx context.Context, in *orgServeInputs, c sqlc.ListCollectorsWithClusterByOrgRow) {
	seq := in.expectedSeq[c.ID]
	if in.loadErr != nil {
		serve.RecordFailure(ctx, s.store, s.logger, serve.Failure{
			CollectorID: c.ID, OrgID: in.orgID, DirtySeq: seq, Path: serve.PathEager, Err: in.loadErr,
		})
		return
	}
	// internal/serve.ComputeServed is the single merge -> append-baseline
	// -> hash -> Stage-1-validate implementation this eager path shares
	// with internal/agentapi's lazy recompute (docs/gateway-tier-plan.md
	// §10), role enforcement included.
	served, err := serve.ComputeServed(ctx,
		serve.Deps{Schema: s.schema, BeaconBaseline: s.beaconBaseline},
		serve.Collector{ID: c.ID.String(), Cluster: c.ClusterName, Role: c.Role, AdminLabels: adminLabelsIfAllowed(in.allowLabelMatching, c.Labels), LocalAttrs: in.localAttrs[c.ID.String()]},
		in.pipelines,
	)
	if err != nil {
		serve.RecordFailure(ctx, s.store, s.logger, serve.Failure{
			CollectorID: c.ID, OrgID: in.orgID, DirtySeq: seq, Path: serve.PathEager, Err: err,
		})
		return
	}
	for _, ex := range served.Exclusions {
		if ex.Kind == merge.ExclusionTenantCheck {
			// Strict tenant mode's serve-time check; recorded and audited
			// once per change by serve.LoadPipelines.
			s.logger.Debug("eager recompute: pipeline excluded (strict tenant check)",
				"collector_id", c.ID.String(), "pipeline", ex.PipelineName)
			continue
		}
		// Visible per docs/gateway-tier-plan.md §8 rule 2: a pipeline
		// excluded for a role/signal mismatch is never silent. The
		// generated header (served.Content) already names it too; this
		// log line is what makes it discoverable without opening the
		// merged config.
		s.logger.Warn("eager recompute: pipeline excluded (role/signal mismatch)",
			"collector_id", c.ID.String(), "role", c.Role, "pipeline", ex.PipelineName, "reason", ex.Reason)
	}
	if served.BaselineErr != nil {
		s.logger.Error("eager recompute: appending beacon baseline pipeline failed; serving without it",
			"collector_id", c.ID.String(), "err", served.BaselineErr)
	}

	_, written, storeErr := serve.Store(ctx, s.store.Queries, c.ID, seq, served)
	if storeErr != nil {
		serve.RecordFailure(ctx, s.store, s.logger, serve.Failure{
			CollectorID: c.ID, OrgID: in.orgID, DirtySeq: seq, Path: serve.PathEager, Err: storeErr,
		})
		return
	}
	if !written {
		// Superseded: the row was re-marked after this recompute read
		// its generation, or another recompute already served this
		// generation. The recompute that saw the newer mark writes.
		s.logger.Debug("eager recompute: write superseded by a newer mark", "collector_id", c.ID.String())
	}
}
