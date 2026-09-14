package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

var _ = Describe("Migration: 0020_serve_cache_dirty_seq", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		url string
	)

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
	})

	It("adds dirty_seq as a NOT NULL bigint defaulting to 0 on serve_cache", func(ctx context.Context) {
		var dataType, isNullable, dflt string
		Expect(db.QueryRow(ctx,
			`SELECT data_type, is_nullable, column_default FROM information_schema.columns
			 WHERE table_name = 'serve_cache' AND column_name = 'dirty_seq'`,
		).Scan(&dataType, &isNullable, &dflt)).To(Succeed())
		Expect(dataType).To(Equal("bigint"))
		Expect(isNullable).To(Equal("NO"))
		Expect(dflt).To(Equal("0"))
	})

	It("drops the column on the down migration and restores it on the way back up", func(ctx context.Context) {
		Expect(store.MigrateTo(ctx, url, 19)).To(Succeed())
		var exists bool
		Expect(db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'serve_cache' AND column_name = 'dirty_seq')`,
		).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		Expect(db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'serve_cache' AND column_name = 'dirty_seq')`,
		).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeTrue())
	})
})
