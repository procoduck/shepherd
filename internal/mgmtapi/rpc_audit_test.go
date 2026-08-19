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

// Connect-path coverage for AuditService, over the same
// newRPCWiringRouter(...) wiring rpc_wiring_test.go and rpc_fleet_test.go
// use — a happy path, an authz denial, and an error-code mapping case, per
// docs/api-contract-design.md's testing rules.
var _ = Describe("shepherd.mgmt.v1.AuditService RPC", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		authHandler *auth.Handler
		server      *httptest.Server
		orgID       pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          "audit-rpc-org",
			DisplayName:   "Audit RPC Org",
			AdminGroupID:  "audit-admin-group",
			ReaderGroupID: pgtype.Text{String: "audit-reader-group", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

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
		sessionID := fmt.Sprintf("audit-rpc-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "audit-user-oid", Email: "audit-user@example.com", DisplayName: "Audit User",
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

	It("lists audit entries for an org admin (happy path)", func() {
		err := st.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
			Actor: "tester", ActorType: "user", OrgID: orgID, Action: "org.create",
			ResourceType: "org", ResourceID: orgID.String(), Detail: json.RawMessage("{}"),
		})
		Expect(err).NotTo(HaveOccurred())

		admin := createSession(false, []string{"audit-admin-group"})
		resp := postConnect("/shepherd.mgmt.v1.AuditService/ListAudit", map[string]any{
			"orgId": orgID.String(), "action": "org.create",
		}, admin)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		payload := decodeBody(resp)
		items, ok := payload["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items).To(HaveLen(1))
		item, ok := items[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(item["action"]).To(Equal("org.create"))
		Expect(item["actor"]).To(Equal("tester"))
		Expect(payload["total"]).To(Equal(1.0))
	})

	It("returns an org's entries when no actor/action filter is supplied", func() {
		// Regression: the generated SQL bound an omitted filter as SQL '' rather
		// than NULL, so `action = ''` matched nothing and an unfiltered list came
		// back empty — the audit API returned zero rows for every caller that did
		// not already know an exact action string. NULLIF in the query fixes it.
		err := st.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
			Actor: "unfiltered-tester", ActorType: "user", OrgID: orgID, Action: "pipeline.create",
			ResourceType: "pipeline", ResourceID: orgID.String(), Detail: json.RawMessage("{}"),
		})
		Expect(err).NotTo(HaveOccurred())

		admin := createSession(false, []string{"audit-admin-group"})
		resp := postConnect("/shepherd.mgmt.v1.AuditService/ListAudit", map[string]any{
			"orgId": orgID.String(),
		}, admin)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		payload := decodeBody(resp)
		items, ok := payload["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(len(items)).To(BeNumerically(">=", 1), "unfiltered list must return the org's entries")
		Expect(payload["total"]).To(BeNumerically(">=", 1.0))
	})

	It("denies ListAudit for an org reader (AuditService requires org-admin)", func() {
		reader := createSession(false, []string{"audit-reader-group"})
		resp := postConnect("/shepherd.mgmt.v1.AuditService/ListAudit", map[string]any{"orgId": orgID.String()}, reader)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("permission_denied"))
	})

	It("maps an unauthenticated ListAudit call to the Connect unauthenticated code (401)", func() {
		resp := postConnect("/shepherd.mgmt.v1.AuditService/ListAudit", map[string]any{"orgId": orgID.String()}, nil)
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("unauthenticated"))
	})
})
