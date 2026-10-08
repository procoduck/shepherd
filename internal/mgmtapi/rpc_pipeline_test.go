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

	It("rejects a pipeline name that carries a control character, on create and on update", func() {
		// The served config's header writes the name into a "// " comment
		// line; merge.buildHeader now neutralises line breaks, but the API
		// should not accept them in the first place — a name is a label, not
		// a text field. Cc (control) and Cf (format, e.g. zero-width joiners)
		// are refused; ordinary Unicode letters stay allowed.
		cookie := sessionCookie(true)
		for _, bad := range []string{"line\nbreak", "cr\rbreak", "tab\there", "zero\u200bwidth", "bell\x07"} {
			resp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
				"org_id": orgID, "name": bad, "contents": `// valid alloy comment`, "matchers": []string{},
			}, cookie)
			var payload struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			decodeBody(resp, &payload)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), "name %q must be refused", bad)
			Expect(payload.Code).To(Equal("invalid_argument"), "name %q", bad)
			Expect(payload.Message).To(ContainSubstring("control"), "name %q", bad)
		}

		// Unicode letters are fine.
		resp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "métriques-ñ", "contents": `// valid alloy comment`, "matchers": []string{},
		}, cookie)
		var created map[string]any
		decodeBody(resp, &created)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		// The same rule guards UpdatePipeline: the name cannot be renamed into a bad one.
		resp = postConnect("/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"org_id": orgID, "id": created["id"], "name": "renamed\nbad", "contents": `// valid alloy comment`, "matchers": []string{},
		}, cookie)
		var payload struct {
			Code string `json:"code"`
		}
		decodeBody(resp, &payload)
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(payload.Code).To(Equal("invalid_argument"))
	})

	// #233: `alloy validate` accepts a list-of-lists targets wire that Alloy
	// refuses at load. The save path must refuse it and store nothing.
	It("refuses to create a pipeline whose targets wire is a list of lists", func() {
		cookie := sessionCookie(true)
		resp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "list-of-lists", "contents": issue233Contents, "matchers": []string{},
		}, cookie)
		var payload struct {
			Code string `json:"code"`
		}
		decodeBody(resp, &payload)
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(payload.Code).To(Equal("failed_precondition"))

		pipes, err := st.Queries.ListPipelinesByOrg(ctx, mustUUID(orgID))
		Expect(err).NotTo(HaveOccurred())
		Expect(pipes).To(BeEmpty(), "a refused save must store nothing")

		// The corrected wire saves.
		fixed := strings.Replace(issue233Contents, "[discovery.kubernetes.pods.targets]", "discovery.kubernetes.pods.targets", 1)
		resp = postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "list-of-lists", "contents": fixed, "matchers": []string{},
		}, cookie)
		var created map[string]any
		decodeBody(resp, &created)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
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

	// W2-S7b red run: loadPipeline used to treat ANY GetPipelineByID failure
	// as a missing row (blanket not_found), so a real lookup failure (a
	// connection error, a canceled backend, anything that is not
	// pgx.ErrNoRows) was misreported as 404 instead of the 500 it actually
	// is. Mirrors rpc_destination_test.go's "maps a real lookup failure on
	// GetDestination to Internal, not a false Not Found".
	It("maps a real lookup failure on GetPipeline to Internal, not a false Not Found", func() {
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "lookup-fault-pipe", Contents: "// v1\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "ui",
			WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		cookie := sessionCookie(true)

		// Hold an ACCESS EXCLUSIVE lock on pipelines from a separate connection
		// so loadPipeline's plain SELECT (which a row-level FOR UPDATE lock
		// cannot block) blocks on it, then cancel that backend -- forcing a
		// real, non-ErrNoRows failure deterministically, without a
		// fault-injection seam in store.go.
		lockConn, err := st.Pool().Acquire(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer lockConn.Release()
		lockTx, err := lockConn.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = lockTx.Rollback(ctx) }() //nolint:errcheck // best-effort cleanup; explicit Rollback below is the real one
		_, err = lockTx.Exec(ctx, `LOCK TABLE pipelines IN ACCESS EXCLUSIVE MODE`)
		Expect(err).NotTo(HaveOccurred())

		respCh := make(chan *http.Response, 1)
		go func() {
			defer GinkgoRecover()
			respCh <- postConnect("/shepherd.mgmt.v1.PipelineService/GetPipeline", map[string]any{
				"org_id": orgID, "id": p.ID.String(),
			}, cookie)
		}()

		var pid int
		Eventually(func() error {
			return st.Pool().QueryRow(ctx,
				`SELECT pid FROM pg_stat_activity
				 WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%GetPipelineByID%'`,
			).Scan(&pid)
		}, "5s", "20ms").Should(Succeed(), "GetPipeline's lookup never blocked on the held table lock")
		_, err = st.Pool().Exec(ctx, `SELECT pg_cancel_backend($1)`, pid)
		Expect(err).NotTo(HaveOccurred())

		resp := <-respCh
		var payload struct {
			Code string `json:"code"`
		}
		decodeBody(resp, &payload)
		Expect(payload.Code).To(Equal("internal"), "a real lookup failure must not be reported as not_found")

		Expect(lockTx.Rollback(ctx)).To(Succeed())
	})

	// procoduck/shepherd#139 Phase 1: PreviewMatches must reproduce today's
	// {cluster, role}-only matching byte-for-byte until the org opts in, then
	// pick up admin labels once it does — the actual end-to-end proof that
	// allow_label_matching gates previewMatchedCollectors's admin-label wiring,
	// not just that BuildCollectorLabels behaves correctly in isolation.
	It("PreviewMatches ignores admin labels until allow_label_matching is on, then honors them (#139)", func() {
		cookie := sessionCookie(true)

		cluster, err := st.Queries.UpsertCluster(ctx, "preview-matches-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		setLabelResp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID, "collectorId": collector.ID.String(), "key": "team", "value": "platform",
		}, cookie)
		Expect(setLabelResp.StatusCode).To(Equal(http.StatusOK))

		createResp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "team-only-pipe", "contents": `// valid alloy comment`,
			"matchers": []string{`team="platform"`},
		}, cookie)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var created struct {
			ID string `json:"id"`
		}
		decodeBody(createResp, &created)

		preview := func() []any {
			resp := postConnect("/shepherd.mgmt.v1.PipelineService/PreviewMatches", map[string]any{
				"org_id": orgID, "id": created.ID,
			}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var payload struct {
				Collectors []any `json:"collectors"`
			}
			decodeBody(resp, &payload)
			return payload.Collectors
		}

		Expect(preview()).To(BeEmpty(), "flag off: a custom-label-only matcher must match nothing, same as before #139")

		updateResp := postConnect("/shepherd.mgmt.v1.AdminService/UpdateOrg", map[string]any{
			"orgId": orgID, "displayName": "RPC Pipeline Org", "adminGroupId": "admin-group", "allowLabelMatching": true,
		}, cookie)
		Expect(updateResp.StatusCode).To(Equal(http.StatusOK))

		Expect(preview()).To(HaveLen(1), "flag on: the collector's admin label must now participate in matching")
	})

	// PreviewMatches never set merge.Pipeline.RepoLinkCollectorID, which
	// merge.MatchesPipeline compares for Source == "git", so the preview of any
	// git-sourced pipeline was empty. The serving path (internal/agentapi) was
	// already correct; only the preview was wrong.
	It("PreviewMatches returns a git-sourced pipeline's real linked collector, not zero", func() {
		cookie := sessionCookie(true)

		cluster, err := st.Queries.UpsertCluster(ctx, "preview-git-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
			OrgID: orgUUID(orgID), Name: "preview-git-cred", Kind: "pat",
			Username:        pgtype.Text{String: "git", Valid: true},
			ClientSecretEnc: []byte("enc"),
			ProviderConfig:  json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())
		link, err := st.Queries.CreateRepoLink(ctx, sqlc.CreateRepoLinkParams{
			OrgID: orgUUID(orgID), CollectorID: collector.ID, CredentialID: cred.ID,
			RepoUrl: "https://example.invalid/team/configs.git", Branch: "main", Path: "/",
		})
		Expect(err).NotTo(HaveOccurred())

		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "preview-git-pipe", Contents: `// git-sourced`,
			Matchers: json.RawMessage(`[]`), // git pipelines carry no matchers by design
			Enabled:  true, Source: "git",
			WizardState: json.RawMessage(`{}`), CreatedBy: "gitsync", UpdatedBy: "gitsync",
			RepoLinkID: link.ID,
			GitPath:    pgtype.Text{String: "/preview-git-pipe.alloy", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())

		resp := postConnect("/shepherd.mgmt.v1.PipelineService/PreviewMatches", map[string]any{
			"org_id": orgID, "id": p.ID.String(),
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var payload struct {
			Collectors []struct {
				ID string `json:"id"`
			} `json:"collectors"`
		}
		decodeBody(resp, &payload)
		Expect(payload.Collectors).To(HaveLen(1), "must return the pipeline's real linked collector, not zero")
		Expect(payload.Collectors[0].ID).To(Equal(collector.ID.String()))
	})

	// Walkthrough finding M2: a pipeline saved through the editor carries
	// whatever matchers the operator typed — nothing forces a role matcher —
	// so a cluster-only matcher can select a collector whose role refuses the
	// pipeline's signals. Gate G6 then leaves it out of that collector's
	// served config; the preview must say so per collector, from the same
	// merge.RoleExclusion check the served config is assembled with.
	It("PreviewMatches marks a matched collector whose role excludes the pipeline's signals (M2)", func() {
		cookie := sessionCookie(true)

		cluster, err := st.Queries.UpsertCluster(ctx, "m2-prod")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		logsCollector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "logs"})
		Expect(err).NotTo(HaveOccurred())
		metricsCollector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		createResp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "m2-logs-pipe",
			"contents": `loki.write "default" {
  endpoint {
    url = "http://loki:3100/loki/api/v1/push"
  }
}
`,
			"matchers": []string{`cluster="m2-prod"`},
		}, cookie)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var created struct {
			ID string `json:"id"`
		}
		decodeBody(createResp, &created)

		resp := postConnect("/shepherd.mgmt.v1.PipelineService/PreviewMatches", map[string]any{
			"org_id": orgID, "id": created.ID,
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var payload struct {
			Collectors []struct {
				ID             string `json:"id"`
				Role           string `json:"role"`
				ExcludedReason string `json:"excludedReason"`
			} `json:"collectors"`
		}
		decodeBody(resp, &payload)
		Expect(payload.Collectors).To(HaveLen(2), "a cluster-only matcher selects both collectors")

		reasons := map[string]string{}
		for _, c := range payload.Collectors {
			reasons[c.ID] = c.ExcludedReason
		}
		Expect(reasons).To(HaveKeyWithValue(logsCollector.ID.String(), ""),
			"a logs collector serves a logs pipeline: nothing to report")
		Expect(reasons).To(HaveKeyWithValue(metricsCollector.ID.String(),
			"its signals (logs) are not allowed on role metrics"))
	})

	// Red run that caught a real gap: recomputeOrgCaches (the eager
	// background recompute EnablePipeline/DisablePipeline/UpdatePipeline/
	// DeletePipeline kick off via `go s.recomputeOrgCaches(...)`) built its
	// own serve.Collector{} without AdminLabels, bypassing every other fix
	// in this PR. PreviewMatches's own gating test above did not catch it
	// because it exercises a different code path (previewMatchedCollectors,
	// not recomputeOrgCaches) — this test exercises the actual
	// EnablePipeline -> GetServedConfig path a real collector's poll would
	// hit.
	It("EnablePipeline's eager recompute honors admin labels once allow_label_matching is on (#139)", func() {
		cookie := sessionCookie(true)

		cluster, err := st.Queries.UpsertCluster(ctx, "recompute-org-caches-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		setLabelResp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID, "collectorId": collector.ID.String(), "key": "team", "value": "platform",
		}, cookie)
		Expect(setLabelResp.StatusCode).To(Equal(http.StatusOK))

		updateResp := postConnect("/shepherd.mgmt.v1.AdminService/UpdateOrg", map[string]any{
			"orgId": orgID, "displayName": "RPC Pipeline Org", "adminGroupId": "admin-group", "allowLabelMatching": true,
		}, cookie)
		Expect(updateResp.StatusCode).To(Equal(http.StatusOK))

		createResp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "recompute-org-caches-pipe", "contents": `// recompute-org-caches-marker`,
			"matchers": []string{`team="platform"`},
		}, cookie)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var created struct {
			ID string `json:"id"`
		}
		decodeBody(createResp, &created)

		enableResp := postConnect("/shepherd.mgmt.v1.PipelineService/EnablePipeline", map[string]any{
			"org_id": orgID, "id": created.ID,
		}, cookie)
		Expect(enableResp.StatusCode).To(Equal(http.StatusOK))

		// recomputeOrgCaches runs in a detached goroutine, so this must poll
		// rather than assert immediately.
		Eventually(func() string {
			resp := postConnect("/shepherd.mgmt.v1.FleetService/GetServedConfig", map[string]any{
				"orgId": orgID, "id": collector.ID.String(),
			}, cookie)
			var payload struct {
				Content string `json:"content"`
			}
			decodeBody(resp, &payload)
			return payload.Content
		}, "5s", "50ms").Should(ContainSubstring("recompute-org-caches-marker"),
			"the admin-label-matched pipeline must reach this collector's served config once allow_label_matching is on")
	})

	// PR-8b: the local_attributes half of the same gate, using
	// allow_local_attribute_matching independently of allow_label_matching —
	// the two-flag split exists specifically so agent-reported data (reachable
	// via a compromised agent token) never gates identically to admin-set
	// data. local_attributes is agent-reported, so it's seeded directly via
	// UpsertCollectorInstance (mirrors internal/store/local_attributes_scale_test.go)
	// rather than through a mgmtapi RPC.
	It("PreviewMatches ignores local_attributes until allow_local_attribute_matching is on, then honors them (#139)", func() {
		cookie := sessionCookie(true)

		cluster, err := st.Queries.UpsertCluster(ctx, "preview-matches-local-attrs-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		_, err = st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
			ID: "preview-matches-local-attrs-instance", CollectorID: collector.ID, Name: "singleton",
			LocalAttributes: json.RawMessage(`{"team":"platform"}`),
		})
		Expect(err).NotTo(HaveOccurred())

		createResp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "team-only-local-attrs-pipe", "contents": `// valid alloy comment`,
			"matchers": []string{`team="platform"`},
		}, cookie)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var created struct {
			ID string `json:"id"`
		}
		decodeBody(createResp, &created)

		preview := func() []any {
			resp := postConnect("/shepherd.mgmt.v1.PipelineService/PreviewMatches", map[string]any{
				"org_id": orgID, "id": created.ID,
			}, cookie)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var payload struct {
				Collectors []any `json:"collectors"`
			}
			decodeBody(resp, &payload)
			return payload.Collectors
		}

		Expect(preview()).To(BeEmpty(), "flag off: a custom-local-attribute-only matcher must match nothing, same as before #139")

		updateResp := postConnect("/shepherd.mgmt.v1.AdminService/UpdateOrg", map[string]any{
			"orgId": orgID, "displayName": "RPC Pipeline Org", "adminGroupId": "admin-group", "allowLocalAttributeMatching": true,
		}, cookie)
		Expect(updateResp.StatusCode).To(Equal(http.StatusOK))

		Expect(preview()).To(HaveLen(1), "flag on: the collector's reported local_attributes must now participate in matching")
	})

	// Same red-run shape as the admin-labels recomputeOrgCaches test above —
	// PreviewMatches exercises previewMatchedCollectors, not recomputeOrgCaches,
	// so it alone would not have caught recomputeOrgCaches missing LocalAttrs.
	It("EnablePipeline's eager recompute honors local_attributes once allow_local_attribute_matching is on (#139)", func() {
		cookie := sessionCookie(true)

		cluster, err := st.Queries.UpsertCluster(ctx, "recompute-org-caches-local-attrs-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		_, err = st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
			ID: "recompute-org-caches-local-attrs-instance", CollectorID: collector.ID, Name: "singleton",
			LocalAttributes: json.RawMessage(`{"team":"platform"}`),
		})
		Expect(err).NotTo(HaveOccurred())

		updateResp := postConnect("/shepherd.mgmt.v1.AdminService/UpdateOrg", map[string]any{
			"orgId": orgID, "displayName": "RPC Pipeline Org", "adminGroupId": "admin-group", "allowLocalAttributeMatching": true,
		}, cookie)
		Expect(updateResp.StatusCode).To(Equal(http.StatusOK))

		createResp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "recompute-org-caches-local-attrs-pipe", "contents": `// recompute-org-caches-local-attrs-marker`,
			"matchers": []string{`team="platform"`},
		}, cookie)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var created struct {
			ID string `json:"id"`
		}
		decodeBody(createResp, &created)

		enableResp := postConnect("/shepherd.mgmt.v1.PipelineService/EnablePipeline", map[string]any{
			"org_id": orgID, "id": created.ID,
		}, cookie)
		Expect(enableResp.StatusCode).To(Equal(http.StatusOK))

		// recomputeOrgCaches runs in a detached goroutine, so this must poll
		// rather than assert immediately.
		Eventually(func() string {
			resp := postConnect("/shepherd.mgmt.v1.FleetService/GetServedConfig", map[string]any{
				"orgId": orgID, "id": collector.ID.String(),
			}, cookie)
			var payload struct {
				Content string `json:"content"`
			}
			decodeBody(resp, &payload)
			return payload.Content
		}, "5s", "50ms").Should(ContainSubstring("recompute-org-caches-local-attrs-marker"),
			"the local-attribute-matched pipeline must reach this collector's served config once allow_local_attribute_matching is on")
	})

	// Precedence: admin label must win over a same-key local attribute, since
	// local_attributes is agent-reported (reachable via a compromised agent
	// token) and must never be allowed to shadow an admin-set label — the
	// same precedence merge.BuildCollectorLabels enforces in isolation,
	// proved here end-to-end through both gates at once.
	It("admin label wins over a same-key local attribute when both flags are on (#139)", func() {
		cookie := sessionCookie(true)

		cluster, err := st.Queries.UpsertCluster(ctx, "precedence-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		_, err = st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
			ID: "precedence-instance", CollectorID: collector.ID, Name: "singleton",
			LocalAttributes: json.RawMessage(`{"team":"agent-reported"}`),
		})
		Expect(err).NotTo(HaveOccurred())

		setLabelResp := postConnect("/shepherd.mgmt.v1.FleetService/SetCollectorLabel", map[string]any{
			"orgId": orgID, "collectorId": collector.ID.String(), "key": "team", "value": "admin-set",
		}, cookie)
		Expect(setLabelResp.StatusCode).To(Equal(http.StatusOK))

		updateResp := postConnect("/shepherd.mgmt.v1.AdminService/UpdateOrg", map[string]any{
			"orgId": orgID, "displayName": "RPC Pipeline Org", "adminGroupId": "admin-group",
			"allowLabelMatching": true, "allowLocalAttributeMatching": true,
		}, cookie)
		Expect(updateResp.StatusCode).To(Equal(http.StatusOK))

		createResp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": orgID, "name": "precedence-pipe", "contents": `// valid alloy comment`,
			"matchers": []string{`team="admin-set"`},
		}, cookie)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var created struct {
			ID string `json:"id"`
		}
		decodeBody(createResp, &created)

		resp := postConnect("/shepherd.mgmt.v1.PipelineService/PreviewMatches", map[string]any{
			"org_id": orgID, "id": created.ID,
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var payload struct {
			Collectors []any `json:"collectors"`
		}
		decodeBody(resp, &payload)
		Expect(payload.Collectors).To(HaveLen(1),
			"a matcher on the admin label's value must match: admin label must win over the same-key local attribute")
	})
})
