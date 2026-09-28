package mgmtapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

var sharedPG *testutil.SharedPostgres

func TestMgmtAPI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "MgmtAPI Integration Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	sharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(context.Background(), sharedPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if sharedPG != nil {
		Expect(sharedPG.Terminate(context.Background())).To(Succeed())
	}
})

// Pipeline lifecycle over the Connect API. These specs used to run against
// the /api REST shim; they moved to Connect when the shim was removed. Three
// of the old REST specs are not repeated here because a Connect spec already
// asserts the same property: UnclaimCluster marking serve caches dirty and
// DeleteOrg refusing a non-empty org (rpc_admin_test.go), and DeleteDestination
// refusing a destination a wizard pipeline still references
// (rpc_destination_test.go).
var _ = Describe("Pipelines API", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		orgID       string
		adminCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		// Create a test org.
		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          "testorg",
			DisplayName:   "Test Org",
			AdminGroupID:  "admin-group",
			ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		cfg := &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Validate: config.ValidateConfig{
				AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9,
			},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
		adminCookie = newAppAdminSession(ctx, st)
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	rpc := func(procedure string, body map[string]any) *http.Response {
		return postConnectJSON(server, "/shepherd.mgmt.v1."+procedure, adminCookie, body)
	}
	decode := func(resp *http.Response) map[string]any {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		var out map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&out)).To(Succeed())
		return out
	}
	createPipeline := func(name string) string {
		resp := rpc("PipelineService/CreatePipeline", map[string]any{
			"orgId": orgID, "name": name, "contents": "// ok", "matchers": []string{},
		})
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "create pipeline %q", name)
		id, _ := decode(resp)["id"].(string) //nolint:errcheck // asserted non-empty below
		Expect(id).NotTo(BeEmpty())
		return id
	}
	getPipeline := func(id string) *http.Response {
		return rpc("PipelineService/GetPipeline", map[string]any{"orgId": orgID, "id": id})
	}

	Describe("PipelineService/CreatePipeline", func() {
		It("creates a pipeline", func() {
			resp := rpc("PipelineService/CreatePipeline", map[string]any{
				"orgId":    orgID,
				"name":     "test-pipe",
				"contents": `// valid alloy comment`,
				"matchers": []string{`cluster="prod"`},
			})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			result := decode(resp)
			Expect(result["name"]).To(Equal("test-pipe"))
			// protojson omits a false bool, so "not enabled" is an absent field.
			Expect(result).NotTo(HaveKeyWithValue("enabled", true))
			Expect(result["source"]).To(Equal("ui"))
		})

		It("rejects duplicate names", func() {
			createPipeline("dup-pipe")
			resp := rpc("PipelineService/CreatePipeline", map[string]any{
				"orgId": orgID, "name": "dup-pipe", "contents": "// ok", "matchers": []string{},
			})
			Expect(resp.StatusCode).To(Equal(http.StatusConflict))
			Expect(connectErrorCode(resp)).To(Equal("already_exists"))
		})
	})

	Describe("PipelineService/ListPipelines", func() {
		It("lists pipelines", func() {
			createPipeline("list-pipe")
			resp := rpc("PipelineService/ListPipelines", map[string]any{"orgId": orgID})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			result := decode(resp)
			Expect(result["total"]).To(BeNumerically(">=", 1.0))
		})
	})

	Describe("PipelineService/ValidatePipeline", func() {
		It("returns valid for correct syntax", func() {
			resp := rpc("PipelineService/ValidatePipeline", map[string]any{"orgId": orgID, "contents": `// valid alloy content`})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(decode(resp)["valid"]).To(BeTrue())
		})

		// The REST shim answered a syntax error with HTTP 422; Connect reports
		// it in the response body instead: valid=false with diagnostics.
		It("returns valid=false with diagnostics for syntax errors", func() {
			resp := rpc("PipelineService/ValidatePipeline", map[string]any{"orgId": orgID, "contents": "prometheus.scrape { missing closing brace"})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			result := decode(resp)
			Expect(result).NotTo(HaveKeyWithValue("valid", true))
			Expect(result["diagnostics"]).NotTo(BeEmpty())
		})
	})

	Describe("PipelineService/EnablePipeline and DisablePipeline", func() {
		It("enables and disables a pipeline", func() {
			id := createPipeline("enable-me")

			resp := rpc("PipelineService/EnablePipeline", map[string]any{"orgId": orgID, "id": id})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(decode(getPipeline(id))).To(HaveKeyWithValue("enabled", true))

			resp = rpc("PipelineService/DisablePipeline", map[string]any{"orgId": orgID, "id": id})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(decode(getPipeline(id))).NotTo(HaveKeyWithValue("enabled", true))
		})
	})

	Describe("PipelineService/DeletePipeline", func() {
		It("deletes a pipeline", func() {
			id := createPipeline("del-me")

			resp := rpc("PipelineService/DeletePipeline", map[string]any{"orgId": orgID, "id": id})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			resp.Body.Close() //nolint:errcheck // test cleanup

			Expect(connectErrorCode(getPipeline(id))).To(Equal("not_found"))
		})
	})

	Describe("DestinationService/DeleteDestination", func() {
		It("allows delete after the referencing pipeline is deleted", func() {
			destination, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
				OrgID: orgUUID(orgID), Name: "deletable-destination", Type: "prometheus", Url: "http://prometheus",
				SecretName: "secret", SecretNamespace: "default", AuthMode: "none", Extra: json.RawMessage("{}"),
			})
			Expect(err).NotTo(HaveOccurred())
			var pipelineID pgtype.UUID
			err = st.Pool().QueryRow(ctx, `INSERT INTO pipelines (org_id, name, contents, source, wizard_state) VALUES ($1, $2, '', 'wizard', $3) RETURNING id`,
				orgUUID(orgID), "deletable-destination-pipeline", json.RawMessage(fmt.Sprintf(`{"destination_id":%q}`, destination.ID.String()))).Scan(&pipelineID)
			Expect(err).NotTo(HaveOccurred())

			pipelineResp := rpc("PipelineService/DeletePipeline", map[string]any{"orgId": orgID, "id": pipelineID.String()})
			pipelineResp.Body.Close() //nolint:errcheck // test cleanup
			Expect(pipelineResp.StatusCode).To(Equal(http.StatusOK))
			resp := rpc("DestinationService/DeleteDestination", map[string]any{"orgId": orgID, "id": destination.ID.String()})
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})
	})
})

