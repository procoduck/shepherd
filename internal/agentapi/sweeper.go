package agentapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"shepherd/internal/config"
	"shepherd/internal/store"
)

var tableRowsGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "shepherd_table_rows",
	Help: "Estimated row count per table (pg_class.reltuples).",
}, []string{"table"})

// Sweeper marks collector instances inactive after inactiveAfter and hard-deletes
// them after deleteAfter. It runs on a background goroutine.
type Sweeper struct {
	store         *store.Store
	logger        *slog.Logger
	inactiveAfter time.Duration
	deleteAfter   time.Duration
	tick          time.Duration
}

// NewSweeper creates a Sweeper from agent config.
func NewSweeper(st *store.Store, cfg *config.AgentConfig, logger *slog.Logger) *Sweeper {
	return &Sweeper{
		store:         st,
		logger:        logger,
		inactiveAfter: cfg.InactiveAfter,
		deleteAfter:   cfg.DeleteAfter,
		tick:          cfg.SweepInterval,
	}
}

// Start launches the sweep goroutine and stops when ctx is cancelled.
func (sw *Sweeper) Start(ctx context.Context) {
	go sw.run(ctx)
}

func (sw *Sweeper) run(ctx context.Context) {
	t := time.NewTicker(sw.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sw.sweep(ctx)
		}
	}
}

func (sw *Sweeper) sweep(ctx context.Context) {
	now := time.Now()

	inactiveBefore := pgtype.Timestamptz{Time: now.Add(-sw.inactiveAfter), Valid: true}
	if err := sw.store.Queries.MarkStaleInstancesInactive(ctx, inactiveBefore); err != nil {
		sw.logger.Error("sweeper: failed to mark stale instances inactive", "err", err)
	}

	deleteBefore := pgtype.Timestamptz{Time: now.Add(-sw.deleteAfter), Valid: true}
	if err := sw.store.Queries.DeleteOldInstances(ctx, deleteBefore); err != nil {
		sw.logger.Error("sweeper: failed to delete old instances", "err", err)
	}

	// Sweep expired sessions; log count at Debug when any were deleted.
	// Note: DeleteExpiredSessions is :exec and doesn't return a count.
	// Use a raw query via the pool for the count.
	n, err := sw.sweepSessions(ctx)
	if err != nil {
		sw.logger.Error("sweeper: failed to delete expired sessions", "err", err)
	} else if n > 0 {
		sw.logger.Debug("sweeper: deleted expired sessions", "sessions_swept", n)
	}

	sw.refreshTableGauges(ctx)
}

func (sw *Sweeper) refreshTableGauges(ctx context.Context) {
	for _, table := range []string{"audit_log", "pipeline_revisions"} {
		var count float64
		// RAW-SQL-OK: pg_class system catalog query — not representable as a sqlc query
		err := sw.store.Pool().QueryRow(ctx,
			`SELECT reltuples::bigint FROM pg_class WHERE relname = $1`, table).Scan(&count)
		if err != nil {
			sw.logger.Debug("sweeper: failed to read reltuples", "table", table, "err", err)
			continue
		}
		tableRowsGauge.WithLabelValues(table).Set(count)
	}
}

func (sw *Sweeper) sweepSessions(ctx context.Context) (int64, error) {
	// RAW-SQL-OK: needs RowsAffected count; sqlc :exec does not return pgconn.CommandTag
	tag, err := sw.store.Pool().Exec(ctx, "DELETE FROM sessions WHERE expires_at < now()")
	if err != nil {
		return 0, fmt.Errorf("deleting expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
