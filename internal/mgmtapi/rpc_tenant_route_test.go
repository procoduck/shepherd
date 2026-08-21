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

// Connect-path coverage for TenantRouteService (W4,
// docs/gateway-tier-plan.md §4), over the same newRPCWiringRouter(...)
// wiring rpc_destination_test.go uses: a happy path (create, list, rotate,
// revoke), an authz denial, and an error-code mapping case.
var _ = Describe("shepherd.mgmt.v1.TenantRouteService RPC", Label("integration"), func() {
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
			Name:          "tenant-route-rpc-org",
			DisplayName:   "Tenant Route RPC Org",
			AdminGroupID:  "tenant-route-admin-group",
			ReaderGroupID: pgtype.Text{String: "tenant-route-reader-group", Valid: true},
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
		sessionID := fmt.Sprintf("tenant-route-rpc-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "tenant-route-user-oid", Email: "tenant-route-user@example.com", DisplayName: "Tenant Route User",
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

	createBody := map[string]any{
		"orgId": "", "tenantId": "acme", "kind": "otlp",
		"gatewayMode": "managed", "gatewayName": "shepherd-receiver-gw",
	}

	It("creates, lists, rotates, and revokes a tenant route over the Connect handler (happy path)", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()

		createResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		created := decodeBody(createResp)
		Expect(created["tenantId"]).To(Equal("acme"))
		Expect(created["kind"]).To(Equal("otlp"))
		Expect(created["status"]).To(Equal("active"))
		Expect(created["gatewayMode"]).To(Equal("managed"))
		segment, ok := created["segment"].(string)
		Expect(ok).To(BeTrue(), "expected a segment string")
		Expect(segment).To(HavePrefix("acme-"), "default format is slug-suffix (D9)")
		routeID, ok := created["id"].(string)
		Expect(ok).To(BeTrue(), "expected an id string")

		reader := createSession(false, []string{"tenant-route-reader-group"})
		listResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, reader)
		Expect(listResp.StatusCode).To(Equal(http.StatusOK))
		listed := decodeBody(listResp)
		items, ok := listed["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items).To(HaveLen(1))

		rotateResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/RotateTenantRoute",
			map[string]any{"orgId": orgID.String(), "id": routeID}, admin)
		Expect(rotateResp.StatusCode).To(Equal(http.StatusOK))
		rotated := decodeBody(rotateResp)
		active, ok := rotated["active"].(map[string]any)
		Expect(ok).To(BeTrue())
		deprecated, ok := rotated["deprecated"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(active["status"]).To(Equal("active"))
		Expect(active["segment"]).NotTo(Equal(segment), "rotation must mint a NEW segment")
		Expect(active["rotatedFromId"]).To(Equal(routeID))
		Expect(deprecated["id"]).To(Equal(routeID))
		Expect(deprecated["status"]).To(Equal("deprecated"))
		Expect(deprecated["validUntil"]).NotTo(BeEmpty(), "a deprecated route must carry its rotation-overlap deadline")

		listResp2 := postConnect("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, reader)
		listed2 := decodeBody(listResp2)
		items2, ok := listed2["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items2).To(HaveLen(2), "rotation must leave BOTH the deprecated and the new active route visible, not replace in place")

		activeID, ok := active["id"].(string)
		Expect(ok).To(BeTrue(), "expected an id string")
		revokeResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/RevokeTenantRoute",
			map[string]any{"orgId": orgID.String(), "id": activeID}, admin)
		Expect(revokeResp.StatusCode).To(Equal(http.StatusOK))
		revoked := decodeBody(revokeResp)
		Expect(revoked["status"]).To(Equal("revoked"))
		Expect(revoked["revokedAt"]).NotTo(BeEmpty())
	})

	It("denies ListTenantRoutes for an authenticated session with no access to the org", func() {
		outsider := createSession(false, []string{"some-other-group"})
		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, outsider)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("permission_denied"))
	})

	It("maps a second CreateTenantRoute for the same tenant+kind to the Connect already_exists code (409), enforcing D9's one-active-route-per-tenant model", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()

		first := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(first.StatusCode).To(Equal(http.StatusOK))
		io.ReadAll(first.Body) //nolint:errcheck // drain before reuse of the same connection

		second := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(second.StatusCode).To(Equal(http.StatusConflict))
		payload := decodeBody(second)
		Expect(payload["code"]).To(Equal("already_exists"))
	})

	It("uses the opaque format and discloses no tenant name fragment when requested", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()
		body["format"] = "opaque"

		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		created := decodeBody(resp)
		segment, ok := created["segment"].(string)
		Expect(ok).To(BeTrue(), "expected a segment string")
		Expect(segment).NotTo(ContainSubstring("acme"), "opaque format must not disclose the tenant name")
	})

	It("rejects rotating a route owned by a different org (404, not the org-mismatch confirmed)", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()
		createResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		created := decodeBody(createResp)
		routeID, ok := created["id"].(string)
		Expect(ok).To(BeTrue(), "expected an id string")

		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "other-org-for-tenant-route", DisplayName: "Other Org", AdminGroupID: "other-admin-group",
		})
		Expect(err).NotTo(HaveOccurred())
		otherAdmin := createSession(false, []string{"other-admin-group"})

		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/RotateTenantRoute",
			map[string]any{"orgId": other.ID.String(), "id": routeID}, otherAdmin)
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
	})
})
