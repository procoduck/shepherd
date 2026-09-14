package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0018_service_account_role (docs/gateway-tier-plan.md, D3): gives
// a service account a role TIER (editor/admin) of its own, orthogonal to
// capability (0012_teams_service_accounts, propose vs apply). Before this
// column existed, authorizeServiceAccountProcedure checked only
// sa.OrgID == orgID for every non-app-admin requirement.
//
// DEFAULT 'editor' NOT NULL is a deliberate narrowing of every pre-existing
// service account, not a no-op — pin that every existing row reads back
// editor, that an insert omitting role also lands on editor, that the CHECK
// refuses anything else, and that the down migration cleanly removes the
// column, so a later edit to the CHECK or the default can't silently change
// enforcement without a test going red.
var _ = Describe("Migration: 0018_service_account_role", Label("integration"), func() {
	var (
		db    *pgxpool.Pool
		url   string
		orgID string
	)

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('w18-mig', 'W18 Migration', 'g') RETURNING id`,
		).Scan(&orgID)).To(Succeed())
	})

	It("backfills a pre-existing service_accounts row to role=editor", func(ctx context.Context) {
		// Simulate a row written by pre-0018 application code, which had no
		// role column to set: insert every column 0018 did not add.
		var saID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO service_accounts (org_id, name, capability, token_hash, created_by)
			 VALUES ($1, 'pre-existing', 'propose', '\x00', 'admin@example.com') RETURNING id`,
			orgID,
		).Scan(&saID)).To(Succeed())

		var role string
		Expect(db.QueryRow(ctx, `SELECT role FROM service_accounts WHERE id = $1`, saID).Scan(&role)).To(Succeed())
		Expect(role).To(Equal("editor"),
			"DEFAULT 'editor' NOT NULL must backfill every existing service account, since an apply-capability "+
				"account that depended on the unchecked org-match gap must now land on the narrower tier")
	})

	It("defaults role to editor when an insert omits it", func(ctx context.Context) {
		var saID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO service_accounts (org_id, name, capability, token_hash, created_by)
			 VALUES ($1, 'no-role-given', 'apply', '\x00', 'admin@example.com') RETURNING id`,
			orgID,
		).Scan(&saID)).To(Succeed())

		var role string
		Expect(db.QueryRow(ctx, `SELECT role FROM service_accounts WHERE id = $1`, saID).Scan(&role)).To(Succeed())
		Expect(role).To(Equal("editor"), "role must default to editor, never admin, for any insert that doesn't say")
	})

	It("refuses a role outside editor/admin", func(ctx context.Context) {
		_, err := db.Exec(ctx,
			`INSERT INTO service_accounts (org_id, name, capability, role, token_hash, created_by)
			 VALUES ($1, 'bad-role', 'propose', 'super-admin', '\x00', 'admin@example.com')`, orgID)
		Expect(err).To(HaveOccurred(),
			"role is the TIER axis checked in authorizeServiceAccountProcedure; the database must not accept a "+
				"third value the Go code has no policy for")
	})

	It("accepts both roles the Go constants name", func(ctx context.Context) {
		for _, role := range []string{"editor", "admin"} {
			_, err := db.Exec(ctx,
				`INSERT INTO service_accounts (org_id, name, capability, role, token_hash, created_by)
				 VALUES ($1, $2, 'propose', $3, '\x00', 'admin@example.com')`, orgID, "sa-role-"+role, role)
			Expect(err).NotTo(HaveOccurred(), "role %q must be accepted", role)
		}
	})

	It("drops the role column on the down migration", func(ctx context.Context) {
		// 0018 is no longer head (0019_pipeline_revision_wizard_state
		// followed it), so a bare MigrateDown would revert 0019, not 0018.
		// MigrateTo names the target version explicitly: land on 17
		// (immediately before 0018) so this spec keeps proving what it has
		// always proven — the 0018 down migration drops role — regardless
		// of how many migrations are added after it.
		db.Close()
		Expect(store.MigrateTo(ctx, url, 17)).To(Succeed())

		probe, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer probe.Close()

		var roleColExists bool
		Expect(probe.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = 'service_accounts' AND column_name = 'role')`,
		).Scan(&roleColExists)).To(Succeed())
		Expect(roleColExists).To(BeFalse(), "down migration should drop service_accounts.role")
	})
})
