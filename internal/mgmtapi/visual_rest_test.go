package mgmtapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// newVisualRESTRouter wires mgmtapi.Router (the legacy REST shim surface)
// behind session + CSRF middleware, matching production wiring
// (internal/server/server.go's `r.Mount("/api", mgmtapi.Router(...))` group)
// closely enough for /visual/validate's RequireOrgAccess("orgadmin") guard
// to resolve a real session from the request cookie.
func newVisualRESTRouter(st *store.Store, authHandler *auth.Handler, cfg *config.Config) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authHandler.SessionMiddleware)
		r.Use(auth.CSRFMiddleware)
		r.Mount("/", mgmtapi.Router(st, cfg, nil, nil))
	})
	return r
}

// Regression coverage for VisualService.Validate's legacy REST shape (the
// contract-fidelity fix): an L1 (render-failure) diagnostic list must render
// exactly VisualHandler.Validate's historical body — a bare
// {"diagnostics":[...]} (no "error" key, unlike Render's 422 shape) of
// visual.RenderDiagnostic-shaped objects: layer, code (always present, no
// omitempty), node_id/node_id2 (omitempty), message — never "line"/"col",
// which never existed for this diagnostic kind. See
// internal/visual.RenderDiagnostic's json tags and
// git show HEAD:internal/mgmtapi/visual.go.
var _ = Describe("POST /orgs/{org}/visual/validate — L1 (render-failure) diagnostics", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		authHandler *auth.Handler
		server      *httptest.Server
		orgID       string
		adminCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "visual-rest-org", DisplayName: "Visual REST Org",
			AdminGroupID: "visual-rest-admin-group", ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler = auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newVisualRESTRouter(st, authHandler, cfg))

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "visual-rest-admin-group")}
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	postValidate := func(body map[string]any) *http.Response {
		buf, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest(http.MethodPost, server.URL+fmt.Sprintf("/orgs/%s/visual/validate", orgID), bytes.NewReader(buf))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.AddCookie(adminCookie)
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	// A sanitize collision (two prometheus.remote_write nodes whose labels
	// sanitize to the same Alloy identifier) is a real, currently-shipping
	// L1 diagnostic path — see internal/visual/render_test.go 7.2.5.
	collidingGraph := map[string]any{
		"graph": map[string]any{
			"schema_version": "alloy-v1.18.1",
			"nodes": []map[string]any{
				{"id": "a", "component": "prometheus.remote_write", "label": "my-sink"},
				{"id": "b", "component": "prometheus.remote_write", "label": "my_sink"},
			},
		},
	}

	It("responds 422 with a bare {\"diagnostics\":[...]} body carrying code and node_id2", func() {
		resp := postValidate(collidingGraph)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusUnprocessableEntity))

		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())

		Expect(result).NotTo(HaveKey("error"), `Validate's 422 body has no "error" key, unlike Render's`)

		diags, ok := result["diagnostics"].([]any)
		Expect(ok).To(BeTrue(), "expected a diagnostics array")
		Expect(diags).To(HaveLen(1))

		diag, ok := diags[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(diag["layer"]).To(Equal("L1"))
		Expect(diag["code"]).To(Equal("label_collision"), "code must survive onto the wire, not be dropped")
		Expect(diag["node_id2"]).To(Equal("b"), "node_id2 must survive onto the wire, not be dropped")
		Expect(diag["node_id"]).To(Equal("a"))
		Expect(diag).NotTo(HaveKey("line"), "line never existed for this diagnostic kind")
		Expect(diag).NotTo(HaveKey("col"), "col never existed for this diagnostic kind")
	})

	It("omits render_diagnostics from a successful (L2) 200 response", func() {
		validGraph := map[string]any{
			"graph": map[string]any{
				"schema_version": "alloy-v1.18.1",
				"nodes":          []map[string]any{},
			},
		}
		resp := postValidate(validGraph)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		Expect(result).To(HaveKey("diagnostics"))
		Expect(result).NotTo(HaveKey("render_diagnostics"), "the new proto field must never leak onto the legacy wire shape")
	})
})
