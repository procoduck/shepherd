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

	"connectrpc.com/connect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/config"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// #390 on the lazy serve path: a strict org's pinned import.git is served as
// an import.string of the module the lint checked, never as an import
// collectors would fetch (and a tag pushed under the SHA's name could
// redirect). When the module cannot be inlined the collector keeps its last
// good config and the failure is recorded — never the raw import.git.
var _ = Describe("Pinned import.git on the lazy serve path", Label("integration"), func() {
	const (
		repo = "https://git.example.com/team/modules.git"
		sha  = "0123456789abcdef0123456789abcdef01234567"
	)
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		client collectorv1connect.CollectorServiceClient
		org    sqlc.Org
		key    string
		// clientFor starts the server with tenants.require_owned as given.
		clientFor func(requireOwned bool)
	)

	start := func(requireOwned bool) {
		svc := agentapi.New(st, nil, slog.Default(), testSchemaRegistry(), agentapi.WithRequireOwnedTenants(requireOwned))
		path, handler := collectorv1connect.NewCollectorServiceHandler(svc, connect.WithRequestGate(agentapi.NewAuthGate(st, nil)))
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server = httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
		server.Start()
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		org, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "pinned-lazy", DisplayName: "P", AdminGroupID: "admins"})
		Expect(err).NotTo(HaveOccurred())
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "pinned-token", TokenHash: hash[:], CreatedBy: "test", OrgID: org.ID, MayClaim: true,
		})
		Expect(err).NotTo(HaveOccurred())
		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))
		key, err = pinnedimport.NormalizeRepository(repo)
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.AddOrgImportRepository(ctx, sqlc.AddOrgImportRepositoryParams{OrgID: org.ID, Repository: repo, RepositoryKey: key, CreatedBy: "t"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.PutPinnedImportModule(ctx, sqlc.PutPinnedImportModuleParams{
			OrgID: org.ID, RepositoryKey: key, Revision: sha, Path: "m.alloy", Content: "declare \"linted\" { }\n",
		})).To(Succeed())
		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org.ID, Name: "pinned",
			Contents: "import.git \"m\" {\n  repository = \"" + repo + "\"\n  revision   = \"" + sha + "\"\n  path       = \"m.alloy\"\n}\n",
			Matchers: json.RawMessage(`["cluster=\"lazy-cluster\""]`), Enabled: true, Source: "ui",
			WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		client = nil
		DeferCleanup(func() {
			if server != nil {
				server.Close()
			}
			st.Close()
			cancel()
		})
		newClient := func() collectorv1connect.CollectorServiceClient {
			return collectorv1connect.NewCollectorServiceClient(server.Client(), server.URL, connect.WithGRPC(),
				connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
					return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
						req.Header().Set("Authorization", authHeader)
						return next(c, req)
					}
				})))
		}
		clientFor = func(requireOwned bool) {
			start(requireOwned)
			client = newClient()
		}
	})

	poll := func() string {
		GinkgoHelper()
		_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
			Id: "lazy-instance", Name: "lazy-instance", LocalAttributes: map[string]string{"cluster": "lazy-cluster", "role": "singleton"},
		}))
		Expect(err).NotTo(HaveOccurred())
		cl, err := st.Queries.GetClusterByName(ctx, "lazy-cluster")
		Expect(err).NotTo(HaveOccurred())
		if !cl.OrgID.Valid {
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: org.ID})).To(Succeed())
		}
		c, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{Name: "lazy-cluster", Role: "singleton"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.MarkServeCacheDirty(ctx, c.ID)).To(Succeed())
		resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "lazy-instance", LocalAttributes: map[string]string{"cluster": "lazy-cluster", "role": "singleton"},
		}))
		Expect(err).NotTo(HaveOccurred())
		return resp.Msg.Content
	}

	It("serves tenants.require_owned orgs the inlined module, and keeps the last good config when it cannot", func() {
		clientFor(true)
		content := poll()
		Expect(content).To(ContainSubstring(`import.string "m"`))
		Expect(content).To(ContainSubstring(`declare \"linted\"`))
		Expect(content).NotTo(ContainSubstring("import.git"))

		Expect(st.Queries.DeletePinnedImportModule(ctx, sqlc.DeletePinnedImportModuleParams{
			OrgID: org.ID, RepositoryKey: key, Revision: sha, Path: "m.alloy",
		})).To(Succeed())
		Expect(poll()).To(Equal(content), "the last good config, never the raw import.git")
		c, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{Name: "lazy-cluster", Role: "singleton"})
		Expect(err).NotTo(HaveOccurred())
		row, err := st.Queries.GetServeCache(ctx, c.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.LastFailedSeq).To(BeNumerically(">", 0))
	})

	It("passes a non-strict org's import.git through as written", func() {
		clientFor(false)
		Expect(poll()).To(ContainSubstring(`import.git "m"`))
	})
})
