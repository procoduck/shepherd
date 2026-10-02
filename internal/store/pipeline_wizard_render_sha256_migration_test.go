package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0030_pipeline_wizard_render_sha256 (#262 follow-up): the render
// fingerprint a wizard write stores on its pipeline. Additive and nullable
// with no default or backfill — existing rows read NULL ("unknown"), which
// the hand-edit check treats as "fall back to a fresh render". Down drops it.
var _ = Describe("Migration: 0030_pipeline_wizard_render_sha256", Label("integration"), func() {
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

	It("adds a nullable wizard_render_sha256 that an insert omitting it leaves NULL", func(ctx context.Context) {
		var nullable string
		Expect(db.QueryRow(ctx,
			`SELECT is_nullable FROM information_schema.columns
			 WHERE table_name = 'pipelines' AND column_name = 'wizard_render_sha256'`,
		).Scan(&nullable)).To(Succeed(), "column wizard_render_sha256 must exist on pipelines")
		Expect(nullable).To(Equal("YES"))

		var orgID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('m30', 'M30', 'g') RETURNING id`,
		).Scan(&orgID)).To(Succeed())
		var fp *string
		Expect(db.QueryRow(ctx,
			`INSERT INTO pipelines (org_id, name, source) VALUES ($1, 'm30-p', 'wizard') RETURNING wizard_render_sha256`,
			orgID,
		).Scan(&fp)).To(Succeed())
		Expect(fp).To(BeNil(), "no default: a pipeline written before 0030 must read as unknown")
	})

	It("drops the column on MigrateTo(29)", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateTo(ctx, url, 29)).To(Succeed())
		probe, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer probe.Close()
		var exists bool
		Expect(probe.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = 'pipelines' AND column_name = 'wizard_render_sha256')`,
		).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse())
	})
})
