package mgmtapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Org-bound agent tokens (migration 0038) over the real Connect wiring: an org
// admin creates, lists and revokes only its own org's tokens and never sees an
// unbound or another org's token; BindAgentToken is app admin only and binds
// in place (the secret hash is untouched); an app admin sees every token.
var _ = Describe("AdminService agent tokens", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		orgA   sqlc.Org
		orgB   sqlc.Org
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		orgA, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "org-a", DisplayName: "Org A", AdminGroupID: "a-admin",
			ViewerGroupID: pgtype.Text{String: "a-viewer", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgB, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "org-b", DisplayName: "Org B", AdminGroupID: "b-admin"})
		Expect(err).NotTo(HaveOccurred())

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	session := func(appAdmin bool, groups ...string) *http.Cookie {
		if groups == nil {
			groups = []string{}
		}
		groupsJSON, err := json.Marshal(groups)
		Expect(err).NotTo(HaveOccurred())
		id := fmt.Sprintf("tokens-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: id, UserOid: "oid", Email: "u@example.com", DisplayName: "U",
			GroupIds: groupsJSON, IsAppAdmin: appAdmin,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:    "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: id}
	}

	call := func(method string, body map[string]any, cookie *http.Cookie) (int, map[string]any) {
		buf, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest(http.MethodPost, server.URL+"/shepherd.mgmt.v1.AdminService/"+method, strings.NewReader(string(buf)))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		raw, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var payload map[string]any
		Expect(json.Unmarshal(raw, &payload)).To(Succeed(), string(raw))
		return resp.StatusCode, payload
	}

	// seedToken inserts a token row directly; a nil org is an unbound token.
	seedToken := func(name string, org *sqlc.Org) sqlc.AgentToken {
		hash := sha256.Sum256([]byte(name + "-secret"))
		p := sqlc.CreateAgentTokenParams{Name: name, TokenHash: hash[:], CreatedBy: "test"}
		if org != nil {
			p.OrgID = org.ID
		}
		tok, err := st.Queries.CreateAgentToken(ctx, p)
		Expect(err).NotTo(HaveOccurred())
		return tok
	}

	names := func(payload map[string]any) []string {
		items, _ := payload["items"].([]any) //nolint:errcheck // shape asserted by the caller
		out := make([]string, 0, len(items))
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(item).NotTo(HaveKey("secret"), "a list never carries a secret")
			out = append(out, item["name"].(string))
		}
		return out
	}

	auditCount := func(action string, org pgtype.UUID) int {
		var n int
		Expect(st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = $1 AND org_id = $2`, action, org).Scan(&n)).To(Succeed()) // RAW-SQL-OK: test assertion
		return n
	}

	Describe("an org admin", func() {
		It("creates a token bound to its own org, with allowlists, and the create is audited to that org", func() {
			code, created := call("CreateAgentToken", map[string]any{
				"name": "a-collectors", "orgId": orgA.ID.String(),
				"clusters": []string{"prod-eu-1"}, "roles": []string{"metrics"},
			}, session(false, "a-admin"))
			Expect(code).To(Equal(http.StatusOK))
			Expect(created["secret"]).NotTo(BeEmpty())

			var id pgtype.UUID
			Expect(id.Scan(created["id"])).To(Succeed())
			tok, err := st.Queries.GetAgentTokenByID(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(tok.OrgID).To(Equal(orgA.ID))
			Expect(string(tok.Clusters)).To(MatchJSON(`["prod-eu-1"]`))
			Expect(string(tok.Roles)).To(MatchJSON(`["metrics"]`))
			Expect(tok.MayClaim).To(BeFalse(), "an org admin's token never claims clusters")
			Expect(auditCount("agent_token.create", orgA.ID)).To(Equal(1))
		})

		It("cannot create a token for another org", func() {
			code, _ := call("CreateAgentToken", map[string]any{"name": "x", "orgId": orgB.ID.String()}, session(false, "a-admin"))
			Expect(code).To(Equal(http.StatusForbidden))
		})

		It("cannot create a token without an org", func() {
			code, _ := call("CreateAgentToken", map[string]any{"name": "x"}, session(false, "a-admin"))
			Expect(code).NotTo(Equal(http.StatusOK))
		})

		It("lists only its own org's tokens — never an unbound or another org's token", func() {
			seedToken("a-1", &orgA)
			seedToken("b-1", &orgB)
			seedToken("legacy", nil)

			code, listed := call("ListAgentTokens", map[string]any{"orgId": orgA.ID.String()}, session(false, "a-admin"))
			Expect(code).To(Equal(http.StatusOK))
			Expect(names(listed)).To(ConsistOf("a-1"))

			code, _ = call("ListAgentTokens", map[string]any{}, session(false, "a-admin"))
			Expect(code).NotTo(Equal(http.StatusOK), "the unscoped list (every token) is app admin only")

			code, _ = call("ListAgentTokens", map[string]any{"orgId": orgB.ID.String()}, session(false, "a-admin"))
			Expect(code).To(Equal(http.StatusForbidden))
		})

		It("revokes its own org's token, and gets NotFound for another org's or an unbound token", func() {
			mine := seedToken("a-1", &orgA)
			theirs := seedToken("b-1", &orgB)
			legacy := seedToken("legacy", nil)
			admin := session(false, "a-admin")

			code, _ := call("RevokeAgentToken", map[string]any{"id": theirs.ID.String(), "orgId": orgA.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusNotFound))
			code, _ = call("RevokeAgentToken", map[string]any{"id": legacy.ID.String(), "orgId": orgA.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusNotFound))
			code, _ = call("RevokeAgentToken", map[string]any{"id": theirs.ID.String(), "orgId": orgB.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusForbidden))
			code, _ = call("RevokeAgentToken", map[string]any{"id": theirs.ID.String()}, admin)
			Expect(code).NotTo(Equal(http.StatusOK))

			_, err := st.Queries.GetAgentTokenByID(ctx, theirs.ID)
			Expect(err).NotTo(HaveOccurred(), "org B's token is still live")
			_, err = st.Queries.GetAgentTokenByID(ctx, legacy.ID)
			Expect(err).NotTo(HaveOccurred(), "the unbound token is still live")

			code, _ = call("RevokeAgentToken", map[string]any{"id": mine.ID.String(), "orgId": orgA.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusOK))
			_, err = st.Queries.GetAgentTokenByID(ctx, mine.ID)
			Expect(err).To(HaveOccurred(), "revoked")
			Expect(auditCount("agent_token.revoke", orgA.ID)).To(Equal(1))
		})

		It("cannot bind a token, not even an unbound one to its own org", func() {
			legacy := seedToken("legacy", nil)
			code, _ := call("BindAgentToken", map[string]any{"id": legacy.ID.String(), "orgId": orgA.ID.String()}, session(false, "a-admin"))
			Expect(code).To(Equal(http.StatusForbidden))
			tok, err := st.Queries.GetAgentTokenByID(ctx, legacy.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(tok.OrgID.Valid).To(BeFalse())
		})
	})

	// An admin-tier service account reaches exactly an org admin's token
	// scope (sa=same-org,tier>=admin in the golden), and like an org admin
	// its tokens never claim clusters.
	It("an admin-tier service account mints only its own org's tokens, which never claim", func() {
		secret := "tokens-sa-secret"
		hash := sha256.Sum256([]byte(secret))
		sa, err := st.Queries.CreateServiceAccount(ctx, sqlc.CreateServiceAccountParams{
			OrgID: orgA.ID, Name: "ci", Capability: "apply", Role: "admin", TokenHash: hash[:], CreatedBy: "admin@example.com",
		})
		Expect(err).NotTo(HaveOccurred())
		saCall := func(method string, body map[string]any) (int, map[string]any) {
			buf, err := json.Marshal(body)
			Expect(err).NotTo(HaveOccurred())
			req, err := http.NewRequest(http.MethodPost, server.URL+"/shepherd.mgmt.v1.AdminService/"+method, strings.NewReader(string(buf)))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.Header.Set("Shepherd-On-Behalf-Of", "admin@example.com")
			req.SetBasicAuth(sa.ID.String(), secret)
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			raw, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			var payload map[string]any
			Expect(json.Unmarshal(raw, &payload)).To(Succeed(), string(raw))
			return resp.StatusCode, payload
		}

		code, created := saCall("CreateAgentToken", map[string]any{"name": "sa-tok", "orgId": orgA.ID.String()})
		Expect(code).To(Equal(http.StatusOK), "%v", created)
		var id pgtype.UUID
		Expect(id.Scan(created["id"])).To(Succeed())
		tok, err := st.Queries.GetAgentTokenByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(tok.OrgID).To(Equal(orgA.ID))
		Expect(tok.MayClaim).To(BeFalse(), "a service account's token never claims clusters")

		code, _ = saCall("CreateAgentToken", map[string]any{"name": "x", "orgId": orgB.ID.String()})
		Expect(code).To(Equal(http.StatusForbidden))
		code, _ = saCall("ListAgentTokens", map[string]any{})
		Expect(code).To(Equal(http.StatusForbidden), "the every-token view is app admin only")
		legacy := seedToken("legacy", nil)
		code, _ = saCall("BindAgentToken", map[string]any{"id": legacy.ID.String(), "orgId": orgA.ID.String()})
		Expect(code).To(Equal(http.StatusForbidden))
	})

	It("an org viewer cannot list, create or revoke the org's tokens", func() {
		mine := seedToken("a-1", &orgA)
		viewer := session(false, "a-viewer")
		code, _ := call("ListAgentTokens", map[string]any{"orgId": orgA.ID.String()}, viewer)
		Expect(code).To(Equal(http.StatusForbidden))
		code, _ = call("CreateAgentToken", map[string]any{"name": "x", "orgId": orgA.ID.String()}, viewer)
		Expect(code).To(Equal(http.StatusForbidden))
		code, _ = call("RevokeAgentToken", map[string]any{"id": mine.ID.String(), "orgId": orgA.ID.String()}, viewer)
		Expect(code).To(Equal(http.StatusForbidden))
	})

	Describe("an app admin", func() {
		It("lists every token, unbound first, with org and allowlists", func() {
			seedToken("a-1", &orgA)
			seedToken("legacy", nil)
			code, listed := call("ListAgentTokens", map[string]any{}, session(true))
			Expect(code).To(Equal(http.StatusOK))
			Expect(names(listed)).To(Equal([]string{"legacy", "a-1"}))
			items := listed["items"].([]any)
			Expect(items[0].(map[string]any)).NotTo(HaveKey("orgId"), "an unbound token has no org")
			Expect(items[1].(map[string]any)["orgName"]).To(Equal("org-a"))
		})

		It("binds an unbound token in place — the secret hash is unchanged — and rebinds it, audited each time", func() {
			legacy := seedToken("legacy", nil)
			admin := session(true)

			code, bound := call("BindAgentToken", map[string]any{
				"id": legacy.ID.String(), "orgId": orgA.ID.String(), "clusters": []string{"prod-eu-1"},
			}, admin)
			Expect(code).To(Equal(http.StatusOK))
			Expect(bound["orgId"]).To(Equal(orgA.ID.String()))
			Expect(bound["orgName"]).To(Equal("org-a"))
			Expect(bound["clusters"]).To(ConsistOf("prod-eu-1"))

			after, err := st.Queries.GetAgentTokenByID(ctx, legacy.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(after.TokenHash).To(Equal(legacy.TokenHash), "binding never touches the secret")
			Expect(after.OrgID).To(Equal(orgA.ID))
			Expect(after.MayClaim).To(BeFalse(), "a bind never grants a claim right the token did not have")
			Expect(bound).NotTo(HaveKey("mayClaim"))
			Expect(auditCount("agent_token.bind", orgA.ID)).To(Equal(1))

			code, _ = call("BindAgentToken", map[string]any{"id": legacy.ID.String(), "orgId": orgB.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusOK))
			after, err = st.Queries.GetAgentTokenByID(ctx, legacy.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(after.OrgID).To(Equal(orgB.ID))
			Expect(string(after.Clusters)).To(MatchJSON(`[]`), "a rebind replaces the allowlists")
			Expect(auditCount("agent_token.bind", orgB.ID)).To(Equal(1))
		})

		// #389: a bind never grants a claim right the token did not have — an
		// org admin's token (may_claim=false) stays non-claiming when an app
		// admin rebinds it, and an app admin's token keeps its right.
		It("keeps a token's original claim right across a rebind", func() {
			admin := session(true)
			code, created := call("CreateAgentToken", map[string]any{"name": "org-tok", "orgId": orgA.ID.String()}, session(false, "a-admin"))
			Expect(code).To(Equal(http.StatusOK), "%v", created)
			var orgTok pgtype.UUID
			Expect(orgTok.Scan(created["id"])).To(Succeed())
			code, created = call("CreateAgentToken", map[string]any{"name": "app-tok", "orgId": orgA.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusOK), "%v", created)
			var appTok pgtype.UUID
			Expect(appTok.Scan(created["id"])).To(Succeed())

			for id, want := range map[pgtype.UUID]bool{orgTok: false, appTok: true} {
				code, bound := call("BindAgentToken", map[string]any{"id": id.String(), "orgId": orgB.ID.String()}, admin)
				Expect(code).To(Equal(http.StatusOK), "%v", bound)
				after, err := st.Queries.GetAgentTokenByID(ctx, id)
				Expect(err).NotTo(HaveOccurred())
				Expect(after.OrgID).To(Equal(orgB.ID))
				Expect(after.MayClaim).To(Equal(want), "a rebind keeps may_claim")
				Expect(bound["mayClaim"] == true).To(Equal(want))
			}
		})

		It("refuses to bind to an unknown org, with a role no collector can register, or a revoked token", func() {
			legacy := seedToken("legacy", nil)
			admin := session(true)
			code, _ := call("BindAgentToken", map[string]any{"id": legacy.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusBadRequest))
			code, _ = call("BindAgentToken", map[string]any{"id": legacy.ID.String(), "orgId": "00000000-0000-4000-8000-000000000099"}, admin)
			Expect(code).To(Equal(http.StatusBadRequest))
			code, _ = call("BindAgentToken", map[string]any{"id": legacy.ID.String(), "orgId": orgA.ID.String(), "roles": []string{"bogusrole"}}, admin)
			Expect(code).To(Equal(http.StatusBadRequest))

			code, _ = call("RevokeAgentToken", map[string]any{"id": legacy.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusOK))
			code, _ = call("BindAgentToken", map[string]any{"id": legacy.ID.String(), "orgId": orgA.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusNotFound))
		})

		It("creates a token for any org, and requires one", func() {
			admin := session(true)
			code, created := call("CreateAgentToken", map[string]any{"name": "b-tok", "orgId": orgB.ID.String()}, admin)
			Expect(code).To(Equal(http.StatusOK))
			var id pgtype.UUID
			Expect(id.Scan(created["id"])).To(Succeed())
			tok, err := st.Queries.GetAgentTokenByID(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(tok.MayClaim).To(BeTrue(), "an app admin's token may claim clusters")
			code, _ = call("CreateAgentToken", map[string]any{"name": "nowhere"}, admin)
			Expect(code).To(Equal(http.StatusBadRequest))
		})
	})
})
