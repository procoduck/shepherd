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

	// F6: wizard_state must survive a text-only PUT and be replaceable by an
	// explicit one. Both cases seed source="visual" directly via
	// st.Queries.CreatePipeline (bypassing the HTTP save path's visual
	// render-equality gate) so the persisted `source` column, and the
	// scenario these tests describe, is exactly the one in
	// docs/project-status.md's F6: "a visual pipeline edited through the
	// text API".
	It("preserves a visual pipeline's stored wizard_state when an update omits the field", func() {
		cookie := sessionCookie(true)

		initialGraph := json.RawMessage(`{"kind":"alloy-graph/v1","schema_version":"v1","nodes":[{"id":"n1"}]}`)
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "visual-preserve-pipe", Contents: "// v1\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "visual",
			WizardState: initialGraph, CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		// Text-only edit: the request carries no "wizard_state" key at all,
		// exactly what the visual pipeline editor's raw-Alloy tab would send.
		resp := postConnect("/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"org_id": orgID, "id": p.ID.String(), "name": "visual-preserve-pipe",
			"contents": "// v2 text-only edit\n", "matchers": []string{}, "source": "visual",
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		stored, err := st.Queries.GetPipelineByID(ctx, p.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Contents).To(Equal("// v2 text-only edit\n"))
		Expect(string(stored.WizardState)).To(MatchJSON(initialGraph))
	})

	It("replaces a visual pipeline's stored wizard_state when the update sends a new graph", func() {
		cookie := sessionCookie(true)

		initialGraph := json.RawMessage(`{"kind":"alloy-graph/v1","schema_version":"v1","nodes":[{"id":"n1"}]}`)
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "visual-replace-pipe", Contents: "// v1\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "visual",
			WizardState: initialGraph, CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		newGraph := map[string]any{
			"kind": "alloy-graph/v1", "schema_version": "v2",
			"nodes": []map[string]any{{"id": "n2"}},
		}
		// The request's "source" is deliberately "wizard", not "visual",
		// purely to route around PipelineService's visual render-equality
		// gate (checkVisualRenderMatch in rpc_pipeline.go), which engages
		// only when source=="visual" and would otherwise require `contents`
		// to be the exact canonical render of newGraph — orthogonal to what
		// this test proves. UpdatePipeline's SQL never has a `source`
		// column in its SET list (pipelines.sql), so the row's persisted
		// source stays "visual" regardless, asserted below.
		resp := postConnect("/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"org_id": orgID, "id": p.ID.String(), "name": "visual-replace-pipe",
			"contents": "// v2\n", "matchers": []string{}, "source": "wizard",
			"wizard_state": newGraph,
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		stored, err := st.Queries.GetPipelineByID(ctx, p.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Source).To(Equal("visual")) // UpdatePipeline never mutates source
		newGraphJSON, err := json.Marshal(newGraph)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(stored.WizardState)).To(MatchJSON(newGraphJSON))
	})

	// B3(a) (docs/reviews/README.md): `Pipeline` (pipeline.proto) previously
	// had no wizard_state field at all — only Create/UpdatePipelineRequest
	// carried it — so GetPipeline could never return the stored graph and
	// the visual builder (D3: wizard_state is the source of truth on load)
	// always fell back to the lossy text re-parse, losing node ids,
	// positions, labels, notes, disabled flags, bindings and non-scalar props.
	It("returns a visual pipeline's stored wizard_state on GetPipeline", func() {
		cookie := sessionCookie(true)

		graph := json.RawMessage(`{"kind":"alloy-graph/v1","schema_version":"v1","nodes":[{"id":"n1","component":"prometheus.remote_write","label":"sink","position":{"x":-256,"y":-304}}],"edges":[],"bindings":[]}`)
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "visual-getpipeline-pipe", Contents: "// v1\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "visual",
			WizardState: graph, CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		resp := postConnect("/shepherd.mgmt.v1.PipelineService/GetPipeline", map[string]any{
			"org_id": orgID, "id": p.ID.String(),
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var result map[string]any
		decodeBody(resp, &result)
		ws, ok := result["wizardState"].(map[string]any)
		Expect(ok).To(BeTrue(), "expected wizardState in GetPipeline response, got %#v", result["wizardState"])
		Expect(ws["kind"]).To(Equal("alloy-graph/v1"))
		nodes, ok := ws["nodes"].([]any)
		Expect(ok).To(BeTrue())
		Expect(nodes).To(HaveLen(1))
		node, ok := nodes[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(node["id"]).To(Equal("n1"))
		// Positions must round-trip exactly — this is also the regression
		// covered by "positions do not round-trip" (task item 4), same root
		// cause as B3(a).
		pos, ok := node["position"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(pos["x"]).To(Equal(-256.0))
		Expect(pos["y"]).To(Equal(-304.0))
	})

	It("refuses to read another org's pipeline through a caller-supplied org id", func() {
		// Tenant isolation: the authz interceptor only proves the caller may act on
		// the org NAMED IN THE REQUEST. Without an ownership check the caller could
		// pair their own org id with any pipeline id and read it. NotFound rather
		// than PermissionDenied so the response does not confirm it exists.
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          fmt.Sprintf("rpc-pipeline-other-%d", time.Now().UnixNano()),
			DisplayName:   "Other Org",
			AdminGroupID:  "admin-group",
			ReaderGroupID: pgtype.Text{},
		})
		Expect(err).NotTo(HaveOccurred())

		foreign, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: other.ID, Name: "foreign-pipeline", Contents: "// foreign",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "ui",
			WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		cookie := sessionCookie(true)
		for _, procedure := range []string{
			"/shepherd.mgmt.v1.PipelineService/GetPipeline",
			"/shepherd.mgmt.v1.PipelineService/DeletePipeline",
		} {
			resp := postConnect(procedure, map[string]any{
				"org_id": orgID, // the caller's own org, NOT the pipeline's
				"id":     foreign.ID.String(),
			}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusNotFound), "%s must not reach another org's pipeline", procedure)
		}

		// And it must still be there.
		stored, err := st.Queries.GetPipelineByID(ctx, foreign.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Name).To(Equal("foreign-pipeline"))
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
