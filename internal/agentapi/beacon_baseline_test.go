package agentapi_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"connectrpc.com/connect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/beacon"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// The lazy half of the same obligation internal/mgmtapi's
// beacon_baseline_test.go covers for the eager half. D6's baseline is served
// on two paths, and docs/gateway-tier-plan.md §10 records from W1 that
// enforcing one of two paths that produce the same served config is not
// enforcement. Both call beacon.AppendBaseline — but a shared function proves
// agreement, not that both callers still call it, and neither caller was
// covered by a test that drives the real path until these two specs.
//
// This drives GetConfig inside the dirty window, exactly as G6's spec in
// service_test.go does, because that is the path a live agent takes when a
// pipeline write has marked serve_cache dirty.
//
// Red run, executed: removing the beacon.AppendBaseline call from
// recomputeServeCache (serving result.Content directly) fails this spec with
// `the served config contains no beacon baseline`.
var _ = Describe("D6 baseline on the lazy serve path", Label("integration"), func() {
	const baseURL = "https://shepherd.example.test"

	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		client collectorv1connect.CollectorServiceClient
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "beacon-token", TokenHash: hash[:], CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))

		// WithBeaconRemoteWrite is what production wiring applies
		// (internal/server/server.go). Without it the baseline is a documented
		// no-op, so a spec omitting it would pass regardless.
		svc := agentapi.New(st, nil, slog.Default(), testSchemaRegistry(),
			agentapi.WithBeaconRemoteWrite(baseURL))
		path, handler := collectorv1connect.NewCollectorServiceHandler(
			svc, connect.WithInterceptors(agentapi.NewAuthInterceptor(st)),
		)
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server = httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
		server.Start()

		client = collectorv1connect.NewCollectorServiceClient(
			server.Client(), server.URL, connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", authHeader)
					return next(c, req)
				}
			})),
		)
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("serves the baseline through GetConfig's dirty-window recompute", func() {
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "beacon-lazy-org", DisplayName: "Beacon lazy org", AdminGroupID: "admins",
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
			Id: "beacon-instance", Name: "beacon-instance",
			LocalAttributes: map[string]string{"cluster": "beacon-cluster", "role": "metrics"},
		}))
		Expect(err).NotTo(HaveOccurred())
		cluster, err := st.Queries.GetClusterByName(ctx, "beacon-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())

		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org.ID, Name: "metrics-pipeline",
			Contents: `prometheus.scrape "app" {
  targets    = []
  forward_to = []
}`,
			Matchers:    json.RawMessage(`["cluster=\"beacon-cluster\""]`),
			Enabled:     true,
			Source:      "ui",
			WizardState: json.RawMessage(`{}`),
			CreatedBy:   "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		collector, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{
			Name: "beacon-cluster", Role: "metrics",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.MarkServeCacheDirty(ctx, collector.ID)).To(Succeed())

		resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "beacon-instance", LocalAttributes: map[string]string{"cluster": "beacon-cluster", "role": "metrics"},
		}))
		Expect(err).NotTo(HaveOccurred())

		Expect(resp.Msg.Content).To(ContainSubstring("shepherd-beacon"),
			"the served config contains no beacon baseline — D6 says the baseline is not opt-in, "+
				"and this is the lazy path a live agent drives when serve_cache is dirty")
		Expect(resp.Msg.Content).To(ContainSubstring(baseURL+beacon.WritePath),
			"the baseline is present but its remote_write does not point at %s%s", baseURL, beacon.WritePath)

		// The user's own pipeline must still be served: the baseline is an
		// addition, never a replacement.
		Expect(resp.Msg.Content).To(ContainSubstring("prometheus.scrape"),
			"appending the baseline must not displace the collector's actual pipeline")
		Expect(strings.Count(resp.Msg.Content, `prometheus.remote_write "`)).To(Equal(1),
			"expected exactly one baseline remote_write block in the served config")
	})
})
