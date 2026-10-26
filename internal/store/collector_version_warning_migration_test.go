package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0044_collector_version_warning (#375): the agent API's version
// check of a collector's served config. An existing collector starts with no
// warning and no check; the warning and its timestamp are set and cleared
// together; a mismatch always carries a warning; an older binary's collector
// upsert still works. Down drops every column.
var _ = Describe("Migration: 0044_collector_version_warning", Label("integration"), func() {
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

	// RAW-SQL-OK: migration specs drive the schema directly.
	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 42)).To(Succeed())
		reopen(ctx)
		var cluster string
		Expect(db.QueryRow(ctx, `INSERT INTO clusters (name) VALUES ('m44') RETURNING id`).Scan(&cluster)).To(Succeed())
		Expect(db.QueryRow(ctx,
			`INSERT INTO collectors (cluster_id, role) VALUES ($1, 'metrics') RETURNING id`, cluster,
		).Scan(&collector)).To(Succeed())
		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		DeferCleanup(func() { db.Close() })
	})

	It("starts an existing collector with no warning and no check", func(ctx context.Context) {
		var n int
		Expect(db.QueryRow(ctx,
			`SELECT count(*) FROM collectors WHERE id = $1 AND version_warning IS NULL AND version_warning_at IS NULL
			   AND NOT version_mismatch AND version_checked_hash IS NULL AND version_checked_versions = '{}'
			   AND version_checked_at IS NULL AND version_checked_until IS NULL`,
			collector).Scan(&n)).To(Succeed())
		Expect(n).To(Equal(1))
	})

	It("keeps the warning and its timestamp together, and a mismatch with a warning", func(ctx context.Context) {
		_, err := db.Exec(ctx, `UPDATE collectors SET version_warning = 'x' WHERE id = $1`, collector)
		Expect(err).To(MatchError(ContainSubstring("collectors_version_warning_pair")))
		_, err = db.Exec(ctx, `UPDATE collectors SET version_mismatch = true WHERE id = $1`, collector)
		Expect(err).To(MatchError(ContainSubstring("collectors_version_mismatch_has_warning")))
		_, err = db.Exec(ctx,
			`UPDATE collectors SET version_warning = 'x', version_warning_at = now(), version_mismatch = true WHERE id = $1`, collector)
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts an older binary's collector upsert", func(ctx context.Context) {
		_, err := db.Exec(ctx,
			`INSERT INTO collectors (cluster_id, role) SELECT cluster_id, 'logs' FROM collectors WHERE id = $1`, collector)
		Expect(err).NotTo(HaveOccurred())
	})

	It("drops the columns on down", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateTo(ctx, url, 42)).To(Succeed())
		reopen(ctx)
		var n int
		Expect(db.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.columns WHERE table_name = 'collectors' AND column_name LIKE 'version_%'`,
		).Scan(&n)).To(Succeed())
		Expect(n).To(BeZero())
	})
})
