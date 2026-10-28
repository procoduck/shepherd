package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/metrics"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Store writes one ComputeServed result to serve_cache — the one cache write
// both serve paths make (internal/agentapi's lazy GetConfig recompute and
// internal/mgmtapi's eager recomputeOrgCaches). It is a compare-and-swap on
// the dirty generation: expectedSeq is the dirty_seq the caller read BEFORE it
// loaded pipelines (0 when the collector had no row yet), and the row is
// stamped with this binary's Format.
//
// written=false with a nil error means superseded: a newer mark landed while
// the recompute ran, or another recompute already served this generation.
// That is not a failure, and what to do about it is the caller's policy — the
// lazy path still has a poll to answer, the eager path simply moves on,
// because the recompute that observed the newer mark writes instead.
//
// r.Provenance is written in the same row write as the content it describes
// (F-CONTRIB, #423).
func Store(ctx context.Context, q *sqlc.Queries, collectorID pgtype.UUID, expectedSeq int64, r Result) (row sqlc.ServeCache, written bool, err error) {
	provenance, err := json.Marshal(r.Provenance)
	if err != nil {
		return sqlc.ServeCache{}, false, fmt.Errorf("encoding serve provenance: %w", err)
	}
	row, err = q.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
		CollectorID: collectorID,
		Content:     r.Content,
		Hash:        r.Hash,
		DirtySeq:    expectedSeq,
		Format:      Format,
		Provenance:  provenance,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ServeCache{}, false, nil
	}
	if err != nil {
		return sqlc.ServeCache{}, false, fmt.Errorf("writing serve cache: %w", err)
	}
	return row, true, nil
}

// Path names which serve path a recompute ran on, for RecordFailure's log
// line and audit detail.
type Path string

const (
	// PathLazy is internal/agentapi's recompute inside GetConfig.
	PathLazy Path = "lazy"
	// PathEager is internal/mgmtapi's recomputeOrgCaches after a write.
	PathEager Path = "eager"
)

// AuditActionRecomputeFailed is the audit_log action RecordFailure writes.
const AuditActionRecomputeFailed = "serve.recompute_failed"

// Failure describes one failed recompute of one collector's served config.
type Failure struct {
	CollectorID pgtype.UUID
	OrgID       pgtype.UUID
	// DirtySeq is the generation the recompute tried to serve: the dirty_seq
	// the caller read before loading pipelines, the same value it would have
	// passed to Store.
	DirtySeq int64
	Path     Path
	Err      error
}

// RecordFailure is what both serve paths do when a collector's recompute
// fails (spec §6.3): the previous cache entry stays served (the caller simply
// does not write), the failure is logged at Error and counted in
// shepherd_serve_recompute_failures_total on every attempt, and it is written
// to audit_log once per (collector, dirty_seq) — MarkServeCacheFailed is the
// compare-and-swap that makes repeated polls of the same broken generation add
// no further rows, while the next mark (a new generation) is reported again.
//
// The audit detail carries the error text. Every error a recompute returns is
// built from pipeline and block names, matcher text, a Stage 1 verdict with no
// config content, or a database error — never from pipeline contents, so a
// destination credential rendered into a pipeline cannot reach it.
//
// Best-effort: a failure to record the failure is logged, never returned —
// the caller is already on its fail-soft path.
func RecordFailure(ctx context.Context, st *store.Store, logger *slog.Logger, f Failure) {
	metrics.ServeRecomputeFailuresTotal.Inc()
	logger.Error("serve-cache recompute failed; previous config stays served",
		"path", string(f.Path), "collector_id", f.CollectorID.String(), "dirty_seq", f.DirtySeq, "err", f.Err)

	if err := auditFailure(ctx, st, f); err != nil {
		logger.Error("serve-cache recompute failed: recording the audit row failed",
			"path", string(f.Path), "collector_id", f.CollectorID.String(), "err", err)
	}
}

func auditFailure(ctx context.Context, st *store.Store, f Failure) error {
	detail, err := json.Marshal(map[string]any{
		"dirty_seq": f.DirtySeq,
		"path":      string(f.Path),
		"error":     f.Err.Error(),
	})
	if err != nil {
		return fmt.Errorf("encoding audit detail: %w", err)
	}

	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	q := st.Queries.WithTx(tx)

	if _, err := q.MarkServeCacheFailed(ctx, sqlc.MarkServeCacheFailedParams{
		CollectorID: f.CollectorID,
		FailedSeq:   f.DirtySeq,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// This generation's failure is already in audit_log.
			return nil
		}
		return fmt.Errorf("recording failed generation: %w", err)
	}
	if err := q.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		Actor:        "serve",
		ActorType:    "system",
		OrgID:        f.OrgID,
		Action:       AuditActionRecomputeFailed,
		ResourceType: "collector",
		ResourceID:   f.CollectorID.String(),
		Detail:       detail,
	}); err != nil {
		return fmt.Errorf("writing audit row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	return nil
}
