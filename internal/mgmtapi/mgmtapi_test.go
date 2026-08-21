package mgmtapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/mgmtapi"
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
		server = httptest.NewServer(newRESTRouter(st, authHandler, cfg, nil))
		adminCookie = newAppAdminSession(ctx, st)
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	Describe("POST /orgs/{org}/pipelines", func() {
		It("creates a pipeline", func() {
			body := map[string]any{
				"name":     "test-pipe",
				"contents": `// valid alloy comment`,
				"matchers": []string{`cluster="prod"`},
			}
			resp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), body, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))

			var result map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result["name"]).To(Equal("test-pipe"))
			Expect(result["enabled"]).To(BeFalse())
			Expect(result["source"]).To(Equal("ui"))
		})

		It("rejects duplicate names", func() {
			body := map[string]any{"name": "dup-pipe", "contents": "// ok", "matchers": []string{}}
			resp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), body, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusCreated))
			resp2 := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), body, adminCookie)
			Expect(resp2.StatusCode).To(Equal(http.StatusConflict))
		})
	})

	Describe("GET /orgs/{org}/pipelines", func() {
		It("lists pipelines", func() {
			postJSON(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), map[string]any{
				"name": "list-pipe", "contents": "// ok", "matchers": []string{},
			}, adminCookie)
			resp := getRequest(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var result map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			Expect(result["total"]).To(BeNumerically(">=", 1.0))
		})
	})

	Describe("POST /orgs/{org}/pipelines/validate", func() {
		It("returns valid for correct syntax", func() {
			body := map[string]any{"contents": `// valid alloy content`}
			resp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines/validate", orgID), body, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})

		It("returns 422 for syntax errors", func() {
			body := map[string]any{"contents": "prometheus.scrape { missing closing brace"}
			resp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines/validate", orgID), body, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusUnprocessableEntity))
		})
	})

	Describe("enable/disable pipeline", func() {
		var pipelineID string

		BeforeEach(func() {
			body := map[string]any{"name": "enable-me", "contents": "// ok", "matchers": []string{}}
			resp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), body, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusCreated), "create pipeline for enable/disable")
			var result map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			idVal, _ := result["id"].(string) //nolint:errcheck // map value may be absent in test; empty string is safe
			pipelineID = idVal
		})

		It("enables and disables a pipeline", func() {
			resp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines/%s/enable", orgID, pipelineID), nil, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			resp = postJSON(server, fmt.Sprintf("/orgs/%s/pipelines/%s/disable", orgID, pipelineID), nil, adminCookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})
	})

	Describe("DELETE /orgs/{org}/pipelines/{id}", func() {
		It("deletes a pipeline", func() {
			resp := postJSON(server, fmt.Sprintf("/orgs/%s/pipelines", orgID), map[string]any{
				"name": "del-me", "contents": "// ok", "matchers": []string{},
			}, adminCookie)
			var result map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
			idVal, _ := result["id"].(string) //nolint:errcheck // map value may be absent in test; empty string is safe
			id := idVal

			resp2 := deleteRequest(server, fmt.Sprintf("/orgs/%s/pipelines/%s", orgID, id), adminCookie)
			Expect(resp2.StatusCode).To(Equal(http.StatusNoContent))
		})
	})

	Describe("UnclaimCluster", func() {
		It("marks serve_cache dirty for the cluster's collectors after unclaim", func() {
			cluster, err := st.Queries.UpsertCluster(ctx, "unclaim-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
			collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Pool().Exec(ctx, `INSERT INTO serve_cache (collector_id, dirty) VALUES ($1, false)`, collector.ID)
			Expect(err).NotTo(HaveOccurred())

			resp := postJSON(server, "/admin/clusters/unclaim-cluster/unclaim", nil, adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var dirty bool
			Expect(st.Pool().QueryRow(ctx, `SELECT dirty FROM serve_cache WHERE collector_id = $1`, collector.ID).Scan(&dirty)).To(Succeed())
			Expect(dirty).To(BeTrue())
		})
	})

	Describe("DeleteDestination", func() {
		It("returns 409 with in_use code when referenced by a wizard pipeline", func() {
			destination, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
				OrgID: orgUUID(orgID), Name: "referenced-destination", Type: "prometheus", Url: "http://prometheus",
				SecretName: "secret", SecretNamespace: "default", AuthMode: "none", Extra: json.RawMessage("{}"),
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Pool().Exec(ctx, `INSERT INTO pipelines (org_id, name, contents, source, wizard_state) VALUES ($1, $2, '', 'wizard', $3)`,
				orgUUID(orgID), "destination-pipeline", json.RawMessage(fmt.Sprintf(`{"destination_id":%q}`, destination.ID.String())))
			Expect(err).NotTo(HaveOccurred())

			resp := deleteRequest(server, fmt.Sprintf("/orgs/%s/destinations/%s", orgID, destination.ID.String()), adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusConflict))
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring(`"in_use"`))
			Expect(string(body)).To(ContainSubstring("destination-pipeline"))
		})

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

			pipelineResp := deleteRequest(server, fmt.Sprintf("/orgs/%s/pipelines/%s", orgID, pipelineID.String()), adminCookie)
			pipelineResp.Body.Close() //nolint:errcheck // test cleanup
			Expect(pipelineResp.StatusCode).To(Equal(http.StatusNoContent))
			resp := deleteRequest(server, fmt.Sprintf("/orgs/%s/destinations/%s", orgID, destination.ID.String()), adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
		})
	})

	Describe("DeleteOrg", func() {
		It("returns 409 not_empty when org has clusters", func() {
			cluster, err := st.Queries.UpsertCluster(ctx, "non-empty-org-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())

			resp := deleteRequest(server, "/admin/orgs/"+orgID, adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusConflict))
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring(`"not_empty"`))
		})

		It("deletes an empty org successfully", func() {
			resp := deleteRequest(server, "/admin/orgs/"+orgID, adminCookie)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
		})
	})
})

// newRESTRouter wires mgmtapi.Router (the legacy REST shim surface) behind
// session + CSRF middleware, matching production wiring
// (internal/server/server.go's `r.Mount("/api", mgmtapi.Router(...))` group)
// closely enough for the router's RequireAuth/RequireAppAdmin/
// RequireOrgAccess guards to resolve a real session from the request
// cookie. See also newVisualRESTRouter (visual_rest_test.go), which does the
// same thing for its own Describe block.
func newRESTRouter(st *store.Store, authHandler *auth.Handler, cfg *config.Config, enc *crypto.Encryptor) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authHandler.SessionMiddleware)
		r.Use(auth.CSRFMiddleware)
		r.Mount("/", mgmtapi.Router(st, cfg, enc, nil))
	})
	return r
}

// newAppAdminSession creates an app-admin session and returns its cookie.
// An app admin is authorized for every route the REST shim exposes (see
// auth.authorizeOrgAccess's IsAppAdmin bypass), so the REST-shim
// compatibility-oracle tests in this package use one session for every
// request rather than juggling per-route reader/org-admin identities.
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
// production web transport always sends on mutating requests.
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

// getRequest issues a GET with a session cookie.
func getRequest(server *httptest.Server, path string, cookie *http.Cookie) *http.Response {
	req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		panic(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	return resp
}

// deleteRequest issues a DELETE with a session cookie and the CSRF header
// the production web transport always sends on mutating requests.
func deleteRequest(server *httptest.Server, path string, cookie *http.Cookie) *http.Response {
	req, err := http.NewRequest(http.MethodDelete, server.URL+path, nil)
	if err != nil {
		panic(err)
	}
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
