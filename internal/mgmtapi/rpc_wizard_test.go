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

// wizardTestFixture is shared setup for the WizardService Connect-path specs:
// an org with distinct admin/reader groups and sessions for each, wired
// through the exact production RPC mounting (newRPCWiringRouter, defined in
// rpc_wiring_test.go).
var _ = Describe("shepherd.mgmt.v1.WizardService", Label("integration"), func() {
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
			Name: "wizard-rpc-org", DisplayName: "Wizard RPC Org",
			AdminGroupID: "wizard-admin-grp", ReaderGroupID: pgtype.Text{String: "wizard-reader-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		// The destination the specs' metrics_dest_name names: wizards resolve
		// it against the org's rows (#229), so it must exist.
		_, err = st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: o.ID, Name: "mimir", Type: "prometheus", Url: "http://mimir:9009/api/v1/push",
			AuthMode: "none", Extra: json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "wizard-admin-grp")}
		readerCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "wizard-reader-grp")}

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("commits a wizard over the Connect handler (happy path)", func() {
		body := map[string]any{
			"org_id": orgID,
			"kind":   "app-observability",
			"name":   "wizard-commit-rpc",
			"state": map[string]any{
				"scrape_url":        "http://myapp:9090/metrics",
				"metrics_dest_name": "mimir",
			},
		}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.WizardService/CommitWizard", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var pipeline map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&pipeline)).To(Succeed())
		Expect(pipeline["name"]).To(Equal("wizard-commit-rpc"))
		Expect(pipeline["source"]).To(Equal("wizard"))
		Expect(pipeline["contents"]).To(ContainSubstring("prometheus.scrape"))
	})

	It("denies CommitWizard for a session without org-editor access", func() {
		body := map[string]any{
			"org_id": orgID,
			"kind":   "app-observability",
			"name":   "should-not-be-created",
			"state":  map[string]any{"scrape_url": "http://myapp:9090/metrics", "metrics_dest_name": "mimir"},
		}
		// A reader-group session has org-reader, not org-editor — CommitWizard requires org-editor.
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.WizardService/CommitWizard", readerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(connectErrorCode(resp)).To(Equal("permission_denied"))
	})

	It("maps an unknown wizard kind to invalid_argument", func() {
		body := map[string]any{
			"org_id": orgID,
			"kind":   "does-not-exist",
			"name":   "irrelevant",
			"state":  map[string]any{},
		}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.WizardService/CommitWizard", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("invalid_argument"))
	})

	It("renders a wizard preview without creating a pipeline", func() {
		body := map[string]any{
			"org_id": orgID,
			"kind":   "app-observability",
			"name":   "wizard-render-rpc",
			"state": map[string]any{
				"scrape_url":        "http://myapp:9090/metrics",
				"metrics_dest_name": "mimir",
				"cluster_pattern":   "prod-.*",
				"role":              "metrics",
			},
		}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.WizardService/RenderWizard", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var render map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&render)).To(Succeed())
		Expect(render["contents"]).To(ContainSubstring("prometheus.scrape"))
		// The default protojson codec omits zero-value/empty fields (no
		// EmitUnpopulated) -- a passing render may simply lack the "valid"
		// and "diagnostics" keys rather than carrying true/[].
		Expect(render["valid"]).To(Or(BeNil(), BeTrue()))
		Expect(render["diagnostics"]).To(BeNil())
		Expect(render["matchers"]).To(ConsistOf(`cluster=~"prod-.*"`, `role="metrics"`))

		// Nothing was persisted: the pipeline list for this org stays empty.
		listResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/ListPipelines", adminCookie,
			map[string]any{"org_id": orgID})
		defer listResp.Body.Close() //nolint:errcheck // test cleanup
		var list map[string]any
		Expect(json.NewDecoder(listResp.Body).Decode(&list)).To(Succeed())
		Expect(list["items"]).To(Or(BeNil(), BeEmpty()))
	})

	// Since #289 App Observability always emits a role matcher (singleton when
	// logs are collected, metrics otherwise), so a metrics pipeline no longer
	// lands on a logs collector and role enforcement (gate G6) has nothing to
	// exclude. The exclusion warning itself is pinned in role_exclusion_test.go.
	It("matches only the collectors its role allows, with no role-exclusion warning", func() {
		oid := mustUUID(orgID)
		cluster, err := st.Queries.UpsertCluster(ctx, "prod-eu")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: oid})).To(Succeed())
		_, err = st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "logs"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		// No "role" in state: the wizard picks metrics, as no logs are collected.
		body := map[string]any{
			"org_id": orgID,
			"kind":   "app-observability",
			"name":   "wizard-render-excluded",
			"state": map[string]any{
				"scrape_url":        "http://myapp:9090/metrics",
				"metrics_dest_name": "mimir",
				"cluster_pattern":   "prod-.*",
			},
		}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.WizardService/RenderWizard", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var render map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&render)).To(Succeed())
		Expect(render["matchedCollectors"]).To(ConsistOf(HaveKeyWithValue("role", "metrics")))
		Expect(fmt.Sprint(render["warnings"])).NotTo(ContainSubstring("Excluded from"))
	})

	It("surfaces stage-1 syntax diagnostics from RenderWizard without failing the RPC", func() {
		body := map[string]any{
			"org_id": orgID,
			"kind":   "app-observability",
			"name":   "wizard-render-invalid",
			"state": map[string]any{
				// job_name containing a quote breaks the generated Alloy syntax,
				// giving Stage 1 something concrete to reject. (scrape_url
				// used to carry the quote; the wizard now parses the URL and
				// refuses one that is not a scrape target.)
				"scrape_url":        "http://myapp:9090/metrics",
				"job_name":          `myapp" broken = "x`,
				"metrics_dest_name": "mimir",
			},
		}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.WizardService/RenderWizard", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var render map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&render)).To(Succeed())
		// invalid Alloy syntax means "valid" is false -- the default
		// protojson codec omits false zero-values entirely (see the
		// happy-path test's comment), so its absence here is exactly what a
		// failing render looks like; the diagnostics list is the real signal.
		Expect(render["valid"]).To(Or(BeNil(), BeFalse()))
		Expect(render["diagnostics"]).NotTo(BeEmpty())
	})

	// The Review step shows this refusal inline (WizardRunnerPage), so its
	// message has to name the field and the fix.
	It("refuses a RenderWizard whose scrape_url is not a scrape target, naming the field", func() {
		body := map[string]any{
			"org_id": orgID,
			"kind":   "app-observability",
			"name":   "wizard-render-bad-url",
			"state": map[string]any{
				"scrape_url":        "ftp://myapp/metrics",
				"metrics_dest_name": "mimir",
			},
		}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.WizardService/RenderWizard", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		var payload struct{ Code, Message string }
		Expect(json.NewDecoder(resp.Body).Decode(&payload)).To(Succeed())
		Expect(payload.Code).To(Equal("failed_precondition"))
		Expect(payload.Message).To(ContainSubstring(`scrape_url "ftp://myapp/metrics": scheme must be http or https`))
	})
})

