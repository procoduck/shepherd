package mgmtapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

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

// newPipelineRPCRouter builds a router shaped like internal/server/server.go's
// production wiring for the shepherd.mgmt.v1 Connect handlers: session +
// CSRF middleware ahead of MountRPC, matching how the authz interceptor
// expects to find the session in context.
func newPipelineRPCRouter(st *store.Store, authHandler *auth.Handler, cfg *config.Config) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(authHandler.SessionMiddleware)
		r.Use(auth.CSRFMiddleware)
		mgmtapi.MountRPC(r, st, cfg, nil, slog.Default())
	})
	return r
}

var _ = Describe("PipelineService Connect RPC", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		authHandler *auth.Handler
		cfg         *config.Config
		orgID       string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          "rpc-pipeline-org",
			DisplayName:   "RPC Pipeline Org",
			AdminGroupID:  "admin-group",
			ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		cfg = &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Validate: config.ValidateConfig{
				AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9,
			},
		}
		authHandler = auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newPipelineRPCRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// sessionCookie creates a session row and returns the cookie referencing it.
	sessionCookie := func(isAppAdmin bool) *http.Cookie {
		sessionID := "pipeline-rpc-session-" + time.Now().Format(time.RFC3339Nano)
		_, err := st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "user-oid", Email: "user@example.com", DisplayName: "User",
			GroupIds:   json.RawMessage(`[]`),
			IsAppAdmin: isAppAdmin,
			ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:     "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: sessionID}
	}

	postConnect := func(procedure string, body any, cookie *http.Cookie) *http.Response {
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

	decodeBody := func(resp *http.Response, v any) {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		b, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(b, v)).To(Succeed())
	}

	It("creates a pipeline over the Connect endpoint (happy path)", func() {
		cookie := sessionCookie(true) // app admin: satisfies org-admin requirement for CreatePipeline
		resp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id":   orgID,
			"name":     "connect-created-pipe",
			"contents": `// valid alloy comment`,
			"matchers": []string{`cluster="prod"`},
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK)) // Connect unary success is always HTTP 200

		var result map[string]any
		decodeBody(resp, &result)
		Expect(result["name"]).To(Equal("connect-created-pipe"))
		// The raw Connect wire protocol uses the connect-go runtime's default
		// protojson codec (camelCase field names), distinct from the REST
		// shim's byte-compatible snake_case rendering (shim.go's MarshalOpts).
		Expect(result["orgId"]).To(Equal(orgID))
		// The default protojson codec omits zero-value fields (no
		// EmitUnpopulated), so a freshly created (disabled) pipeline may
		// simply lack the "enabled" key rather than carrying false.
		Expect(result["enabled"]).To(Or(BeNil(), BeFalse()))
		Expect(result["source"]).To(Equal("ui"))
	})

	It("denies CreatePipeline for a session without org-admin access", func() {
		cookie := sessionCookie(false) // no group memberships: fails the org-admin requirement
		resp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id":   orgID,
			"name":     "should-not-be-created",
			"contents": `// valid alloy comment`,
			"matchers": []string{},
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

		var payload struct {
			Code string `json:"code"`
		}
		decodeBody(resp, &payload)
		Expect(payload.Code).To(Equal("permission_denied"))
	})

	It("maps a missing pipeline to the Connect not_found code (404)", func() {
		cookie := sessionCookie(true)
		resp := postConnect("/shepherd.mgmt.v1.PipelineService/GetPipeline", map[string]any{
			"org_id": orgID,
			"id":     "00000000-0000-0000-0000-000000000000",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))

		var payload struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		decodeBody(resp, &payload)
		Expect(payload.Code).To(Equal("not_found"))
		Expect(payload.Message).To(Equal("pipeline not found"))
	})
})
