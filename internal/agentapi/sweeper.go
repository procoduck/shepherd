package agentapi

import (
	"context"
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

// beaconInventoryExpireAfter bounds how long a beacon_inventory row survives
// without a fresh report before the sweeper deletes it (plan §4, W5: "a
// collector that stops reporting ages out rather than lingering as a
// permanently-healthy ghost"). Five times the baseline pipeline's rendered
// scrape_interval (60s — beacon.RenderBaselinePipeline's caller sets this;
// see internal/beacon/render.go) tolerates a handful of missed/slow
// remote_write batches without flapping a row in and out of existence on
// every transient failure, while still being short enough that "still
// present" stays a meaningful signal. Not wired through config.AgentConfig
// (unlike inactiveAfter/deleteAfter below): this expiry is a property of the
// baseline pipeline's own cadence, which this package also controls, rather
// than an independent operator-tunable knob.
const beaconInventoryExpireAfter = 5 * time.Minute

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
	n, err := sw.store.Queries.DeleteExpiredSessions(ctx)
	if err != nil {
		sw.logger.Error("sweeper: failed to delete expired sessions", "err", err)
	} else if n > 0 {
		sw.logger.Debug("sweeper: deleted expired sessions", "sessions_swept", n)
	}

	// W5 (D6): age out beacon inventory nobody has reported for a while —
	// same shape as the collector-instance sweep above, new table.
	beaconCutoff := pgtype.Timestamptz{Time: now.Add(-beaconInventoryExpireAfter), Valid: true}
	if bn, err := sw.store.Queries.DeleteExpiredBeaconInventory(ctx, beaconCutoff); err != nil {
		sw.logger.Error("sweeper: failed to delete expired beacon inventory", "err", err)
	} else if bn > 0 {
		sw.logger.Debug("sweeper: deleted expired beacon inventory rows", "rows_swept", bn)
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
