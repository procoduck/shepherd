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
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/config"
	"shepherd/internal/metrics"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
	"shepherd/internal/version"
)

// sharedPG is a single container shared across all specs in this suite.
var sharedPG *testutil.SharedPostgres

// queryCounter is a minimal pgx.QueryTracer that counts Query/QueryRow/Exec
// calls issued over one pool — used by the PR-9 performance-regression test
// to prove the local_attributes match-drift hook's byte-compare short-circuit
// actually skips work, not just that its outcome happens to be a no-op.
// Mirrors internal/store/local_attributes_scale_test.go's own copy; not
// shared across packages since it's an unexported test helper in both.
type queryCounter struct {
	mu    sync.Mutex
	count int
}

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *queryCounter) Reset() {
	c.mu.Lock()
	c.count = 0
	c.mu.Unlock()
}

func (c *queryCounter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func TestAgentAPI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AgentAPI Integration Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	sharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	// Run migrations once on the root DB template.
	Expect(store.MigrateUp(context.Background(), sharedPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if sharedPG != nil {
		Expect(sharedPG.Terminate(context.Background())).To(Succeed())
	}
})

var _ = Describe("CollectorService", Label("integration"), func() {
	var (
		ctx        context.Context
		cancel     context.CancelFunc
		st         *store.Store
		server     *httptest.Server
		client     collectorv1connect.CollectorServiceClient
		authHeader string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		// Each spec gets its own isolated database pre-seeded with the migrated schema.
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		// Create an agent token for auth.
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name:      "test-token",
			TokenHash: hash[:],
			CreatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		var tokenID string
		tokenID = tok.ID.String()

		creds := base64.StdEncoding.EncodeToString([]byte(tokenID + ":" + secret))
		authHeader = "Basic " + creds

		logger := slog.Default()
		svc := agentapi.New(st, nil, logger, testSchemaRegistry())
		authGate := agentapi.NewAuthGate(st, nil)
		path, handler := collectorv1connect.NewCollectorServiceHandler(
			svc,
			connect.WithRequestGate(authGate),
		)
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server = httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
		server.Start()

		// Capture authHeader for closure.
		hdr := authHeader
		client = collectorv1connect.NewCollectorServiceClient(
			server.Client(),
			server.URL,
			connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", hdr)
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

	Describe("RegisterCollector", func() {
		It("registers a collector and upserts cluster/collector/instance rows", func() {
			resp, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:   "instance-1",
				Name: "test-instance",
				LocalAttributes: map[string]string{
					"cluster": "test-cluster",
					"role":    "metrics",
				},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp).NotTo(BeNil())

			cluster, err := st.Queries.GetClusterByName(ctx, "test-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(cluster.Name).To(Equal("test-cluster"))
			Expect(cluster.OrgID.Valid).To(BeFalse(), "newly registered cluster should be unclaimed")

			instance, err := st.Queries.GetCollectorInstanceByID(ctx, "instance-1")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.Name).To(Equal("test-instance"))
		})

		It("falls back to the wire id when the collector sends no name", func() {
			_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:   "noname-instance",
				Name: "",
				LocalAttributes: map[string]string{
					"cluster": "noname-cluster",
					"role":    "metrics",
				},
			}))
			Expect(err).NotTo(HaveOccurred())

			instance, err := st.Queries.GetCollectorInstanceByID(ctx, "noname-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.Name).To(Equal("noname-instance"), "RegisterCollector must fall back to the wire id like GetConfig does, never store an empty name")

			// A subsequent poll with no collector.name attribute must not
			// wipe the fallback name back to empty either.
			_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id:              "noname-instance",
				LocalAttributes: map[string]string{"cluster": "noname-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())

			instance, err = st.Queries.GetCollectorInstanceByID(ctx, "noname-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.Name).To(Equal("noname-instance"))
		})

		It("rejects missing cluster attribute", func() {
			_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:              "instance-bad",
				Name:            "bad",
				LocalAttributes: map[string]string{"role": "metrics"},
			}))
			Expect(err).To(HaveOccurred())
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
		})

		It("rejects invalid role", func() {
			_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:              "instance-bad2",
				Name:            "bad",
				LocalAttributes: map[string]string{"cluster": "x", "role": "invalid"},
			}))
			Expect(err).To(HaveOccurred())
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
		})
	})

	Describe("GetConfig", func() {
		counterValue := func(counter interface{ Write(*dto.Metric) error }) float64 {
			metric := &dto.Metric{}
			Expect(counter.Write(metric)).To(Succeed())
			return metric.Counter.GetValue()
		}

		setupClaimedPipeline := func() (sqlc.Collector, sqlc.Pipeline) {
			org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "recompute-org", DisplayName: "Recompute org", AdminGroupID: "admins"})
			Expect(err).NotTo(HaveOccurred())
			_, err = client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "recompute-instance", Name: "recompute-instance",
				LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st.Queries.GetClusterByName(ctx, "recompute-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
			pipeline, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org.ID, Name: "recompute-pipeline", Contents: "// recompute-pipeline",
				Matchers: json.RawMessage(`["cluster=\"recompute-cluster\""]`), Enabled: true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			collector, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{Name: "recompute-cluster", Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())
			return collector, pipeline
		}

		// PR-8b freshness requirement: the hot path must use the CURRENT
		// request's local_attributes, never a re-query of collector_instances
		// (which would still reflect the previous heartbeat — this same
		// request's own upsertCollectorInstance write isn't what GetConfig
		// reads back from here). A single GetConfig call reporting a new
		// matching attribute must be served in that SAME response.
		It("serves a pipeline matched on local_attributes in the same request that reports them, once allow_local_attribute_matching is on", func() {
			org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "local-attrs-org", DisplayName: "Local attrs org", AdminGroupID: "admins"})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
				ID: org.ID, DisplayName: org.DisplayName, AdminGroupID: org.AdminGroupID,
				AllowLocalAttributeMatching: true,
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "local-attrs-instance", Name: "local-attrs-instance",
				LocalAttributes: map[string]string{"cluster": "local-attrs-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st.Queries.GetClusterByName(ctx, "local-attrs-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
			_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org.ID, Name: "local-attrs-pipeline", Contents: "// local-attrs-marker",
				Matchers: json.RawMessage(`["team=\"platform\""]`), Enabled: true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())

			// One poll, reporting team=platform for the first time — this
			// response, not a subsequent one, must already reflect the match.
			resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "local-attrs-instance",
				LocalAttributes: map[string]string{
					"cluster": "local-attrs-cluster", "role": "metrics", "team": "platform",
				},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.GetContent()).To(ContainSubstring("local-attrs-marker"),
				"a pipeline matched on a freshly-reported local attribute must be served in the same request that reported it")
		})

		// Flag-off byte-identical: an org that never opts in must reproduce
		// pre-PR-8 behavior exactly, even when the collector reports an
		// attribute that would otherwise match.
		It("does not serve a local_attributes-matched pipeline while allow_local_attribute_matching is off", func() {
			org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "local-attrs-off-org", DisplayName: "Local attrs off org", AdminGroupID: "admins"})
			Expect(err).NotTo(HaveOccurred())

			_, err = client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "local-attrs-off-instance", Name: "local-attrs-off-instance",
				LocalAttributes: map[string]string{"cluster": "local-attrs-off-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st.Queries.GetClusterByName(ctx, "local-attrs-off-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
			_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org.ID, Name: "local-attrs-off-pipeline", Contents: "// local-attrs-off-marker",
				Matchers: json.RawMessage(`["team=\"platform\""]`), Enabled: true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())

			resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "local-attrs-off-instance",
				LocalAttributes: map[string]string{
					"cluster": "local-attrs-off-cluster", "role": "metrics", "team": "platform",
				},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.GetContent()).NotTo(ContainSubstring("local-attrs-off-marker"),
				"flag off: a custom-local-attribute-only matcher must match nothing, same as before #139")
		})

		// PR-9: match-drift observability for local_attributes, the hot-path
		// half of §7 (LABEL-MATCHING-PLAN.md), reusing PR-5's diff engine.
		// Correctness half — a real change (attrs go from cluster/role-only to
		// also reporting the matched key) must emit exactly one "added" flip,
		// both as a metric and as an audit_log row.
		It("emits a match-drift metric and audit row when a poll's local_attributes newly match a pipeline", func() {
			org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "drift-org", DisplayName: "Drift org", AdminGroupID: "admins"})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
				ID: org.ID, DisplayName: org.DisplayName, AdminGroupID: org.AdminGroupID,
				AllowLocalAttributeMatching: true,
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "drift-instance", Name: "drift-instance",
				LocalAttributes: map[string]string{"cluster": "drift-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st.Queries.GetClusterByName(ctx, "drift-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
			_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org.ID, Name: "drift-pipeline", Contents: "// drift-marker",
				Matchers: json.RawMessage(`["team=\"platform\""]`), Enabled: true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())

			before := counterValue(metrics.PipelineMatchChangesTotal.WithLabelValues("added"))

			// First poll (via RegisterCollector above) reported no "team" key;
			// this poll adds it — the flip GetConfig's drift hook must catch.
			_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "drift-instance",
				LocalAttributes: map[string]string{
					"cluster": "drift-cluster", "role": "metrics", "team": "platform",
				},
			}))
			Expect(err).NotTo(HaveOccurred())

			Expect(counterValue(metrics.PipelineMatchChangesTotal.WithLabelValues("added"))).To(
				Equal(before+1), "one pipeline newly matched, one \"added\" flip")

			rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{
				Column1: org.ID, Column3: "pipeline.match.changed", Limit: 10,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(rows).To(HaveLen(1))
			Expect(rows[0].Actor).To(Equal("agentapi"))
			Expect(rows[0].ActorType).To(Equal("system"))
			var detail map[string]string
			Expect(json.Unmarshal(rows[0].Detail, &detail)).To(Succeed())
			// Not a raw substring match: audit_log.detail is jsonb, which
			// reformats on read-back (Postgres's own canonical spacing, not
			// byte-identical to whatever was marshaled before insertion).
			Expect(detail["direction"]).To(Equal("added"))
		})

		// Performance-regression half of PR-9: proves the byte-compare
		// short-circuit, not just its outcome. Comparing raw query counts
		// against a hardcoded baseline would be brittle (any unrelated query
		// GetConfig adds later would break it); instead this compares two
		// back-to-back polls that report IDENTICAL local_attributes, one with
		// the org's flag on and one with it off. The byte-compare runs BEFORE
		// the flag is ever read, so if it's doing its job the two polls issue
		// the exact same number of queries -- neither touches the org row,
		// the pipeline list, or the diff engine. A broken short-circuit (the
		// regression this guards against) would make the flag-on poll issue
		// strictly more queries than the flag-off one.
		It("issues the same query count for an unchanged poll whether or not allow_local_attribute_matching is on", func() {
			dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
			poolCfg, err := pgxpool.ParseConfig(dbURL)
			Expect(err).NotTo(HaveOccurred())
			counter := &queryCounter{}
			poolCfg.ConnConfig.Tracer = counter
			pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
			Expect(err).NotTo(HaveOccurred())
			defer pool.Close()
			st3 := store.NewWithPool(pool)

			raw := make([]byte, 32)
			_, _ = rand.Read(raw)
			secret := base64.URLEncoding.EncodeToString(raw)
			hash := sha256.Sum256([]byte(secret))
			tok, err := st3.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
				Name: "drift-perf-token", TokenHash: hash[:], CreatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			hdr := "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))

			svc3 := agentapi.New(st3, nil, slog.Default(), nil)
			authGate3 := agentapi.NewAuthGate(st3, nil)
			path3, handler3 := collectorv1connect.NewCollectorServiceHandler(svc3, connect.WithRequestGate(authGate3))
			mux3 := http.NewServeMux()
			mux3.Handle(path3, handler3)
			server3 := httptest.NewUnstartedServer(h2c.NewHandler(mux3, &http2.Server{}))
			server3.Start()
			defer server3.Close()

			client3 := collectorv1connect.NewCollectorServiceClient(
				server3.Client(), server3.URL, connect.WithGRPC(),
				connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
					return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
						req.Header().Set("Authorization", hdr)
						return next(c, req)
					}
				})),
			)

			org, err := st3.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "drift-perf-org", DisplayName: "Drift perf org", AdminGroupID: "admins"})
			Expect(err).NotTo(HaveOccurred())
			_, err = client3.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "drift-perf-instance", Name: "drift-perf-instance",
				LocalAttributes: map[string]string{"cluster": "drift-perf-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st3.Queries.GetClusterByName(ctx, "drift-perf-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st3.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())

			attrs := map[string]string{"cluster": "drift-perf-cluster", "role": "metrics"}

			// Establish the stored local_attributes (this poll's own flip is
			// irrelevant -- only the two polls below are measured).
			_, err = client3.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "drift-perf-instance", LocalAttributes: attrs}))
			Expect(err).NotTo(HaveOccurred())

			_, err = st3.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
				ID: org.ID, DisplayName: org.DisplayName, AdminGroupID: org.AdminGroupID,
				AllowLocalAttributeMatching: true,
			})
			Expect(err).NotTo(HaveOccurred())

			counter.Reset()
			_, err = client3.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "drift-perf-instance", LocalAttributes: attrs}))
			Expect(err).NotTo(HaveOccurred())
			flagOnUnchangedCount := counter.Value()

			_, err = st3.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
				ID: org.ID, DisplayName: org.DisplayName, AdminGroupID: org.AdminGroupID,
				AllowLocalAttributeMatching: false,
			})
			Expect(err).NotTo(HaveOccurred())

			counter.Reset()
			_, err = client3.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "drift-perf-instance", LocalAttributes: attrs}))
			Expect(err).NotTo(HaveOccurred())
			flagOffUnchangedCount := counter.Value()

			Expect(flagOnUnchangedCount).To(Equal(flagOffUnchangedCount),
				"an unchanged poll must cost the same whether or not the org has opted in -- "+
					"the byte-compare must short-circuit before the flag (or the pipeline list, or the diff) is ever reached")
		})

		It("marks a never-reported instance APPLIED once it polls with the served hash", func() {
			// Agents report a RemoteConfigStatus only when they apply a CHANGE, so a
			// collector that is healthy and polling steadily would otherwise render as
			// UNKNOWN in the UI indefinitely.
			collector, _ := setupClaimedPipeline()
			first, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(collector.ID.Valid).To(BeTrue())

			// Second poll echoes the served hash and carries no status payload.
			_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "recompute-instance", Hash: first.Msg.GetHash(),
				LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())

			inst, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(inst.RemoteConfigStatus.String).To(Equal("APPLIED"))
		})

		It("serves a git-sourced pipeline linked to the collector by its repo link", func() {
			// Regression: git pipelines are matched by the collector their repo link
			// targets, never by matchers (they are created with an empty matcher set).
			// GetConfig previously built merge.Pipeline without RepoLinkCollectorID, and
			// gitsync created the pipeline without repo_link_id, so every git-sourced
			// pipeline compared "" against the collector id and was silently never served.
			org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "git-org", DisplayName: "Git org", AdminGroupID: "admins"})
			Expect(err).NotTo(HaveOccurred())
			_, err = client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "git-instance", Name: "git-instance",
				LocalAttributes: map[string]string{"cluster": "git-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st.Queries.GetClusterByName(ctx, "git-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
			collector, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{Name: "git-cluster", Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())

			cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
				OrgID: org.ID, Name: "git-cred", Kind: "pat",
				Username:        pgtype.Text{String: "git", Valid: true},
				ClientSecretEnc: []byte("enc"),
				ProviderConfig:  json.RawMessage(`{}`),
			})
			Expect(err).NotTo(HaveOccurred())
			link, err := st.Queries.CreateRepoLink(ctx, sqlc.CreateRepoLinkParams{
				OrgID: org.ID, CollectorID: collector.ID, CredentialID: cred.ID,
				RepoUrl: "https://example.invalid/team/configs.git", Branch: "main", Path: "/",
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org.ID, Name: "git-pipeline", Contents: `// git-pipeline`,
				Matchers: json.RawMessage(`[]`), // git pipelines carry no matchers by design
				Enabled:  true, Source: "git",
				WizardState: json.RawMessage(`{}`), CreatedBy: "gitsync", UpdatedBy: "gitsync",
				RepoLinkID: link.ID,
				GitPath:    pgtype.Text{String: "/git-pipeline.alloy", Valid: true},
			})
			Expect(err).NotTo(HaveOccurred())

			resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "git-instance", LocalAttributes: map[string]string{"cluster": "git-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.GetContent()).To(ContainSubstring("git-pipeline"),
				"a git-sourced pipeline whose repo link targets this collector must be served")
		})

		It("GetConfig recomputes after enable", func() {
			collector, _ := setupClaimedPipeline()
			resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.Content).To(ContainSubstring("recompute-pipeline"))
			cache, err := st.Queries.GetServeCache(ctx, collector.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cache.Dirty).To(BeFalse())
		})

		It("matching hash → not_modified", func() {
			setupClaimedPipeline()
			first, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			before := counterValue(metrics.GetConfigTotal.WithLabelValues("not_modified"))
			second, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", Hash: first.Msg.Hash, LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Msg.NotModified).To(BeTrue())
			Expect(counterValue(metrics.GetConfigTotal.WithLabelValues("not_modified"))).To(Equal(before + 1))
		})

		It("recompute failure serves previous content", func() {
			collector, _ := setupClaimedPipeline()
			first, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(first.Msg.Content).NotTo(BeEmpty())

			// Make the next recompute actually FAIL, the way it does in
			// production: the database goes away under it. Neither a malformed
			// matcher nor unparsable contents does that any more — merge.Assemble
			// excludes the former and role enforcement (a schema registry is
			// wired here, as in production) excludes the latter fail-safe — so
			// the old version of this spec recomputed SUCCESSFULLY and only
			// passed while both computes landed in the same second (CI run
			// 34968998790 caught the flake). Renaming the pipelines table in
			// this spec's isolated database makes ListEnabledPipelinesForMerge
			// fail while every other query GetConfig runs still works.
			// (Deleting the pipeline instead would be a SUCCESSFUL recompute of
			// the empty set, which serves the header-only config — a different
			// contract.)
			_, err = st.Pool().Exec(ctx, `ALTER TABLE pipelines RENAME TO pipelines_unavailable`)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.MarkServeCacheDirty(ctx, collector.ID)).To(Succeed())

			failuresBefore := counterValue(metrics.ServeRecomputeFailuresTotal)
			second, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(counterValue(metrics.ServeRecomputeFailuresTotal)).To(Equal(failuresBefore+1),
				"the recompute must have actually failed — otherwise this spec proves nothing")
			Expect(second.Msg.Content).To(Equal(first.Msg.Content),
				"the previously cached content must be served verbatim when recompute fails")
			Expect(second.Msg.Hash).To(Equal(first.Msg.Hash))
		})

		// W2-S3: this suite's own Service (BeforeEach above) always wires a
		// real schema registry, under which merge.enforceRoles' fail-safe
		// excludes ANY pipeline whose content fails to even parse
		// (signals.Derive errors) BEFORE Stage 1 ever sees it — that check
		// runs regardless of role, "singleton"/Unrestricted included. So
		// exercising the specific gap this step closes — ComputeServed's
		// Stage 1 running unconditionally, with nothing able to skip it —
		// needs its own Service wired the way agentapi.New's doc comment
		// says is supported: both validator and schema registry nil, the
		// exact configuration under which recomputeServeCache used to skip
		// Stage 1 entirely (gated on "if s.validator != nil").
		It("never serves merged output that fails stage 1, even with no validator or schema registry wired", func() {
			dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
			st2, err := store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
			Expect(err).NotTo(HaveOccurred())
			defer st2.Close() //nolint:errcheck // test cleanup

			raw := make([]byte, 32)
			_, _ = rand.Read(raw)
			secret := base64.URLEncoding.EncodeToString(raw)
			hash := sha256.Sum256([]byte(secret))
			tok, err := st2.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
				Name: "stage1-token", TokenHash: hash[:], CreatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			hdr := "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))

			svc2 := agentapi.New(st2, nil, slog.Default(), nil)
			authGate2 := agentapi.NewAuthGate(st2, nil)
			path2, handler2 := collectorv1connect.NewCollectorServiceHandler(svc2, connect.WithRequestGate(authGate2))
			mux2 := http.NewServeMux()
			mux2.Handle(path2, handler2)
			server2 := httptest.NewUnstartedServer(h2c.NewHandler(mux2, &http2.Server{}))
			server2.Start()
			defer server2.Close()

			client2 := collectorv1connect.NewCollectorServiceClient(
				server2.Client(), server2.URL, connect.WithGRPC(),
				connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
					return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
						req.Header().Set("Authorization", hdr)
						return next(c, req)
					}
				})),
			)

			org, err := st2.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "stage1-org", DisplayName: "Stage1 org", AdminGroupID: "admins"})
			Expect(err).NotTo(HaveOccurred())
			_, err = client2.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "stage1-instance", Name: "stage1-instance",
				LocalAttributes: map[string]string{"cluster": "stage1-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st2.Queries.GetClusterByName(ctx, "stage1-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st2.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
			collector, err := st2.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{Name: "stage1-cluster", Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())

			_, err = st2.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org.ID, Name: "good", Contents: "// good",
				Matchers: json.RawMessage(`["cluster=\"stage1-cluster\""]`),
				Enabled:  true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())

			first, err := client2.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "stage1-instance", LocalAttributes: map[string]string{"cluster": "stage1-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(first.Msg.Content).To(ContainSubstring("good"))

			// Inserted directly via the store, bypassing ValidatePipeline's
			// own Stage-1 check at authoring time. Unbalanced braces: parses
			// as neither valid syntax nor a merge failure, only a Stage-1
			// diagnostic on the assembled content.
			_, err = st2.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org.ID, Name: "unbalanced", Contents: `prometheus.scrape "a" {`,
				Matchers: json.RawMessage(`["cluster=\"stage1-cluster\""]`),
				Enabled:  true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(st2.Queries.MarkServeCacheDirty(ctx, collector.ID)).To(Succeed())

			second, err := client2.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "stage1-instance", LocalAttributes: map[string]string{"cluster": "stage1-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Msg.Content).To(Equal(first.Msg.Content),
				"a merged config that fails Stage 1 must never reach serve_cache, even with no validator or schema "+
					"registry wired — the previous content must still be served")
			Expect(second.Msg.Hash).To(Equal(first.Msg.Hash))
		})

		// G6 (docs/gateway-tier-plan.md): the gate W1 could not close from
		// internal/merge's own suite. Enforcement is only real if it holds on
		// the path a live agent actually drives — GetConfig's lazy
		// recompute-when-dirty — not just where merge.Assemble is called
		// directly. A pipeline write marks serve_cache dirty synchronously and
		// only then launches the eager recompute, so an agent polling inside
		// that window reaches this path with the dirty flag still set.
		It("does not serve a metrics pipeline to a logs collector through the dirty-window path", func() {
			org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
				Name: "role-org", DisplayName: "Role org", AdminGroupID: "admins",
			})
			Expect(err).NotTo(HaveOccurred())

			// One cluster, one LOGS-role collector.
			_, err = client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id: "role-instance", Name: "role-instance",
				LocalAttributes: map[string]string{"cluster": "role-cluster", "role": "logs"},
			}))
			Expect(err).NotTo(HaveOccurred())
			cluster, err := st.Queries.GetClusterByName(ctx, "role-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())

			// Two pipelines matching that collector: one genuinely logs-shaped,
			// one metrics-shaped. Both are valid Alloy; only the role differs.
			logsContents := `loki.source.file "app" {
  targets    = []
  forward_to = []
}`
			metricsContents := `prometheus.scrape "app" {
  targets    = []
  forward_to = []
}`
			for name, contents := range map[string]string{
				"logs-pipeline": logsContents, "metrics-pipeline": metricsContents,
			} {
				_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
					OrgID: org.ID, Name: name, Contents: contents,
					Matchers:    json.RawMessage(`["cluster=\"role-cluster\""]`),
					Enabled:     true,
					Source:      "ui",
					WizardState: json.RawMessage(`{}`),
					CreatedBy:   "test", UpdatedBy: "test",
				})
				Expect(err).NotTo(HaveOccurred())
			}

			collector, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{
				Name: "role-cluster", Role: "logs",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.MarkServeCacheDirty(ctx, collector.ID)).To(Succeed())

			// The poll that lands inside the dirty window.
			resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id: "role-instance", LocalAttributes: map[string]string{"cluster": "role-cluster", "role": "logs"},
			}))
			Expect(err).NotTo(HaveOccurred())

			Expect(resp.Msg.Content).To(ContainSubstring("logs_pipeline"),
				"the logs pipeline must still be served — enforcement must not become deny-everything")
			Expect(resp.Msg.Content).NotTo(ContainSubstring("prometheus.scrape"),
				"a metrics pipeline reached a logs-role collector through GetConfig's lazy recompute: "+
					"role enforcement is not wired on the path live agents actually drive (G6)")
			Expect(resp.Msg.Content).To(ContainSubstring("metrics-pipeline"),
				"the exclusion must be named in the generated header — a silently dropped pipeline is "+
					"indistinguishable from one that never matched")
		})

		It("conditional upsert refuses to clear newer dirty flag", func() {
			// The write is a compare-and-swap on the dirty generation: a
			// recompute that read generation N before a newer mark bumped it
			// must not land, or its stale content would clear the newer flag.
			// (This spec used to assert the OPPOSITE — that a write made after
			// the mark, without knowing about it, still cleared the flag.)
			collector, _ := setupClaimedPipeline()
			_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			before, err := st.Queries.GetServeCache(ctx, collector.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.MarkServeCacheDirty(ctx, collector.ID)).To(Succeed())

			// A write against the generation read BEFORE the mark is refused
			// and leaves the row dirty.
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{CollectorID: collector.ID, Content: "stale", Hash: "hash-stale", DirtySeq: before.DirtySeq})
			Expect(err).To(MatchError(pgx.ErrNoRows))
			still, err := st.Queries.GetServeCache(ctx, collector.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(still.Dirty).To(BeTrue())
			Expect(still.Content).To(Equal(before.Content))

			// A write that observed the mark's generation lands and clears it.
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{CollectorID: collector.ID, Content: "new", Hash: "hash", DirtySeq: still.DirtySeq})
			Expect(err).NotTo(HaveOccurred())
			updated, err := st.Queries.GetServeCache(ctx, collector.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Dirty).To(BeFalse())
			Expect(updated.Content).To(Equal("new"))
		})

		It("prewarm and lazy recompute race safely", func() {
			collector, _ := setupClaimedPipeline()
			Expect(st.Queries.MarkServeCacheDirty(ctx, collector.ID)).To(Succeed())
			_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			cache, err := st.Queries.GetServeCache(ctx, collector.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(cache.Dirty).To(BeFalse())
		})

		It("returns empty config for unclaimed cluster", func() {
			resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id:              "instance-gc1",
				Hash:            "",
				LocalAttributes: map[string]string{"cluster": "unclaimed-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.Content).To(BeEmpty())
			Expect(resp.Msg.Hash).To(Equal(agentapi.EmptyHash))
			Expect(resp.Msg.NotModified).To(BeFalse())
		})

		It("returns not_modified when hash already equals empty hash for unclaimed cluster", func() {
			resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id:              "instance-gc2",
				Hash:            agentapi.EmptyHash,
				LocalAttributes: map[string]string{"cluster": "unclaimed-cluster2", "role": "logs"},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Msg.NotModified).To(BeTrue())
		})

		It("self-registers instance on GetConfig", func() {
			_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id:              "self-reg-instance",
				Hash:            "",
				LocalAttributes: map[string]string{"cluster": "self-reg-cluster", "role": "singleton"},
			}))
			Expect(err).NotTo(HaveOccurred())

			instance, err := st.Queries.GetCollectorInstanceByID(ctx, "self-reg-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance).NotTo(BeNil())
		})

		It("stores remote_config_status without the proto enum prefix", func() {
			_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id:              "status-instance",
				LocalAttributes: map[string]string{"cluster": "status-cluster", "role": "metrics"},
				RemoteConfigStatus: &collectorv1.RemoteConfigStatus{
					Status: collectorv1.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
				},
			}))
			Expect(err).NotTo(HaveOccurred())

			instance, err := st.Queries.GetCollectorInstanceByID(ctx, "status-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.RemoteConfigStatus.Valid).To(BeTrue())
			Expect(instance.RemoteConfigStatus.String).To(Equal("APPLIED"))
			Expect(instance.RemoteConfigStatus.String).NotTo(ContainSubstring("RemoteConfigStatuses"))
		})

		It("keeps the registered display name across subsequent GetConfig polls", func() {
			_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:              "named-instance",
				Name:            "my-hostname",
				LocalAttributes: map[string]string{"cluster": "named-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())

			_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id:              "named-instance",
				LocalAttributes: map[string]string{"cluster": "named-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())

			instance, err := st.Queries.GetCollectorInstanceByID(ctx, "named-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.Name).To(Equal("my-hostname"), "GetConfig must not overwrite the display name with the wire id")
		})

		It("leaves the stored outcome alone when a stale instance reconnects", func() {
			_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:              "reconnect-instance",
				Name:            "reconnect-instance",
				LocalAttributes: map[string]string{"cluster": "reconnect-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
				ID:                 "reconnect-instance",
				RemoteConfigStatus: pgtype.Text{String: "APPLYING", Valid: true},
			})).To(Succeed())

			// RAW-SQL-OK: backdate last_seen past inactive_after; no sqlc query
			// takes an explicit last_seen.
			_, err = st.Pool().Exec(ctx,
				`UPDATE collector_instances SET last_seen = now() - interval '1 hour' WHERE id = 'reconnect-instance'`)
			Expect(err).NotTo(HaveOccurred())
			agentapi.NewSweeper(st, &config.AgentConfig{InactiveAfter: 5 * time.Minute, DeleteAfter: 24 * time.Hour, SweepInterval: time.Minute}, slog.Default()).SweepOnce(ctx)

			instance, err := st.Queries.GetCollectorInstanceByID(ctx, "reconnect-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.RemoteConfigStatus.String).To(Equal("APPLYING"),
				"the sweeper no longer writes staleness into the outcome column (#237)")

			_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id:              "reconnect-instance",
				LocalAttributes: map[string]string{"cluster": "reconnect-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())

			instance, err = st.Queries.GetCollectorInstanceByID(ctx, "reconnect-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.RemoteConfigStatus.String).To(Equal("APPLYING"))
			Expect(instance.LastSeen.Time).To(BeTemporally("~", time.Now(), time.Minute), "reconnect is liveness: last_seen moves")
		})

		Describe("B1: stale FAILED status clearing", func() {
			// failClaimed sets up a claimed cluster/pipeline, then drives the
			// instance into FAILED with a stale error message, returning the
			// hash actually served (what a healthy agent would report back as
			// its applied hash on the next poll).
			failClaimed := func() string {
				setupClaimedPipeline()
				served, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())

				_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            served.Msg.Hash,
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
					RemoteConfigStatus: &collectorv1.RemoteConfigStatus{
						Status:       collectorv1.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
						ErrorMessage: "dial tcp: lookup shepherd: no such host",
					},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("FAILED"))

				return served.Msg.Hash
			}

			// #115, replaying what Alloy v1.20.1 was recorded doing
			// (docs/proofs/applied-status.md): after rejecting a config it
			// reports FAILED once WITH that config's hash, then keeps polling
			// with the same hash and no status while it runs its previous
			// config. That silence is not recovery.
			It("stays FAILED while the agent polls silently with the hash it rejected", func() {
				servedHash := failClaimed()

				for range 3 {
					_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
						Id:              "recompute-instance",
						Hash:            servedHash,
						LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
					}))
					Expect(err).NotTo(HaveOccurred())
				}

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("FAILED"),
					"a FAILED for the config the agent is still polling with must not read as APPLIED")
				Expect(instance.RemoteConfigError.String).To(Equal("dial tcp: lookup shepherd: no such host"),
					"the rejection reason stays visible")
			})

			It("becomes APPLIED when the agent reports APPLIED for a config", func() {
				servedHash := failClaimed()

				_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            servedHash,
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
					RemoteConfigStatus: &collectorv1.RemoteConfigStatus{
						Status: collectorv1.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
					},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("APPLIED"))
				Expect(instance.RemoteConfigError.Valid).To(BeFalse())
			})

			// F1 (the #115 walkthrough follow-up): Alloy re-sends a status only
			// when its (status, error) pair changes — remotecfg's
			// getRemoteConfigStatusForRequest. A NEW config that fails with a
			// byte-identical error (any content change below the failing line moves
			// the hash, the failing line does not -- before #213 the served header's
			// timestamp alone did it on every recompute) is therefore never
			// re-reported: the agent polls silently with the new hash. That
			// must stay FAILED — and follow the new hash — not read as recovery.
			It("stays FAILED when a new config fails with the same error, and follows the new hash", func() {
				failClaimed()
				// The FAILED was reported for an earlier config.
				Expect(st.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
					ID:                 "recompute-instance",
					RemoteConfigStatus: pgtype.Text{String: "FAILED", Valid: true},
					RemoteConfigError:  pgtype.Text{String: "40:3: Failed to build component", Valid: true},
					StatusHash:         pgtype.Text{String: "an-earlier-hash", Valid: true},
				})).To(Succeed())
				served, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            "an-earlier-hash",
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())

				// Alloy receives the new config, fails identically, says nothing.
				for range 2 {
					_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
						Id:              "recompute-instance",
						Hash:            served.Msg.Hash,
						LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
					}))
					Expect(err).NotTo(HaveOccurred())
				}

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("FAILED"),
					"silence after FAILED means the outcome is unchanged, not recovered")
				Expect(instance.RemoteConfigError.String).To(Equal("40:3: Failed to build component"))
				Expect(instance.RemoteConfigStatusHash.String).To(Equal(served.Msg.Hash),
					"the failure is now about the config the agent was just served")
			})

			// Alloy sets effective_config only after a successful load and sends
			// it whenever the loaded config changes — with no status when the
			// status itself did not change (APPLIED → APPLIED), or the first
			// time after a FAILED if the status did change.
			effective := &collectorv1.EffectiveConfig{ConfigMap: &collectorv1.AgentConfigMap{
				ConfigMap: map[string]*collectorv1.AgentConfigFile{"": {Body: []byte("// loaded")}},
			}}

			It("treats a silent poll carrying effective_config for the served hash as a load", func() {
				servedHash := failClaimed()

				_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            servedHash,
					EffectiveConfig: effective,
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("APPLIED"))
				Expect(instance.RemoteConfigError.Valid).To(BeFalse())
				Expect(instance.RemoteConfigStatusHash.String).To(Equal(servedHash))
			})

			It("keeps APPLIED on a good config replacing a good one, and follows the new hash", func() {
				servedHash := failClaimed()
				Expect(st.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
					ID:                 "recompute-instance",
					RemoteConfigStatus: pgtype.Text{String: "APPLIED", Valid: true},
					StatusHash:         pgtype.Text{String: "an-earlier-hash", Valid: true},
				})).To(Succeed())

				_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            servedHash,
					EffectiveConfig: effective,
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("APPLIED"))
				Expect(instance.RemoteConfigStatusHash.String).To(Equal(servedHash))
			})

			// #237 / F1: a FAILED instance that went stale and then comes back
			// polling silently with the served hash is still failing — Alloy
			// re-sends a status only when it changes. Staleness must not reset
			// the outcome on the way (the old sweeper's 'inactive' marker was
			// cleared to NULL on reconnect and NULL is promoted to APPLIED).
			It("keeps a FAILED instance FAILED through staleness, a sweep and a silent reconnect", func() {
				servedHash := failClaimed()
				// RAW-SQL-OK: backdate last_seen past inactive_after; no sqlc
				// query takes an explicit last_seen.
				_, err := st.Pool().Exec(ctx,
					`UPDATE collector_instances SET last_seen = now() - interval '1 hour' WHERE id = 'recompute-instance'`)
				Expect(err).NotTo(HaveOccurred())
				agentapi.NewSweeper(st, &config.AgentConfig{InactiveAfter: 5 * time.Minute, DeleteAfter: 24 * time.Hour, SweepInterval: time.Minute}, slog.Default()).SweepOnce(ctx)

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("FAILED"), "the sweep leaves the outcome alone")

				_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            servedHash,
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err = st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("FAILED"),
					"a silent poll after reconnecting must not promote a failure to APPLIED")
				Expect(instance.RemoteConfigError.String).To(Equal("dial tcp: lookup shepherd: no such host"))
			})

			It("still promotes a never-reported status to APPLIED on a served-hash poll", func() {
				_, _ = setupClaimedPipeline()
				served, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())
				_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            served.Msg.Hash,
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("APPLIED"))
			})

			It("stays FAILED when the agent keeps re-reporting FAILED", func() {
				servedHash := failClaimed()

				_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            servedHash,
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
					RemoteConfigStatus: &collectorv1.RemoteConfigStatus{
						Status:       collectorv1.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
						ErrorMessage: "still failing: permission denied",
					},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("FAILED"), "a repeated FAILED report must win over the hash-match inference")
				Expect(instance.RemoteConfigError.String).To(Equal("still failing: permission denied"))
			})

			It("stays FAILED when the reported hash does not match what was served", func() {
				failClaimed()

				_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            "not-the-served-hash",
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("FAILED"), "agent has not applied the newly served config yet")
			})
		})
	})

	Describe("UnregisterCollector", func() {
		It("marks instance as unregistered", func() {
			_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:              "to-unreg",
				Name:            "to-unreg",
				LocalAttributes: map[string]string{"cluster": "unreg-cluster", "role": "receiver"},
			}))
			Expect(err).NotTo(HaveOccurred())

			_, err = client.UnregisterCollector(ctx, connect.NewRequest(&collectorv1.UnregisterCollectorRequest{
				Id: "to-unreg",
			}))
			Expect(err).NotTo(HaveOccurred())

			instance, err := st.Queries.GetCollectorInstanceByID(ctx, "to-unreg")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.UnregisteredAt.Valid).To(BeTrue())
		})
	})

	Describe("Auth", func() {
		// The gate decides on the headers before the body is read. A request
		// whose body is not even valid JSON must therefore still be answered
		// "unauthenticated" (401), not "invalid argument" (400): the decode
		// that would have produced the 400 never runs for a caller that
		// could not authenticate. Under the previous interceptor wiring the
		// body was decoded first, and this exact request answered 400.
		It("refuses a bad token before reading the request body", func() {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				server.URL+collectorv1connect.CollectorServiceRegisterCollectorProcedure,
				strings.NewReader("this is not json"))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("bad:bad")))
			resp, err := server.Client().Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("rejects requests with a bad token", func() {
			badClient := collectorv1connect.NewCollectorServiceClient(
				server.Client(),
				server.URL,
				connect.WithGRPC(),
				connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
					return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
						req.Header().Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("bad:bad")))
						return next(c, req)
					}
				})),
			)
			_, err := badClient.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:              "x",
				Name:            "x",
				LocalAttributes: map[string]string{"cluster": "x", "role": "metrics"},
			}))
			Expect(err).To(HaveOccurred())
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeUnauthenticated))
		})
	})
})

// testSchemaRegistry loads the shipped schema so the agent path under test
// enforces roles exactly as production does. A nil registry here would make
// every agentapi test silently exercise the unenforced path.
func testSchemaRegistry() *schema.Registry {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	Expect(err).NotTo(HaveOccurred())
	return reg
}