// -- shared Connect-path test helpers (used by rpc_wizard_test.go,
// rpc_visual_test.go, rpc_simulate_test.go) --

// newTestSession creates a session for a user in the given group and returns its ID.
func newTestSession(ctx context.Context, st *store.Store, groupID string) string {
	id := fmt.Sprintf("sess-%s-%d", groupID, time.Now().UnixNano())
	groups := []string{}
	if groupID != "" {
		groups = []string{groupID}
	}
	groupsJSON, err := json.Marshal(groups)
	Expect(err).NotTo(HaveOccurred())
	_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
		ID: id, UserOid: "user-" + id, Email: id + "@example.com", DisplayName: "Test User",
		GroupIds:   groupsJSON,
		IsAppAdmin: false,
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		Source:     "test",
	})
	Expect(err).NotTo(HaveOccurred())
	return id
}

// postConnectJSON issues a Connect unary POST with a JSON body, matching the
// production web transport's required X-Requested-With CSRF header.
func postConnectJSON(server *httptest.Server, procedure string, cookie *http.Cookie, body any) *http.Response {
	b, err := json.Marshal(body)
	Expect(err).NotTo(HaveOccurred())
	req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(string(b)))
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

// connectErrorCode extracts the Connect error "code" field from an error response body.
func connectErrorCode(resp *http.Response) string {
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	var payload struct {
		Code string `json:"code"`
	}
	Expect(json.Unmarshal(body, &payload)).To(Succeed())
	return payload.Code
}
