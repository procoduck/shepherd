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

// Connect-path coverage for DestinationService, over the same
// newRPCWiringRouter(...) wiring rpc_wiring_test.go and rpc_fleet_test.go
// use — a happy path, an authz denial, and an error-code mapping case, per
// docs/api-contract-design.md's testing rules. mgmtapi_test.go's
// "DeleteDestination" Describe block covers the REST shim path (the
// compatibility oracle, including the legacy "in_use" error code) and is
// unchanged by this migration.
var _ = Describe("shepherd.mgmt.v1.DestinationService RPC", Label("integration"), func() {
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
			Name:          "destination-rpc-org",
			DisplayName:   "Destination RPC Org",
			AdminGroupID:  "destination-admin-group",
			ReaderGroupID: pgtype.Text{String: "destination-reader-group", Valid: true},
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
		sessionID := fmt.Sprintf("destination-rpc-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "destination-user-oid", Email: "destination-user@example.com", DisplayName: "Destination User",
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

	It("creates and lists a destination over the Connect handler (happy path)", func() {
		admin := createSession(false, []string{"destination-admin-group"})
		createResp := postConnect("/shepherd.mgmt.v1.DestinationService/CreateDestination", map[string]any{
			"orgId": orgID.String(), "name": "prom-prod", "type": "prometheus", "url": "http://prometheus:9090",
			"authMode": "none",
		}, admin)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		created := decodeBody(createResp)
		Expect(created["name"]).To(Equal("prom-prod"))
		Expect(created["type"]).To(Equal("prometheus"))

		reader := createSession(false, []string{"destination-reader-group"})
		listResp := postConnect("/shepherd.mgmt.v1.DestinationService/ListDestinations", map[string]any{"orgId": orgID.String()}, reader)
		Expect(listResp.StatusCode).To(Equal(http.StatusOK))
		listed := decodeBody(listResp)
		items, ok := listed["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items).To(HaveLen(1))
		Expect(listed["total"]).To(Equal(1.0))
	})

	It("denies ListDestinations for an authenticated session with no access to the org", func() {
		outsider := createSession(false, []string{"some-other-group"})
		resp := postConnect("/shepherd.mgmt.v1.DestinationService/ListDestinations", map[string]any{"orgId": orgID.String()}, outsider)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("permission_denied"))
	})

	It("maps a destination still referenced by a wizard pipeline to the Connect already_exists code (409)", func() {
		destination, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: orgID, Name: "referenced-destination", Type: "prometheus", Url: "http://prometheus",
			SecretName: "secret", SecretNamespace: "default", AuthMode: "none", Extra: json.RawMessage("{}"),
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Pool().Exec(ctx, `INSERT INTO pipelines (org_id, name, contents, source, wizard_state) VALUES ($1, $2, '', 'wizard', $3)`,
			orgID, "destination-pipeline", json.RawMessage(fmt.Sprintf(`{"destination_id":%q}`, destination.ID.String())))
		Expect(err).NotTo(HaveOccurred())

		admin := createSession(false, []string{"destination-admin-group"})
		resp := postConnect("/shepherd.mgmt.v1.DestinationService/DeleteDestination", map[string]any{
			"orgId": orgID.String(), "id": destination.ID.String(),
		}, admin)
		Expect(resp.StatusCode).To(Equal(http.StatusConflict))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("already_exists"))
		Expect(payload["message"]).To(ContainSubstring("destination-pipeline"))
	})
})
