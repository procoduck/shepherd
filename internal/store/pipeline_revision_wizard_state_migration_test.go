package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0019_pipeline_revision_wizard_state (docs/plans/2026-09-11-f-revisions.md,
// F-REVISIONS): gives each pipeline_revisions row its own copy of the
// visual-builder graph document, nullable with no default. Pin that the
// column exists and stays nullable after MigrateUp, that a row inserted
// without it reads back NULL rather than failing or defaulting to
// something else, and that the down migration cleanly removes it.
var _ = Describe("Migration: 0019_pipeline_revision_wizard_state", Label("integration"), func() {
	var (
		db         *pgxpool.Pool
		url        string
		pipelineID string
	)

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		var orgID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('w19-mig', 'W19 Migration', 'g') RETURNING id`,
		).Scan(&orgID)).To(Succeed())

		Expect(db.QueryRow(ctx,
			`INSERT INTO pipelines (org_id, name, source) VALUES ($1, 'w19-pipeline', 'ui') RETURNING id`,
			orgID,
		).Scan(&pipelineID)).To(Succeed())
	})

	It("adds wizard_state as a nullable column on pipeline_revisions", func(ctx context.Context) {
		var exists, nullable bool
		Expect(db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = 'pipeline_revisions' AND column_name = 'wizard_state'),
			        COALESCE((SELECT is_nullable = 'YES' FROM information_schema.columns
			                  WHERE table_name = 'pipeline_revisions' AND column_name = 'wizard_state'), false)`,
		).Scan(&exists, &nullable)).To(Succeed())
		Expect(exists).To(BeTrue(), "column wizard_state must exist on pipeline_revisions")
		Expect(nullable).To(BeTrue(), "wizard_state must be nullable — a NULL means this revision predates "+
			"0019 or the pipeline has no graph")
	})

	It("reads back NULL when an insert omits wizard_state", func(ctx context.Context) {
		var revID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO pipeline_revisions (pipeline_id, revision, contents, matchers, enabled, changed_by, change_note)
			 VALUES ($1, 1, 'x', '[]', false, 'tester', 'no wizard_state') RETURNING id`,
			pipelineID,
		).Scan(&revID)).To(Succeed())

		var wizardState *string
		Expect(db.QueryRow(ctx,
			`SELECT wizard_state::text FROM pipeline_revisions WHERE id = $1`, revID,
		).Scan(&wizardState)).To(Succeed())
		Expect(wizardState).To(BeNil(), "an insert omitting wizard_state must read back NULL, not a default")
	})

	It("drops wizard_state on MigrateTo(18)", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateTo(ctx, url, 18)).To(Succeed())

		probe, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer probe.Close()

		var wizardStateColExists bool
		Expect(probe.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = 'pipeline_revisions' AND column_name = 'wizard_state')`,
		).Scan(&wizardStateColExists)).To(Succeed())
		Expect(wizardStateColExists).To(BeFalse(), "down migration should drop pipeline_revisions.wizard_state")
	})
})
