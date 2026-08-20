package mgmtapi_test

import (
	"context"
	"encoding/json"
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

// minimalRunGraph is a structurally valid CreateRun request body: kind ==
// "alloy-graph/v1" and at least one node, which is all CreateRun's
// synchronous pre-check requires (deeper validation happens in
// internal/simulate.RunWorker, which is never started in these tests — a
// created run simply stays "queued", which is all GetRun needs to exercise).
func minimalRunGraph(orgID string) map[string]any {
	return map[string]any{
		"org_id": orgID,
		"graph": map[string]any{
			"kind":           "alloy-graph/v1",
			"schema_version": "alloy-v1.18.1",
			"nodes": []map[string]any{
				{"id": "n1", "component": "prometheus.scrape", "label": "scrape1"},
			},
		},
		"duration_seconds": 15,
	}
}

var _ = Describe("shepherd.mgmt.v1.SimulateService — Run API (S3 sandbox runs)", Label("integration"), func() {
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
			Name: "simulate-run-org", DisplayName: "Simulate Run Org",
			AdminGroupID: "run-admin-grp", ReaderGroupID: pgtype.Text{String: "run-reader-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "run-admin-grp")}
		readerCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "run-reader-grp")}

		cfg := &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Simulator: config.SimulatorConfig{
				Enabled: true, MaxNonTerminalPerOrg: 2,
				Duration: 30 * time.Second, MaxDuration: 120 * time.Second,
			},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// createRun/getRun return the raw, unread, unclosed response — the
	// caller decides whether to decode a success body or read a Connect
	// error via connectErrorCode, and owns closing it exactly once.
	createRun := func(cookie *http.Cookie, body map[string]any) *http.Response {
		return postConnectJSON(server, "/shepherd.mgmt.v1.SimulateService/CreateRun", cookie, body)
	}
	getRun := func(cookie *http.Cookie, id string) *http.Response {
		return postConnectJSON(server, "/shepherd.mgmt.v1.SimulateService/GetRun", cookie, map[string]any{"org_id": orgID, "id": id})
	}
	decodeBody := func(resp *http.Response) map[string]any {
		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		return result
	}

	It("creates a run and GetRun shows it queued", func() {
		resp := createRun(adminCookie, minimalRunGraph(orgID))
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		created := decodeBody(resp)
		runID, _ := created["runId"].(string) //nolint:errcheck // map value may be absent in test; empty string is safe
		Expect(runID).NotTo(BeEmpty())

		getResp := getRun(adminCookie, runID)
		defer getResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(getResp.StatusCode).To(Equal(http.StatusOK))
		run := decodeBody(getResp)
		Expect(run["status"]).To(Equal("queued"))
		Expect(run["orgId"]).To(Equal(orgID))
		Expect(run["fidelityNote"]).NotTo(BeEmpty(), "fidelity_note must always be populated")
	})

	It("denies CreateRun and GetRun for a session without org-admin access", func() {
		resp := createRun(readerCookie, minimalRunGraph(orgID))
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(connectErrorCode(resp)).To(Equal("permission_denied"))

		// A run must exist to prove GetRun itself denies, not just 404s.
		adminResp := createRun(adminCookie, minimalRunGraph(orgID))
		defer adminResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(adminResp.StatusCode).To(Equal(http.StatusOK))
		runID, _ := decodeBody(adminResp)["runId"].(string) //nolint:errcheck // map value may be absent in test; empty string is safe

		getResp := getRun(readerCookie, runID)
		defer getResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(getResp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(connectErrorCode(getResp)).To(Equal("permission_denied"))
	})

	It("refuses to read another org's run through a caller-supplied org id (tenant isolation)", func() {
		otherOrg, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "simulate-run-org-b", DisplayName: "Simulate Run Org B",
			AdminGroupID: "run-admin-grp-b", ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		otherAdminCookie := &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "run-admin-grp-b")}

		graphJSON, err := json.Marshal(map[string]any{
			"kind": "alloy-graph/v1", "schema_version": "alloy-v1.18.1",
			"nodes": []map[string]any{{"id": "n1", "component": "prometheus.scrape", "label": "s"}},
		})
		Expect(err).NotTo(HaveOccurred())
		runA, err := st.Queries.CreateSimulateRun(ctx, sqlc.CreateSimulateRunParams{
			OrgID: orgUUID(orgID), Graph: graphJSON, RequestedDurationSeconds: 30, CreatedBy: "org-a-admin",
		})
		Expect(err).NotTo(HaveOccurred())

		// org B's admin, querying with THEIR OWN org id, must not be able to
		// read org A's run — and the response must be 404, never 403 (which
		// would itself confirm the run id belongs to some other org).
		req := map[string]any{"org_id": otherOrg.ID.String(), "id": runA.ID.String()}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.SimulateService/GetRun", otherAdminCookie, req)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		Expect(connectErrorCode(resp)).To(Equal("not_found"))

		// The row itself must be untouched and still readable by its own org.
		reReadResp := getRun(adminCookie, runA.ID.String())
		defer reReadResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(reReadResp.StatusCode).To(Equal(http.StatusOK))
		reRead := decodeBody(reReadResp)
		Expect(reRead["orgId"]).To(Equal(orgID))
		Expect(reRead["status"]).To(Equal("queued"))
	})

	It("404s GetRun for a run id that never existed", func() {
		resp := getRun(adminCookie, "00000000-0000-0000-0000-000000000000")
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		Expect(connectErrorCode(resp)).To(Equal("not_found"))
	})

	It("400s GetRun for a malformed run id or org id", func() {
		resp := getRun(adminCookie, "not-a-uuid")
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("invalid_argument"))

		badOrgResp := postConnectJSON(server, "/shepherd.mgmt.v1.SimulateService/GetRun", adminCookie,
			map[string]any{"org_id": "not-a-uuid", "id": "00000000-0000-0000-0000-000000000000"})
		defer badOrgResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(badOrgResp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(badOrgResp)).To(Equal("invalid_argument"))
	})

	It("writes exactly one simulate.run.create audit row per CreateRun call", func() {
		resp := createRun(adminCookie, minimalRunGraph(orgID))
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		runID, _ := decodeBody(resp)["runId"].(string) //nolint:errcheck // map value may be absent in test; empty string is safe

		var count int
		Expect(st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE action = 'simulate.run.create' AND resource_id = $1`, runID,
		).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))

		var actor, actorType string
		var detail []byte
		Expect(st.Pool().QueryRow(ctx,
			`SELECT actor, actor_type, detail FROM audit_log WHERE action = 'simulate.run.create' AND resource_id = $1`, runID,
		).Scan(&actor, &actorType, &detail)).To(Succeed())
		Expect(actorType).To(Equal("user"))
		var detailMap map[string]any
		Expect(json.Unmarshal(detail, &detailMap)).To(Succeed())
		Expect(detailMap["node_count"]).To(BeNumerically("==", 1))
		Expect(detailMap["duration_seconds"]).To(BeNumerically("==", 15))
	})

	It("rejects CreateRun with failed_precondition once the org has too many non-terminal runs", func() {
		graphJSON, err := json.Marshal(map[string]any{
			"kind": "alloy-graph/v1", "schema_version": "alloy-v1.18.1",
			"nodes": []map[string]any{{"id": "n1", "component": "prometheus.scrape", "label": "s"}},
		})
		Expect(err).NotTo(HaveOccurred())
		// cfg.Simulator.MaxNonTerminalPerOrg == 2 in this suite's BeforeEach.
		for i := 0; i < 2; i++ {
			_, err := st.Queries.CreateSimulateRun(ctx, sqlc.CreateSimulateRunParams{
				OrgID: orgUUID(orgID), Graph: graphJSON, RequestedDurationSeconds: 30, CreatedBy: "seed",
			})
			Expect(err).NotTo(HaveOccurred())
		}

		// The Connect protocol's own HTTP status mapping for FailedPrecondition
		// is 400 (distinct from the REST shim's custom 422 mapping — see
		// ConnectCodeStatus/WriteConnectError, exercised through this same cap
		// via the REST shim in simulate_run_rest_test.go instead). The Connect
		// error code itself is the load-bearing assertion here.
		resp := createRun(adminCookie, minimalRunGraph(orgID))
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("failed_precondition"))
	})

	It("expires a run whose TTL has elapsed and the janitor's expire step runs", func() {
		graphJSON, err := json.Marshal(map[string]any{"kind": "alloy-graph/v1", "nodes": []map[string]any{{"id": "n1", "component": "prometheus.scrape", "label": "s"}}})
		Expect(err).NotTo(HaveOccurred())
		run, err := st.Queries.CreateSimulateRun(ctx, sqlc.CreateSimulateRunParams{
			OrgID: orgUUID(orgID), Graph: graphJSON, RequestedDurationSeconds: 30, CreatedBy: "seed",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Pool().Exec(ctx, `UPDATE simulate_runs SET created_at = now() - interval '10 minutes' WHERE id = $1`, run.ID)
		Expect(err).NotTo(HaveOccurred())

		expired, err := st.Queries.ExpireStaleSimulateRuns(ctx, 300) // 5 minute TTL, matching config default
		Expect(err).NotTo(HaveOccurred())
		Expect(expired).To(HaveLen(1))

		resp := getRun(adminCookie, run.ID.String())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(decodeBody(resp)["status"]).To(Equal("expired"))
	})

	It("purges a run whose retention window has elapsed", func() {
		graphJSON, err := json.Marshal(map[string]any{"kind": "alloy-graph/v1", "nodes": []map[string]any{{"id": "n1", "component": "prometheus.scrape", "label": "s"}}})
		Expect(err).NotTo(HaveOccurred())
		run, err := st.Queries.CreateSimulateRun(ctx, sqlc.CreateSimulateRunParams{
			OrgID: orgUUID(orgID), Graph: graphJSON, RequestedDurationSeconds: 30, CreatedBy: "seed",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Pool().Exec(ctx,
			`UPDATE simulate_runs SET status = 'completed', finished_at = now() - interval '2 hours' WHERE id = $1`, run.ID)
		Expect(err).NotTo(HaveOccurred())

		Expect(st.Queries.DeleteOldSimulateRuns(ctx, 3600)).To(Succeed()) // 1 hour retention, matching config default

		resp := getRun(adminCookie, run.ID.String())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
	})
})
