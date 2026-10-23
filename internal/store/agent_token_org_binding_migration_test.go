package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Migration 0038_agent_token_org_binding binds agent tokens to an org in the
// shape of an agent_identities binding (org + cluster/role allowlists). Pin
// what the agent API and the admin API lean on: a token that existed before
// the migration stays unbound (no guessed org) with empty allowlists, the
// allowlists must be jsonb arrays, deleting an org unbinds its tokens rather
// than blocking the delete, and the scoped list/revoke/bind queries.
var _ = Describe("Migration: 0038_agent_token_org_binding", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		q   *sqlc.Queries
		url string
	)

	hash := func(s string) []byte {
		h := sha256.Sum256([]byte(s))
		return h[:]
	}
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
		// Stop just before 0038 so a token can be written the way pre-0038
		// code wrote one.
		Expect(store.MigrateTo(ctx, url, 37)).To(Succeed())
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		q = sqlc.New(db)
	})

	It("leaves a pre-existing token unbound with empty allowlists", func(ctx context.Context) {
		var id pgtype.UUID
		Expect(db.QueryRow(ctx, // RAW-SQL-OK: a row as pre-0038 code wrote it
			`INSERT INTO agent_tokens (name, token_hash, created_by) VALUES ('legacy', '\x00', 'cli') RETURNING id`,
		).Scan(&id)).To(Succeed())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		tok, err := q.GetAgentTokenByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(tok.OrgID.Valid).To(BeFalse(), "the migration must not guess an org")
		Expect(string(tok.Clusters)).To(MatchJSON(`[]`))
		Expect(string(tok.Roles)).To(MatchJSON(`[]`))
		Expect(tok.MayClaim).To(BeFalse(), "only an app admin's bind lets a token claim")
	})

	Describe("after the migration", func() {
		var orgA, orgB sqlc.Org

		BeforeEach(func(ctx context.Context) {
			Expect(store.MigrateUp(ctx, url)).To(Succeed())
			var err error
			orgA, err = q.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-a", DisplayName: "A", AdminGroupID: "ga"})
			Expect(err).NotTo(HaveOccurred())
			orgB, err = q.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "B", AdminGroupID: "gb"})
			Expect(err).NotTo(HaveOccurred())
		})

		It("stores a NULL allowlist as the empty one, and refuses a non-array", func(ctx context.Context) {
			tok, err := q.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "t", TokenHash: hash("t"), OrgID: orgA.ID})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(tok.Clusters)).To(MatchJSON(`[]`))

			_, err = q.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
				Name: "bad", TokenHash: hash("bad"), OrgID: orgA.ID, Clusters: json.RawMessage(`"prod"`),
			})
			Expect(err).To(HaveOccurred(), "the CHECK keeps an allowlist an array")
		})

		It("unbinds an org's tokens when the org is deleted", func(ctx context.Context) {
			tok, err := q.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "t", TokenHash: hash("t"), OrgID: orgB.ID})
			Expect(err).NotTo(HaveOccurred())
			Expect(q.DeleteOrg(ctx, orgB.ID)).To(Succeed())
			got, err := q.GetAgentTokenByID(ctx, tok.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.OrgID.Valid).To(BeFalse())
		})

		It("lists unbound tokens first, and scopes a list and a revoke to one org", func(ctx context.Context) {
			a, err := q.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "a", TokenHash: hash("a"), OrgID: orgA.ID})
			Expect(err).NotTo(HaveOccurred())
			b, err := q.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "b", TokenHash: hash("b"), OrgID: orgB.ID})
			Expect(err).NotTo(HaveOccurred())
			legacy, err := q.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "legacy", TokenHash: hash("l")})
			Expect(err).NotTo(HaveOccurred())

			all, err := q.ListAgentTokens(ctx, pgtype.UUID{})
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(3))
			Expect(all[0].AgentToken.ID).To(Equal(legacy.ID), "unbound first")
			Expect(all[0].OrgName.Valid).To(BeFalse())

			onlyA, err := q.ListAgentTokens(ctx, orgA.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(onlyA).To(HaveLen(1))
			Expect(onlyA[0].AgentToken.ID).To(Equal(a.ID))
			Expect(onlyA[0].OrgName.String).To(Equal("org-a"))

			for _, other := range []pgtype.UUID{b.ID, legacy.ID} {
				_, err = q.RevokeAgentToken(ctx, sqlc.RevokeAgentTokenParams{ID: other, OrgID: orgA.ID})
				Expect(errors.Is(err, pgx.ErrNoRows)).To(BeTrue(), "an org-scoped revoke never reaches another org's or an unbound token")
			}
			_, err = q.RevokeAgentToken(ctx, sqlc.RevokeAgentTokenParams{ID: a.ID, OrgID: orgA.ID})
			Expect(err).NotTo(HaveOccurred())
		})

		It("binds a live token in place, keeping its hash, and never a revoked one", func(ctx context.Context) {
			legacy, err := q.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{Name: "legacy", TokenHash: hash("l")})
			Expect(err).NotTo(HaveOccurred())
			bound, err := q.BindAgentToken(ctx, sqlc.BindAgentTokenParams{ID: legacy.ID, OrgID: orgA.ID, Clusters: arr("prod-eu-1"), Roles: arr()})
			Expect(err).NotTo(HaveOccurred())
			Expect(bound.OrgID).To(Equal(orgA.ID))
			Expect(bound.TokenHash).To(Equal(legacy.TokenHash))
			Expect(bound.MayClaim).To(BeTrue(), "an app admin's bind lets the token claim clusters")

			_, err = q.RevokeAgentToken(ctx, sqlc.RevokeAgentTokenParams{ID: legacy.ID})
			Expect(err).NotTo(HaveOccurred())
			_, err = q.BindAgentToken(ctx, sqlc.BindAgentTokenParams{ID: legacy.ID, OrgID: orgB.ID, Clusters: arr(), Roles: arr()})
			Expect(errors.Is(err, pgx.ErrNoRows)).To(BeTrue())
		})

		It("drops the binding columns on the down migration", func(ctx context.Context) {
			Expect(store.MigrateTo(ctx, url, 37)).To(Succeed())
			var n int
			Expect(db.QueryRow(ctx, // RAW-SQL-OK: schema assertion
				`SELECT count(*) FROM information_schema.columns
				 WHERE table_name = 'agent_tokens' AND column_name IN ('org_id', 'clusters', 'roles', 'may_claim')`,
			).Scan(&n)).To(Succeed())
			Expect(n).To(Equal(0))
		})
	})
})
