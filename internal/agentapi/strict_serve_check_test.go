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
	"shepherd/internal/config"
	"shepherd/internal/serve"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Strict tenant mode's serve-time check on the lazy serve path: a pipeline
// stored before the check it now fails (inserted here straight through sqlc,
// as an older version would have saved it) is left out of the collector's
// served config, named in the header, audited once — and served again once
// a fix is saved. An org not in strict mode is served the same text as is.
var _ = Describe("The strict tenant check on the lazy serve path", Label("integration"), func() {
	const computed = "loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n" +
		"  external_labels = encoding.from_json(\"{}\")\n}\n"
	const clean = "logging {\n  level = \"info\"\n}\n"
	var (
		ctx      context.Context
		cancel   context.CancelFunc
		st       *store.Store
		server   *httptest.Server
		client   collectorv1connect.CollectorServiceClient
		org      sqlc.Org
		pipeline sqlc.Pipeline
	)

	setup := func(strict bool) {
		ctx, cancel = context.WithCancel(context.Background())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		org, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "strict-lazy", DisplayName: "S", AdminGroupID: "admins"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.AddOrgTenant(ctx, sqlc.AddOrgTenantParams{OrgID: org.ID, Tenant: "acme", Kind: "exact", IsDefault: true, CreatedBy: "t"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.SetOrgStrictTenants(ctx, sqlc.SetOrgStrictTenantsParams{ID: org.ID, StrictTenants: strict})
		Expect(err).NotTo(HaveOccurred())
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "strict-token", TokenHash: hash[:], CreatedBy: "test", OrgID: org.ID, MayClaim: true,
		})
		Expect(err).NotTo(HaveOccurred())
		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))
		for name, contents := range map[string]string{"computed": computed, "clean": clean} {
			p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org.ID, Name: name, Contents: contents,
				Matchers: json.RawMessage(`["cluster=\"lazy-cluster\""]`), Enabled: true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			if name == "computed" {
				pipeline = p
			}
		}
		svc := agentapi.New(st, nil, slog.Default(), testSchemaRegistry())
		path, handler := collectorv1connect.NewCollectorServiceHandler(svc, connect.WithRequestGate(agentapi.NewAuthGate(st, nil)))
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server = httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
		server.Start()
		client = collectorv1connect.NewCollectorServiceClient(server.Client(), server.URL, connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", authHeader)
					return next(c, req)
				}
			})))
		DeferCleanup(func() {
			server.Close()
			st.Close()
			cancel()
		})
	}

	poll := func() string {
		GinkgoHelper()
		attrs := map[string]string{"cluster": "lazy-cluster", "role": "singleton"}
		_, err := client.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
			Id: "lazy-instance", Name: "lazy-instance", LocalAttributes: attrs,
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
		resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{Id: "lazy-instance", LocalAttributes: attrs}))
		Expect(err).NotTo(HaveOccurred())
		return resp.Msg.Content
	}
	audits := func(action string) int {
		GinkgoHelper()
		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: org.ID, Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		n := 0
		for _, r := range rows {
			if r.Action == action {
				Expect(r.ResourceID).To(Equal(pipeline.ID.String()))
				n++
			}
		}
		return n
	}

	It("leaves out a stored pipeline the check refuses, names it in the header, audits it once, and serves it again once fixed", func() {
		setup(true)
		content := poll()
		Expect(content).To(ContainSubstring("// Excluded (1) - strict tenant check:\n//   - computed: "))
		Expect(content).To(ContainSubstring("not a literal map"))
		Expect(content).NotTo(ContainSubstring(`declare "pipe_computed"`))
		Expect(content).To(ContainSubstring(`declare "pipe_clean"`))
		Expect(poll()).To(Equal(content), "a recompute of the same inputs serves the same bytes")
		Expect(audits(serve.AuditActionTenantCheckExcluded)).To(Equal(1), "audited once per change, not per recompute")
		row, err := st.Queries.GetPipelineTenantExclusion(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Reason).To(ContainSubstring("not a literal map"))

		// The pipeline stays stored and enabled; a fix includes it again.
		got, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Enabled).To(BeTrue())
		_, err = st.Queries.UpdatePipeline(ctx, sqlc.UpdatePipelineParams{
			ID: pipeline.ID, Name: got.Name, Contents: strings.Replace(computed, `encoding.from_json("{}")`, `{ team = "a" }`, 1),
			Matchers: got.Matchers, UpdatedBy: "test", WizardState: got.WizardState,
		})
		Expect(err).NotTo(HaveOccurred())
		fixed := poll()
		Expect(fixed).To(ContainSubstring(`declare "pipe_computed"`))
		Expect(fixed).NotTo(ContainSubstring("strict tenant check"))
		Expect(audits(serve.AuditActionTenantCheckCleared)).To(Equal(1))
		_, err = st.Queries.GetPipelineTenantExclusion(ctx, pipeline.ID)
		Expect(err).To(HaveOccurred(), "the exclusion row is gone")
	})

	It("serves a non-strict org the same pipeline as stored, and records nothing", func() {
		setup(false)
		content := poll()
		Expect(content).To(ContainSubstring(`declare "pipe_computed"`))
		Expect(content).NotTo(ContainSubstring("strict tenant check"))
		Expect(audits(serve.AuditActionTenantCheckExcluded)).To(BeZero())
	})
})