// newAppAdminSession creates an app-admin session and returns its cookie.
// An app admin is authorized for every org-scoped procedure (see
// auth.authorizeOrgAccess's IsAppAdmin bypass), so specs that are not about
// authorization use one session for every request rather than juggling
// per-procedure reader/org-admin identities.
func newAppAdminSession(ctx context.Context, st *store.Store) *http.Cookie {
	id := fmt.Sprintf("app-admin-sess-%d", time.Now().UnixNano())
	groupsJSON, err := json.Marshal([]string{})
	Expect(err).NotTo(HaveOccurred())
	_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
		ID: id, UserOid: "user-" + id, Email: id + "@example.com", DisplayName: "Test App Admin",
		GroupIds:   groupsJSON,
		IsAppAdmin: true,
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		Source:     "test",
	})
	Expect(err).NotTo(HaveOccurred())
	return &http.Cookie{Name: "shepherd_session", Value: id}
}

// postJSON issues a POST with a JSON body, cookie, and the CSRF header the
// production web transport always sends on mutating requests. Connect unary
// calls are plain JSON POSTs, so this serves any procedure path.
func postJSON(server *httptest.Server, path string, body any, cookie *http.Cookie) *http.Response {
	var buf bytes.Buffer
	if body != nil {
		if encErr := json.NewEncoder(&buf).Encode(body); encErr != nil {
			panic(encErr)
		}
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+path, &buf)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	return resp
}

func orgUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}
