package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0036_collector_instance_per_collector keys collector_instances by
// (collector_id, id): one wire id may have a row under several collectors. Its
// down migration is lossy by design — pin that it keeps each id's most
// recently seen row and restores the single-column key.
var _ = Describe("Migration: 0036_collector_instance_per_collector", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		url string
		c1  string
		c2  string
	)

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 36)).To(Succeed())
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		var cl string
		Expect(db.QueryRow(ctx, `INSERT INTO clusters (name) VALUES ('m36') RETURNING id`).Scan(&cl)).To(Succeed())
		Expect(db.QueryRow(ctx, `INSERT INTO collectors (cluster_id, role) VALUES ($1, 'metrics') RETURNING id`, cl).Scan(&c1)).To(Succeed())
		Expect(db.QueryRow(ctx, `INSERT INTO collectors (cluster_id, role) VALUES ($1, 'logs') RETURNING id`, cl).Scan(&c2)).To(Succeed())
	})

	insert := func(ctx context.Context, collectorID, id, ago string) error {
		_, err := db.Exec(ctx, `INSERT INTO collector_instances (id, collector_id, name, last_seen)
			VALUES ($1, $2, $1, now() - $3::interval)`, id, collectorID, ago)
		return err
	}

	It("accepts one wire id under two collectors, but not twice under one", func(ctx context.Context) {
		Expect(insert(ctx, c1, "host-1", "0")).To(Succeed())
		Expect(insert(ctx, c2, "host-1", "0")).To(Succeed())
		Expect(insert(ctx, c1, "host-1", "0")).NotTo(Succeed())
	})

	It("rolls back keeping each id's most recently seen row", func(ctx context.Context) {
		Expect(insert(ctx, c1, "host-1", "1 hour")).To(Succeed())
		Expect(insert(ctx, c2, "host-1", "1 minute")).To(Succeed())
		Expect(insert(ctx, c1, "host-2", "0")).To(Succeed())

		Expect(store.MigrateTo(ctx, url, 35)).To(Succeed())

		var kept string
		Expect(db.QueryRow(ctx, `SELECT collector_id FROM collector_instances WHERE id = 'host-1'`).Scan(&kept)).To(Succeed())
		Expect(kept).To(Equal(c2))
		var n int
		Expect(db.QueryRow(ctx, `SELECT count(*) FROM collector_instances`).Scan(&n)).To(Succeed())
		Expect(n).To(Equal(2))
		Expect(insert(ctx, c1, "host-2", "0")).NotTo(Succeed(), "the wire id is the key again")
	})
})
