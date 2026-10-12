package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0033_serve_cache_format: every cached served config records the
// serve format it was computed in (internal/serve.Format); a row in an older
// format is recomputed before it is served. Existing rows are format 0. A
// dirty mark — which always bumps dirty_seq — resets a row to format 0, so an
// older binary's recompute (which names its columns and never sets format)
// can never inherit the format a newer binary stamped. Down drops all of it.
var _ = Describe("Migration: 0033_serve_cache_format", Label("integration"), func() {
	var (
		db        *pgxpool.Pool
		url       string
		collector string
	)

	reopen := func(ctx context.Context) {
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 32)).To(Succeed())
		reopen(ctx)
		DeferCleanup(func() { db.Close() })

		var clusterID string
		Expect(db.QueryRow(ctx, `INSERT INTO clusters (name) VALUES ('m33') RETURNING id`).Scan(&clusterID)).To(Succeed())
		Expect(db.QueryRow(ctx,
			`INSERT INTO collectors (cluster_id, role) VALUES ($1, 'metrics') RETURNING id`, clusterID,
		).Scan(&collector)).To(Succeed())
		_, err := db.Exec(ctx,
			`INSERT INTO serve_cache (collector_id, content, hash, dirty, dirty_seq) VALUES ($1, '// rev 0 bytes', 'h1', false, 4)`,
			collector)
		Expect(err).NotTo(HaveOccurred())

		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
	})

	format := func(ctx context.Context) int16 {
		var f int16
		Expect(db.QueryRow(ctx, `SELECT format FROM serve_cache WHERE collector_id = $1`, collector).Scan(&f)).To(Succeed())
		return f
	}

	It("adds format with default 0 to existing rows", func(ctx context.Context) {
		Expect(format(ctx)).To(BeEquivalentTo(0))
	})

	It("resets format on a dirty mark and keeps it on a recompute write", func(ctx context.Context) {
		_, err := db.Exec(ctx, `UPDATE serve_cache SET format = 1 WHERE collector_id = $1`, collector)
		Expect(err).NotTo(HaveOccurred())
		// A recompute write (no generation bump), as an older binary issues it.
		_, err = db.Exec(ctx, `UPDATE serve_cache SET content = 'x', hash = 'h2', dirty = false WHERE collector_id = $1`, collector)
		Expect(err).NotTo(HaveOccurred())
		Expect(format(ctx)).To(BeEquivalentTo(1), "only a mark resets the format")

		// The mark every binary issues (MarkServeCacheDirty*, 0032).
		_, err = db.Exec(ctx, `UPDATE serve_cache SET dirty = true, dirty_seq = dirty_seq + 1 WHERE collector_id = $1`, collector)
		Expect(err).NotTo(HaveOccurred())
		Expect(format(ctx)).To(BeEquivalentTo(0))
	})

	// The real mark path: MarkServeCacheDirty* (and every older binary's
	// copy of them) is an INSERT … ON CONFLICT DO UPDATE SET dirty_seq, not
	// a plain UPDATE. The trigger must fire for it too.
	It("resets format when the upsert-shaped mark hits an existing row", func(ctx context.Context) {
		_, err := db.Exec(ctx, `UPDATE serve_cache SET format = 1 WHERE collector_id = $1`, collector)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.Exec(ctx, `INSERT INTO serve_cache (collector_id, content, hash, dirty, dirty_seq)
VALUES ($1, '', '', true, 1)
ON CONFLICT (collector_id) DO UPDATE SET
    dirty = true,
    dirty_seq = serve_cache.dirty_seq + 1`, collector)
		Expect(err).NotTo(HaveOccurred())
		Expect(format(ctx)).To(BeEquivalentTo(0))
		var seq int64
		Expect(db.QueryRow(ctx, `SELECT dirty_seq FROM serve_cache WHERE collector_id = $1`, collector).Scan(&seq)).To(Succeed())
		Expect(seq).To(BeEquivalentTo(5))
	})

	It("rolls back to 32 by dropping the column, trigger and function", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateTo(ctx, url, 32)).To(Succeed())
		reopen(ctx)
		var n int
		Expect(db.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.columns WHERE table_name = 'serve_cache' AND column_name = 'format'`,
		).Scan(&n)).To(Succeed())
		Expect(n).To(BeZero())
		Expect(db.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname = 'serve_cache_mark_resets_format'`).Scan(&n)).To(Succeed())
		Expect(n).To(BeZero())
		// The mark still works without the trigger.
		_, err := db.Exec(ctx, `UPDATE serve_cache SET dirty = true, dirty_seq = dirty_seq + 1 WHERE collector_id = $1`, collector)
		Expect(err).NotTo(HaveOccurred())
		// And up again is clean.
		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		Expect(format(ctx)).To(BeEquivalentTo(0))
	})
})
