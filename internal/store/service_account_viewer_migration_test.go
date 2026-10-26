package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0042_service_account_viewer (#362, review step 7b): a service
// account may hold the viewer tier, and a viewer carries no capability —
// capability is NULL for a viewer and required (propose or apply) for editor
// and admin. Pin that rows written before 0042 read back unchanged, that the
// pairing holds in both directions, and that the down migration restores the
// old shape (deleting viewers, never widening them).
var _ = Describe("Migration: 0042_service_account_viewer", Label("integration"), func() {
	var (
		db    *pgxpool.Pool
		url   string
		orgID string
	)

	reopen := func(ctx context.Context) {
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
	}

	insert := func(ctx context.Context, name string, role, capability any) error {
		_, err := db.Exec(ctx,
			`INSERT INTO service_accounts (org_id, name, capability, role, token_hash, created_by)
			 VALUES ($1, $2, $3, $4, '\x00', 'admin@example.com')`, orgID, name, capability, role)
		return err
	}

	// RAW-SQL-OK: migration specs drive the schema directly.
	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 41)).To(Succeed())
		reopen(ctx)
		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('m42', 'M42', 'g') RETURNING id`,
		).Scan(&orgID)).To(Succeed())
		// Rows a v0.19 binary could write: every tier/capability pair.
		for _, r := range [][2]string{{"editor", "propose"}, {"editor", "apply"}, {"admin", "propose"}, {"admin", "apply"}} {
			Expect(insert(ctx, "pre-"+r[0]+"-"+r[1], r[0], r[1])).To(Succeed())
		}
		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		DeferCleanup(func() { db.Close() })
	})

	It("leaves every pre-existing row unchanged", func(ctx context.Context) {
		rows, err := db.Query(ctx, `SELECT name, role, capability FROM service_accounts ORDER BY name`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		got := map[string][2]string{}
		for rows.Next() {
			var name, role string
			var capability *string
			Expect(rows.Scan(&name, &role, &capability)).To(Succeed())
			Expect(capability).NotTo(BeNil(), name)
			got[name] = [2]string{role, *capability}
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(got).To(Equal(map[string][2]string{
			"pre-admin-apply":    {"admin", "apply"},
			"pre-admin-propose":  {"admin", "propose"},
			"pre-editor-apply":   {"editor", "apply"},
			"pre-editor-propose": {"editor", "propose"},
		}))
	})

	It("accepts a viewer with no capability", func(ctx context.Context) {
		Expect(insert(ctx, "viewer", "viewer", nil)).To(Succeed())
	})

	It("refuses a viewer with a capability", func(ctx context.Context) {
		for _, c := range []string{"propose", "apply"} {
			Expect(insert(ctx, "viewer-"+c, "viewer", c)).
				To(MatchError(ContainSubstring("service_accounts_role_capability")), c)
		}
	})

	It("refuses an editor or admin with no capability", func(ctx context.Context) {
		for _, r := range []string{"editor", "admin"} {
			Expect(insert(ctx, "nocap-"+r, r, nil)).
				To(MatchError(ContainSubstring("service_accounts_role_capability")), r)
		}
	})

	It("still refuses a capability or role outside the known values", func(ctx context.Context) {
		Expect(insert(ctx, "bad-cap", "editor", "write")).To(MatchError(ContainSubstring("violates check constraint")))
		Expect(insert(ctx, "bad-role", "super-admin", "apply")).To(MatchError(ContainSubstring("violates check constraint")))
	})

	It("refuses an UPDATE that would give a viewer a capability", func(ctx context.Context) {
		Expect(insert(ctx, "viewer-upd", "viewer", nil)).To(Succeed())
		_, err := db.Exec(ctx, `UPDATE service_accounts SET capability = 'apply' WHERE name = 'viewer-upd'`)
		Expect(err).To(MatchError(ContainSubstring("service_accounts_role_capability")))
	})

	It("deletes viewers and restores the old constraints on the down migration", func(ctx context.Context) {
		Expect(insert(ctx, "viewer-down", "viewer", nil)).To(Succeed())
		db.Close()
		Expect(store.MigrateTo(ctx, url, 41)).To(Succeed())
		reopen(ctx)

		var viewers, kept int
		Expect(db.QueryRow(ctx, `SELECT count(*) FROM service_accounts WHERE role = 'viewer'`).Scan(&viewers)).To(Succeed())
		Expect(viewers).To(BeZero(), "a viewer must be removed, never widened to editor")
		Expect(db.QueryRow(ctx, `SELECT count(*) FROM service_accounts`).Scan(&kept)).To(Succeed())
		Expect(kept).To(Equal(4), "the down migration must keep every editor and admin row")

		Expect(insert(ctx, "viewer-after-down", "viewer", "propose")).To(MatchError(ContainSubstring("service_accounts_role_check")))
		Expect(insert(ctx, "nocap-after-down", "editor", nil)).To(MatchError(ContainSubstring("not-null")))
	})
})
