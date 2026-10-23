package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Migration 0039_org_tenants: an organisation can own several tenants and
// tenant prefixes. Pin the move (each org's tenant_id becomes its default
// exact tenant, and back), and the constraints SQL enforces on its own: an
// exact tenant is unique across all orgs, one default per org, the default is
// exact, and the tenant charset. Prefix overlap across orgs is a relation SQL
// cannot index; the API enforces it (internal/mgmtapi, org_tenants specs).
var _ = Describe("Migration: 0039_org_tenants", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		q   *sqlc.Queries
		url string
	)

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		// Stop just before 0039 so orgs can be written the way pre-0039 code
		// wrote them, with orgs.tenant_id.
		Expect(store.MigrateTo(ctx, url, 38)).To(Succeed())
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		q = sqlc.New(db)
	})

	legacyOrg := func(ctx context.Context, name, tenant string) pgtype.UUID {
		var id pgtype.UUID
		var t any
		if tenant != "" {
			t = tenant
		}
		Expect(db.QueryRow(ctx, // RAW-SQL-OK: a row as pre-0039 code wrote it
			`INSERT INTO orgs (name, display_name, admin_group_id, tenant_id) VALUES ($1, $1, 'g', $2) RETURNING id`,
			name, t).Scan(&id)).To(Succeed())
		return id
	}

	It("carries each org's tenant over as its default exact tenant, and back", func(ctx context.Context) {
		acme := legacyOrg(ctx, "acme", "acme")
		none := legacyOrg(ctx, "none", "")
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		rows, err := q.ListOrgTenants(ctx, acme)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Tenant).To(Equal("acme"))
		Expect(rows[0].Kind).To(Equal("exact"))
		Expect(rows[0].IsDefault).To(BeTrue(), "the org's one tenant is what its routes and chart values used")
		rows, err = q.ListOrgTenants(ctx, none)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty(), "the migration must not invent a tenant")

		// More tenants after the move; the down migration keeps the default.
		_, err = q.AddOrgTenant(ctx, sqlc.AddOrgTenantParams{OrgID: acme, Tenant: "acme-eu", Kind: "exact"})
		Expect(err).NotTo(HaveOccurred())
		_, err = q.AddOrgTenant(ctx, sqlc.AddOrgTenantParams{OrgID: none, Tenant: "none-", Kind: "prefix"})
		Expect(err).NotTo(HaveOccurred())
		Expect(store.MigrateTo(ctx, url, 38)).To(Succeed())

		var got pgtype.Text
		Expect(db.QueryRow(ctx, `SELECT tenant_id FROM orgs WHERE id = $1`, acme).Scan(&got)).To(Succeed()) // RAW-SQL-OK: pre-0039 column
		Expect(got.String).To(Equal("acme"))
		Expect(db.QueryRow(ctx, `SELECT tenant_id FROM orgs WHERE id = $1`, none).Scan(&got)).To(Succeed()) // RAW-SQL-OK: pre-0039 column
		Expect(got.Valid).To(BeFalse(), "an org without a default goes back to having no tenant")
	})

	Describe("after the migration", func() {
		var orgA, orgB pgtype.UUID

		BeforeEach(func(ctx context.Context) {
			Expect(store.MigrateUp(ctx, url)).To(Succeed())
			a, err := q.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-a", DisplayName: "A", AdminGroupID: "ga"})
			Expect(err).NotTo(HaveOccurred())
			b, err := q.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "B", AdminGroupID: "gb"})
			Expect(err).NotTo(HaveOccurred())
			orgA, orgB = a.ID, b.ID
		})

		add := func(ctx context.Context, org pgtype.UUID, tenant, kind string, isDefault bool) error {
			_, err := q.AddOrgTenant(ctx, sqlc.AddOrgTenantParams{OrgID: org, Tenant: tenant, Kind: kind, IsDefault: isDefault})
			return err
		}

		It("keeps an exact tenant to one org", func(ctx context.Context) {
			Expect(add(ctx, orgA, "shared", "exact", false)).To(Succeed())
			Expect(add(ctx, orgB, "shared", "exact", false)).To(HaveOccurred())
			Expect(add(ctx, orgB, "shared-", "prefix", false)).To(Succeed(), "a prefix is not an exact tenant")
			Expect(add(ctx, orgA, "shared-", "prefix", false)).To(HaveOccurred(), "the very same prefix twice")
		})

		It("allows one default per org, and only an exact one", func(ctx context.Context) {
			Expect(add(ctx, orgA, "a1", "exact", true)).To(Succeed())
			Expect(add(ctx, orgA, "a2", "exact", true)).To(HaveOccurred())
			Expect(add(ctx, orgA, "a-", "prefix", true)).To(HaveOccurred())
			Expect(add(ctx, orgB, "b1", "exact", true)).To(Succeed(), "the limit is per org")
		})

		It("holds tenants to the tenant charset and length", func(ctx context.Context) {
			Expect(add(ctx, orgA, "a/b", "exact", false)).To(HaveOccurred())
			Expect(add(ctx, orgA, "a b-", "prefix", false)).To(HaveOccurred())
			Expect(add(ctx, orgA, "__mimir_cluster", "exact", false)).To(HaveOccurred())
			Expect(add(ctx, orgA, "x", "other", false)).To(HaveOccurred(), "unknown kind")
		})

		It("goes with its org", func(ctx context.Context) {
			Expect(add(ctx, orgB, "b1", "exact", true)).To(Succeed())
			Expect(q.DeleteOrg(ctx, orgB)).To(Succeed())
			Expect(add(ctx, orgA, "b1", "exact", false)).To(Succeed(), "the deleted org's tenant is free again")
		})
	})
})
