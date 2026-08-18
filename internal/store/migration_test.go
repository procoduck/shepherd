package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

var _ = Describe("Migration: idx_collector_instances_last_seen", Label("integration"), func() {
	It("index exists after migration", func(ctx context.Context) {
		Expect(store.MigrateUp(ctx, sharedPG.RootURL)).To(Succeed())

		db, err := pgxpool.New(ctx, sharedPG.RootURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		var indexExists bool
		err = db.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM pg_indexes
				WHERE tablename = 'collector_instances'
				AND indexname = 'idx_collector_instances_last_seen'
			)`).Scan(&indexExists)
		Expect(err).NotTo(HaveOccurred())
		Expect(indexExists).To(BeTrue())
	})
})
