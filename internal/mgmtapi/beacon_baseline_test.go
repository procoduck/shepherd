package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/beacon"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// D6's baseline pipeline is appended on TWO serve paths: this one, the eager
// recompute mgmtapi runs after a pipeline write, and the lazy one
// internal/agentapi.Service.recomputeServeCache takes when serve_cache is
// dirty. Both call the same beacon.AppendBaseline, which makes them agree by
// construction — but sharing a function is not the same as proving both
// callers still call it.
//
// docs/gateway-tier-plan.md §10 records exactly this trap from W1: "two paths
// produce the same served config; enforcing one of them is not enforcement",
// and W1's gate only closed once a test drove the real serving path rather
// than the shared helper. A review of this slice found that bar unmet here —
// deleting the AppendBaseline call from recomputeOrgCaches still compiled and
// still passed the whole mgmtapi suite. This spec is what makes that
// regression fail.
//
// Red run, executed: removing the beacon.AppendBaseline call from
// recomputeOrgCaches (serving result.Content directly) fails this spec with
// `serve_cache for the collector never contained the beacon baseline`.
var _ = Describe("D6 baseline on the eager serve path", Label("integration"), func() {
	const baseURL = "https://shepherd.example.test"

	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		orgID       string
		adminCookie *http.Cookie
		collectorID pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "beacon-org", DisplayName: "Beacon Org", AdminGroupID: "admin-group",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		// A claimed cluster with one collector: recomputeOrgCaches only writes
		// serve_cache for collectors whose cluster belongs to the org.
		cluster, err := st.Queries.UpsertCluster(ctx, "beacon-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: o.ID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{
			ClusterID: cluster.ID, Role: "metrics",
		})
		Expect(err).NotTo(HaveOccurred())
		collectorID = collector.ID

		// BaseURL is what production wiring threads into WithBeaconRemoteWrite
		// (internal/mgmtapi/router.go). Without it the baseline is a documented
		// no-op, so a spec that left it empty would pass no matter what.
		cfg := &config.Config{
			Auth:     config.AuthConfig{InsecureCookies: true},
			Server:   config.ServerConfig{BaseURL: baseURL},
			Validate: config.ValidateConfig{AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRESTRouter(st, authHandler, cfg, nil))
		adminCookie = newAppAdminSession(ctx, st)
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
		if st != nil {
			st.Close()
		}
		cancel()
	})

	It("appends the baseline to served config after a pipeline write", func() {
		// Creating a pipeline is what triggers the eager recompute — the real
		// entry point, not recomputeOrgCaches called directly, so this spec
		// also covers the wiring that reaches it.
		resp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), map[string]any{
			"name":     "beacon-probe",
			"contents": "// a pipeline whose content is irrelevant; the baseline is appended regardless",
			"matchers": []string{},
		}, adminCookie)
		Expect(resp.StatusCode).To(BeNumerically("<", 300), "creating the pipeline should succeed")
		var created map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&created)).To(Succeed())
		Expect(resp.Body.Close()).To(Succeed())
		pipelineID, _ := created["id"].(string) //nolint:errcheck // asserted non-empty below
		Expect(pipelineID).NotTo(BeEmpty())

		// A pipeline is created disabled, and recomputeOrgCaches merges only
		// ENABLED pipelines — so enabling is what produces a served config to
		// append the baseline to. Enabling triggers its own recompute.
		enableResp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines/%s/enable", orgID, pipelineID), nil, adminCookie)
		defer enableResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(enableResp.StatusCode).To(Equal(http.StatusOK))

		// The recompute is deliberately detached (a goroutine, so the request
		// returns promptly), hence Eventually rather than a bare read. Wait
		// for the ROW first and assert on its content second: "no row" and
		// "row without the baseline" are different failures — the first means
		// the recompute never got as far as writing, and reporting them as
		// one message would send a reader hunting in the wrong place.
		Eventually(func() error {
			var got string
			return st.Pool().QueryRow(ctx,
				`SELECT content FROM serve_cache WHERE collector_id = $1`, collectorID).Scan(&got)
		}, "20s", "250ms").Should(Succeed(),
			"recomputeOrgCaches never wrote a serve_cache row for this collector at all — "+
				"the baseline assertion below could not even be reached")

		var content string
		Eventually(func() string {
			var got string
			if err := st.Pool().QueryRow(ctx,
				`SELECT content FROM serve_cache WHERE collector_id = $1`, collectorID).Scan(&got); err != nil {
				return ""
			}
			content = got
			return got
		}, "20s", "250ms").Should(ContainSubstring("shepherd-beacon"),
			"serve_cache for the collector never contained the beacon baseline — D6 says the "+
				"baseline is not opt-in, and this is the eager path that must append it")

		// Assert the served baseline actually points at this deployment's
		// ingest endpoint, not merely that some beacon-shaped text is present.
		Expect(content).To(ContainSubstring(baseURL+beacon.WritePath),
			"the baseline is present but its remote_write does not point at %s%s — a baseline "+
				"pointing somewhere else reports to nobody", baseURL, beacon.WritePath)
		// Count block DECLARATIONS (`prometheus.remote_write "label" {`), not
		// bare occurrences of the component name — the relabel stage also
		// references it as `forward_to = [prometheus.remote_write.<l>.receiver]`,
		// so a substring count would be 2 for a correctly rendered baseline.
		Expect(strings.Count(content, `prometheus.remote_write "`)).To(Equal(1),
			"expected exactly one baseline remote_write block in the served config, got:\n%s", content)
	})
})
