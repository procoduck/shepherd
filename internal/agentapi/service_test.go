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
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
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
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

// sharedPG is a single container shared across all specs in this suite.
var sharedPG *testutil.SharedPostgres

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
		svc := agentapi.New(st, nil, logger)
		authInterceptor := agentapi.NewAuthInterceptor(st)
		path, handler := collectorv1connect.NewCollectorServiceHandler(
			svc,
			connect.WithInterceptors(authInterceptor),
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
			collector, pipeline := setupClaimedPipeline()
			first, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(first.Msg.Content).NotTo(BeEmpty())

			// Make the next recompute actually FAIL: an enabled pipeline with a
			// malformed matcher (invalid regex, inserted directly so the API
			// validation is bypassed) makes merge.Assemble return an error.
			// (Deleting the pipeline instead would be a SUCCESSFUL recompute of
			// the empty set, which serves the header-only config — a different
			// contract.)
			_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: pipeline.OrgID, Name: "broken-matcher", Contents: "// broken",
				Matchers: json.RawMessage(`["cluster=~\"[\""]`), Enabled: true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.MarkServeCacheDirty(ctx, collector.ID)).To(Succeed())

			second, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Msg.Content).To(Equal(first.Msg.Content),
				"the previously cached content must be served verbatim when recompute fails")
			Expect(second.Msg.Hash).To(Equal(first.Msg.Hash))
		})

		It("conditional upsert refuses to clear newer dirty flag", func() {
			collector, _ := setupClaimedPipeline()
			_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "recompute-instance", LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"}}))
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.GetServeCache(ctx, collector.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.MarkServeCacheDirty(ctx, collector.ID)).To(Succeed())
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{CollectorID: collector.ID, Content: "new", Hash: "hash"})
			Expect(err).NotTo(HaveOccurred())
			updated, err := st.Queries.GetServeCache(ctx, collector.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Dirty).To(BeFalse())
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

		It("clears an 'inactive' remote_config_status on reconnect", func() {
			_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
				Id:              "reconnect-instance",
				Name:            "reconnect-instance",
				LocalAttributes: map[string]string{"cluster": "reconnect-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())

			// Simulate the lifecycle sweeper marking the instance inactive.
			Expect(st.Queries.MarkStaleInstancesInactive(ctx, pgtype.Timestamptz{
				Time:  time.Now().Add(time.Hour),
				Valid: true,
			})).To(Succeed())

			instance, err := st.Queries.GetCollectorInstanceByID(ctx, "reconnect-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.RemoteConfigStatus.String).To(Equal("inactive"))

			// Reconnect via a plain poll carrying no RemoteConfigStatus payload.
			_, err = client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
				Id:              "reconnect-instance",
				LocalAttributes: map[string]string{"cluster": "reconnect-cluster", "role": "metrics"},
			}))
			Expect(err).NotTo(HaveOccurred())

			instance, err = st.Queries.GetCollectorInstanceByID(ctx, "reconnect-instance")
			Expect(err).NotTo(HaveOccurred())
			Expect(instance.RemoteConfigStatus.Valid).To(BeFalse(), "reconnect should clear the stale inactive marker")
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

			It("clears FAILED to APPLIED when a later poll reports the served hash with no status", func() {
				servedHash := failClaimed()

				_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
					Id:              "recompute-instance",
					Hash:            servedHash,
					LocalAttributes: map[string]string{"cluster": "recompute-cluster", "role": "metrics"},
				}))
				Expect(err).NotTo(HaveOccurred())

				instance, err := st.Queries.GetCollectorInstanceByID(ctx, "recompute-instance")
				Expect(err).NotTo(HaveOccurred())
				Expect(instance.RemoteConfigStatus.String).To(Equal("APPLIED"), "matching hash with no fresh status means the agent recovered")
				Expect(instance.RemoteConfigError.Valid).To(BeFalse(), "stale error message must be cleared alongside the status")
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
