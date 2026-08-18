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

// Connect-path coverage for FleetService, over the same
// newRPCWiringRouter(...) wiring rpc_wiring_test.go proves 404-guards and
// unimplemented-stub behavior with — this suite exercises the now-migrated
// FleetService methods themselves: a happy path, an authz denial, and an
// error-code mapping case, per docs/api-contract-design.md's testing rules.
// collectors_metadata_test.go and mgmtapi_test.go cover the REST shim paths
// (the compatibility oracle) and are unchanged by this migration.
var _ = Describe("shepherd.mgmt.v1.FleetService RPC", Label("integration"), func() {
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
			Name:          "fleet-rpc-org",
			DisplayName:   "Fleet RPC Org",
			AdminGroupID:  "fleet-admin-group",
			ReaderGroupID: pgtype.Text{String: "fleet-reader-group", Valid: true},
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
		sessionID := fmt.Sprintf("fleet-rpc-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "fleet-user-oid", Email: "fleet-user@example.com", DisplayName: "Fleet User",
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

	It("lists collectors for an org-scoped reader (happy path)", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "fleet-rpc-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		cookie := createSession(false, []string{"fleet-reader-group"})
		resp := postConnect("/shepherd.mgmt.v1.FleetService/ListCollectors", map[string]any{"orgId": orgID.String()}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		payload := decodeBody(resp)
		items, ok := payload["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items).To(HaveLen(1))
		item, ok := items[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(item["id"]).To(Equal(collector.ID.String()))
		Expect(item["cluster"]).To(Equal("fleet-rpc-cluster"))
	})

	It("denies ListCollectors for an authenticated session with no access to the org", func() {
		cookie := createSession(false, []string{"some-other-group"})
		resp := postConnect("/shepherd.mgmt.v1.FleetService/ListCollectors", map[string]any{"orgId": orgID.String()}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("permission_denied"))
	})

	It("maps a not-found collector to the Connect not_found code (404)", func() {
		cookie := createSession(true, nil)
		missingID := "00000000-0000-0000-0000-000000000000"
		resp := postConnect("/shepherd.mgmt.v1.FleetService/GetCollector", map[string]any{
			"orgId": orgID.String(),
			"id":    missingID,
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("not_found"))
	})
})
