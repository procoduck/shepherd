package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// This file mirrors rpc_simulate_run_test.go's RBAC and tenant-isolation
// cases through the REST shim (POST/GET /api/orgs/{org}/simulate/runs...)
// instead of the Connect procedure — the concrete regression test for the
// "REST shims bypass auth" failure mode: router.go adds these two routes
// inside the SAME auth.RequireOrgAccess(st, "org", "orgadmin") group that
// already wraps /simulate/relabel and /simulate/logs, so a route added
// outside that group (or a copy-pasted new ungated group) fails these tests.
var _ = Describe("REST /orgs/{org}/simulate/runs — Run API auth parity", Label("integration"), func() {
	var (
		ctx          context.Context
		cancel       context.CancelFunc
		st           *store.Store
		server       *httptest.Server
		orgID        string
		adminCookie  *http.Cookie
		readerCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "simulate-run-rest-org", DisplayName: "Simulate Run REST Org",
			AdminGroupID: "run-rest-admin-grp", ReaderGroupID: pgtype.Text{String: "run-rest-reader-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "run-rest-admin-grp")}
		readerCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "run-rest-reader-grp")}

		cfg := &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Simulator: config.SimulatorConfig{
				Enabled: true, MaxNonTerminalPerOrg: 3,
				Duration: 30 * time.Second, MaxDuration: 120 * time.Second,
			},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newVisualRESTRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("requires org-admin for POST/GET /simulate/runs, denying an org-reader session", func() {
		resp := postJSON(server, fmt.Sprintf("/orgs/%s/simulate/runs", orgID), minimalRunGraph(orgID), readerCookie)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

		createResp := postJSON(server, fmt.Sprintf("/orgs/%s/simulate/runs", orgID), minimalRunGraph(orgID), adminCookie)
		defer createResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var created map[string]any
		Expect(json.NewDecoder(createResp.Body).Decode(&created)).To(Succeed())
		runID, _ := created["run_id"].(string) //nolint:errcheck // map value may be absent in test; empty string is safe
		Expect(runID).NotTo(BeEmpty())

		getResp := getRequest(server, fmt.Sprintf("/orgs/%s/simulate/runs/%s", orgID, runID), readerCookie)
		defer getResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(getResp.StatusCode).To(Equal(http.StatusForbidden))
	})

	It("refuses to read another org's run through the REST shim (tenant isolation)", func() {
		otherOrg, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "simulate-run-rest-org-b", DisplayName: "Simulate Run REST Org B",
			AdminGroupID: "run-rest-admin-grp-b", ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		otherAdminCookie := &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "run-rest-admin-grp-b")}

		graphJSON, err := json.Marshal(map[string]any{
			"kind": "alloy-graph/v1", "schema_version": "alloy-v1.18.1",
			"nodes": []map[string]any{{"id": "n1", "component": "prometheus.scrape", "label": "s"}},
		})
		Expect(err).NotTo(HaveOccurred())
		runA, err := st.Queries.CreateSimulateRun(ctx, sqlc.CreateSimulateRunParams{
			OrgID: orgUUID(orgID), Graph: graphJSON, RequestedDurationSeconds: 30, CreatedBy: "org-a-admin",
		})
		Expect(err).NotTo(HaveOccurred())

		resp := getRequest(server, fmt.Sprintf("/orgs/%s/simulate/runs/%s", otherOrg.ID.String(), runA.ID.String()), otherAdminCookie)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))

		// The row is untouched and still readable through its own org.
		reReadResp := getRequest(server, fmt.Sprintf("/orgs/%s/simulate/runs/%s", orgID, runA.ID.String()), adminCookie)
		defer reReadResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(reReadResp.StatusCode).To(Equal(http.StatusOK))
		var run map[string]any
		Expect(json.NewDecoder(reReadResp.Body).Decode(&run)).To(Succeed())
		Expect(run["org_id"]).To(Equal(orgID))
		Expect(run["status"]).To(Equal("queued"))
	})
})
