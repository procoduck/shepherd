package agentapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"shepherd/internal/config"
	"shepherd/internal/metrics"
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
	// Publish the gauges once before the first tick. The sweep interval
	// defaults to 5m, so waiting for the ticker would leave
	// shepherd_active_collectors reading 0 for five minutes after every
	// rollout — a fresh instance of the same "gauge that lies" problem this
	// wiring exists to fix, and one that would fire a false alert on every
	// deploy. Only the read-only gauge refresh runs here; the destructive
	// sweeps keep waiting for their interval.
	sw.refreshTableGauges(ctx)
	sw.refreshActiveCollectors(ctx)

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
	sw.refreshActiveCollectors(ctx)
}

// refreshActiveCollectors sets the shepherd_active_collectors gauge.
//
// The gauge was declared with this exact help text and never set by anything,
// so /metrics reported a flat 0 while collectors were polling steadily. A
// gauge that is always zero is worse than a missing one: it cannot be alerted
// on, and an alert written against it looks like coverage while being
// incapable of firing. The sweeper owns it because the sweeper is already the
// thing that decides which instances count as inactive.
func (sw *Sweeper) refreshActiveCollectors(ctx context.Context) {
	n, err := sw.store.Queries.CountActiveInstances(ctx)
	if err != nil {
		// Left at its previous value rather than zeroed: reporting 0 because a
		// count failed is the same lie the gauge used to tell.
		sw.logger.Debug("sweeper: failed to count active instances", "err", err)
		return
	}
	metrics.ActiveCollectors.Set(float64(n))
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
		if count < 0 {
			// Postgres reports reltuples = -1 for a table it has never
			// analyzed (PG14+). Publishing that verbatim put "-1 rows" on a
			// dashboard; leaving the series unset says "unknown", which is
			// what it actually means.
			sw.logger.Debug("sweeper: table not yet analyzed, row estimate unavailable", "table", table)
			continue
		}
		tableRowsGauge.WithLabelValues(table).Set(count)
	}
}
