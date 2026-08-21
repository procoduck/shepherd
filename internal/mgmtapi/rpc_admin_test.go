package mgmtapi_test

import (
	"context"
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

// Connect-path coverage for AdminService and MeService, over the same
// newRPCWiringRouter(...) wiring rpc_wiring_test.go proves 404-guards and
// unimplemented-stub behavior with — this suite exercises the now-migrated
// AdminService/MeService methods themselves: a happy path, an authz denial,
// and an error-code mapping case per service, per
// docs/archive/api-contract-design.md's testing rules. mgmtapi_test.go covers the
// REST shim paths (the compatibility oracle) and is unchanged by this
// migration.
var _ = Describe("shepherd.mgmt.v1 AdminService and MeService RPC", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		authHandler *auth.Handler
		server      *httptest.Server
		orgIDStr    string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          "admin-rpc-org",
			DisplayName:   "Admin RPC Org",
			AdminGroupID:  "admin-rpc-admin-group",
			ReaderGroupID: pgtype.Text{String: "admin-rpc-reader-group", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgIDStr = o.ID.String()

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler = auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	createSession := func(isAppAdmin bool, groupIDs []string) *http.Cookie {
		groupsJSON, err := json.Marshal(groupIDs)
		Expect(err).NotTo(HaveOccurred())
		sessionID := fmt.Sprintf("admin-rpc-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "admin-user-oid", Email: "admin-user@example.com", DisplayName: "Admin User",
			GroupIds:   groupsJSON,
			IsAppAdmin: isAppAdmin,
			ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:     "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: sessionID}
	}

	postConnect := func(procedure string, body map[string]any, cookie *http.Cookie) *http.Response {
		buf, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(string(buf)))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	decodeBody := func(resp *http.Response) map[string]any {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var payload map[string]any
		Expect(json.Unmarshal(body, &payload)).To(Succeed())
		return payload
	}

	Describe("AdminService", func() {
		It("creates and lists an org for an app-admin session (happy path)", func() {
			appAdmin := createSession(true, nil)

			createResp := postConnect("/shepherd.mgmt.v1.AdminService/CreateOrg", map[string]any{
				"name": "created-via-rpc", "displayName": "Created via RPC", "adminGroupId": "created-admin-group",
			}, appAdmin)
			Expect(createResp.StatusCode).To(Equal(http.StatusOK))
			created := decodeBody(createResp)
			Expect(created["name"]).To(Equal("created-via-rpc"))
			Expect(created["displayName"]).To(Equal("Created via RPC"))

			listResp := postConnect("/shepherd.mgmt.v1.AdminService/ListOrgs", map[string]any{}, appAdmin)
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
			listed := decodeBody(listResp)
			items, ok := listed["items"].([]any)
			Expect(ok).To(BeTrue(), "expected an items array")
			Expect(len(items)).To(BeNumerically(">=", 2), "seed org plus the one just created")
		})

		It("denies ListOrgs for an org-admin session that is not an app admin", func() {
			orgAdmin := createSession(false, []string{"admin-rpc-admin-group"})

			resp := postConnect("/shepherd.mgmt.v1.AdminService/ListOrgs", map[string]any{}, orgAdmin)
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

			payload := decodeBody(resp)
			Expect(payload["code"]).To(Equal("permission_denied"))
		})

		It("maps ClaimCluster on an unknown cluster to the Connect not_found code (404)", func() {
			appAdmin := createSession(true, nil)

			resp := postConnect("/shepherd.mgmt.v1.AdminService/ClaimCluster", map[string]any{
				"cluster": "no-such-cluster", "orgId": orgIDStr,
			}, appAdmin)
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound))

			payload := decodeBody(resp)
			Expect(payload["code"]).To(Equal("not_found"))
		})

		It("creates an agent token whose secret is returned exactly once", func() {
			appAdmin := createSession(true, nil)

			resp := postConnect("/shepherd.mgmt.v1.AdminService/CreateAgentToken", map[string]any{
				"name": "rpc-created-token",
			}, appAdmin)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			payload := decodeBody(resp)
			Expect(payload["name"]).To(Equal("rpc-created-token"))
			secret, ok := payload["secret"].(string)
			Expect(ok).To(BeTrue())
			Expect(secret).NotTo(BeEmpty())

			listResp := postConnect("/shepherd.mgmt.v1.AdminService/ListAgentTokens", map[string]any{}, appAdmin)
			Expect(listResp.StatusCode).To(Equal(http.StatusOK))
			listed := decodeBody(listResp)
			items, ok := listed["items"].([]any)
			Expect(ok).To(BeTrue())
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(item).NotTo(HaveKey("secret"), "list response must never carry the plaintext secret")
			}
		})
	})

	Describe("MeService", func() {
		It("returns the caller's identity and org memberships (happy path)", func() {
			cookie := createSession(false, []string{"admin-rpc-admin-group"})

			resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", map[string]any{}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(resp.Header.Get("Cache-Control")).To(Equal("no-store, no-cache, must-revalidate"))

			payload := decodeBody(resp)
			Expect(payload["email"]).To(Equal("admin-user@example.com"))
			orgs, ok := payload["orgs"].([]any)
			Expect(ok).To(BeTrue(), "expected an orgs array")
			Expect(orgs).To(HaveLen(1))
			org, ok := orgs[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(org["id"]).To(Equal(orgIDStr))
			Expect(org["role"]).To(Equal("admin"))
		})

		It("denies GetMe for a request with no session with the Connect unauthenticated code", func() {
			resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", map[string]any{}, nil)
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

			payload := decodeBody(resp)
			Expect(payload["code"]).To(Equal("unauthenticated"))
		})
	})
})
