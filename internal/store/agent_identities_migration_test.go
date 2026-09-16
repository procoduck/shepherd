package store_test

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Migration 0021_agent_identities (docs/plans/2026-09-16-agent-oidc-auth.md,
// resolution mode 1): binds a collector's OIDC identity (issuer + app id) to
// an org, so org assignment stays in Shepherd. Pin the constraints the
// enforcement path will lean on — unique (issuer, app_id), the org FK, the
// jsonb-array shape of the allowlists — and the sqlc queries that read them.
var _ = Describe("Migration: 0021_agent_identities", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		q   *sqlc.Queries
		url string
		org sqlc.Org
	)

	arr := func(vs ...string) json.RawMessage {
		if vs == nil {
			vs = []string{}
		}
		b, err := json.Marshal(vs)
		Expect(err).NotTo(HaveOccurred())
		return b
	}

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		q = sqlc.New(db)

		org, err = q.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "platform-eng", DisplayName: "Platform Eng", AdminGroupID: "g-admin"})
		Expect(err).NotTo(HaveOccurred())
	})

	It("creates a binding and reads it back by (issuer, app_id)", func(ctx context.Context) {
		created, err := q.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
			Issuer: "https://idp.example/", AppID: "client-eu", OrgID: org.ID,
			Clusters: arr("prod-eu-1"), Roles: arr(), CreatedBy: "cli",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created.OrgID).To(Equal(org.ID))

		got, err := q.GetAgentIdentityByAppID(ctx, sqlc.GetAgentIdentityByAppIDParams{Issuer: "https://idp.example/", AppID: "client-eu"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.OrgID).To(Equal(org.ID))
		Expect(string(got.Clusters)).To(Equal(`["prod-eu-1"]`))
		Expect(string(got.Roles)).To(Equal(`[]`), "empty roles must round-trip as [] = any")
	})

	It("refuses a second binding for the same (issuer, app_id)", func(ctx context.Context) {
		p := sqlc.CreateAgentIdentityParams{Issuer: "https://idp.example/", AppID: "client-dup", OrgID: org.ID, Clusters: arr(), Roles: arr(), CreatedBy: "cli"}
		_, err := q.CreateAgentIdentity(ctx, p)
		Expect(err).NotTo(HaveOccurred())
		_, err = q.CreateAgentIdentity(ctx, p)
		Expect(err).To(HaveOccurred(), "one client identity must resolve to exactly one org")
	})

	It("cascades the binding away when its org is deleted", func(ctx context.Context) {
		_, err := q.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{Issuer: "i", AppID: "a", OrgID: org.ID, Clusters: arr(), Roles: arr(), CreatedBy: "cli"})
		Expect(err).NotTo(HaveOccurred())

		_, err = db.Exec(ctx, `DELETE FROM orgs WHERE id = $1`, org.ID)
		Expect(err).NotTo(HaveOccurred())

		_, err = q.GetAgentIdentityByAppID(ctx, sqlc.GetAgentIdentityByAppIDParams{Issuer: "i", AppID: "a"})
		Expect(err).To(HaveOccurred(), "ON DELETE CASCADE must remove the binding with its org")
	})

	It("refuses a binding to a non-existent org", func(ctx context.Context) {
		var missing pgtype.UUID
		Expect(missing.Scan("00000000-0000-0000-0000-000000000000")).To(Succeed())
		_, err := q.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{Issuer: "i", AppID: "a", OrgID: missing, Clusters: arr(), Roles: arr(), CreatedBy: "cli"})
		Expect(err).To(HaveOccurred(), "org_id FK must reject an unknown org")
	})

	It("rejects a non-array in the clusters column", func(ctx context.Context) {
		_, err := db.Exec(ctx,
			`INSERT INTO agent_identities (issuer, app_id, org_id, clusters) VALUES ('i', 'a', $1, '"not-an-array"'::jsonb)`, org.ID)
		Expect(err).To(HaveOccurred(), "the CHECK must keep a hand-written row's allowlist a JSON array")
	})

	It("lists bindings with the org slug and deletes by identity", func(ctx context.Context) {
		_, err := q.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{Issuer: "https://idp/", AppID: "c1", OrgID: org.ID, Clusters: arr(), Roles: arr("metrics"), CreatedBy: "cli"})
		Expect(err).NotTo(HaveOccurred())

		rows, err := q.ListAgentIdentities(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].OrgName).To(Equal("platform-eng"))
		Expect(string(rows[0].Roles)).To(Equal(`["metrics"]`))

		n, err := q.DeleteAgentIdentity(ctx, sqlc.DeleteAgentIdentityParams{Issuer: "https://idp/", AppID: "c1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(1)))

		rows, err = q.ListAgentIdentities(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})

	It("drops the table on the down migration", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateTo(ctx, url, 20)).To(Succeed())

		probe, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer probe.Close()

		var exists bool
		Expect(probe.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'agent_identities')`,
		).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse(), "down migration should drop agent_identities")
	})
})
