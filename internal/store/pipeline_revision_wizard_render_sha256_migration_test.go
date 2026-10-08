package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0031_pipeline_revision_wizard_render_sha256 (review of #291):
// the render fingerprint per revision, so RestoreRevision can re-attach a
// restored wizard revision. The backfill stamps a revision only from facts
// the server wrote — the pipeline's own fingerprint on its current
// revision, revision 1 of a wizard_kind pipeline (CommitWizard's), and a
// revision a pipeline.rerender audit row names — and never by change_note.
var _ = Describe("Migration: 0031_pipeline_revision_wizard_render_sha256", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		url string
	)
	hash := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 30)).To(Succeed())
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { db.Close() })
	})

	It("backfills the fingerprints the server can vouch for, and nothing by change_note", func(ctx context.Context) {
		var orgID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('m31', 'M31', 'g') RETURNING id`,
		).Scan(&orgID)).To(Succeed())

		pipeline := func(name, contents string, kind, fp *string) string {
			var id string
			Expect(db.QueryRow(ctx,
				`INSERT INTO pipelines (org_id, name, source, contents, wizard_kind, wizard_render_sha256)
				 VALUES ($1, $2, 'wizard', $3, $4, $5) RETURNING id`,
				orgID, name, contents, kind, fp,
			).Scan(&id)).To(Succeed())
			return id
		}
		revision := func(pid string, n int, contents, note string) {
			_, err := db.Exec(ctx,
				`INSERT INTO pipeline_revisions (pipeline_id, revision, contents, change_note) VALUES ($1, $2, $3, $4)`,
				pid, n, contents, note)
			Expect(err).NotTo(HaveOccurred())
		}
		stamp := func(pid string, n int) *string {
			var fp *string
			Expect(db.QueryRow(ctx,
				`SELECT wizard_render_sha256 FROM pipeline_revisions WHERE pipeline_id = $1 AND revision = $2`,
				pid, n,
			).Scan(&fp)).To(Succeed())
			return fp
		}
		kind := "self-monitoring"

		// Untouched since its wizard: current revision matches the row's fingerprint.
		current := "rendered v2"
		fp := hash(current)
		clean := pipeline("clean", current, &kind, &fp)
		revision(clean, 1, "rendered v1", "created")
		revision(clean, 2, current, `re-rendered: destination "m" updated`)
		// Hand-edited (fingerprint cleared): its wizard revisions come only
		// from revision 1 and the audit row; a caller-written note is not one.
		edited := pipeline("edited", "rendered v2 + edit", &kind, nil)
		revision(edited, 1, "rendered v1", "created")
		revision(edited, 2, "rendered v2", `re-rendered: destination "m" updated`)
		revision(edited, 3, "rendered v2 + edit", "updated")
		revision(edited, 4, "anything", "re-rendered: forged by a caller")
		_, err := db.Exec(ctx,
			`INSERT INTO audit_log (actor, actor_type, org_id, action, resource_type, resource_id, detail)
			 VALUES ('a', 'user', $1, 'pipeline.rerender', 'pipeline', $2, '{"revision": 2}')`,
			orgID, edited)
		Expect(err).NotTo(HaveOccurred())
		// Not a wizard pipeline (no wizard_kind): revision 1 is not a wizard's.
		plain := pipeline("plain", "x", nil, nil)
		revision(plain, 1, "x", "created")

		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())

		Expect(stamp(clean, 2)).To(HaveValue(Equal(fp)), "the current revision, from the row's fingerprint")
		Expect(stamp(clean, 1)).To(HaveValue(Equal(hash("rendered v1"))), "revision 1 of a wizard pipeline")
		Expect(stamp(edited, 1)).To(HaveValue(Equal(hash("rendered v1"))))
		Expect(stamp(edited, 2)).To(HaveValue(Equal(hash("rendered v2"))), "named by a pipeline.rerender audit row")
		Expect(stamp(edited, 3)).To(BeNil(), "a hand edit")
		Expect(stamp(edited, 4)).To(BeNil(), "a re-rendered note with no audit row is not trusted")
		Expect(stamp(plain, 1)).To(BeNil())
	})

	It("drops the column on MigrateTo(30)", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		Expect(store.MigrateTo(ctx, url, 30)).To(Succeed())
		probe, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer probe.Close()
		var exists bool
		Expect(probe.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = 'pipeline_revisions' AND column_name = 'wizard_render_sha256')`,
		).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse())
	})
})
