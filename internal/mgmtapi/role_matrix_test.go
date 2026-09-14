package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// role_matrix_test.go proves the org-editor rung is CAPPED over Connect —
// distinct from every pre-existing denial test, which uses a reader
// session and so cannot tell "requires more than editor" apart from
// "requires more than reader" (rpc_wizard_test.go's "denies CommitWizard
// for a session without org-editor access" spec, for instance, only proves
// a reader is denied — an editor session was never tried against an
// org-admin-only procedure until this file). It also pins CSRF enforcement
// on the Connect surface.
//
// These are coverage tests for controls that already exist
// (rpc_interceptor.go's procedureRequirements): each editor-denied case
// below was red-run once by temporarily changing its target procedure's
// entry to auth.RoleOrgEditor and confirming the matching case failed,
// then reverting — see the per-Entry comment for the exact line changed.
// There is no behavior change in rpc_interceptor.go in this commit.
var _ = Describe("Connect role matrix: org-editor rung, CSRF", Label("integration"), func() {
	var (
		ctx          context.Context
		cancel       context.CancelFunc
		st           *store.Store
		server       *httptest.Server
		orgID        string
		editorCookie *http.Cookie
		readerCookie *http.Cookie
		// roleMatrixPipelineID backs the PipelineService/RestoreRevision
		// Entry below — see pipelineIDPlaceholder's comment.
		roleMatrixPipelineID string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "role-matrix-org", DisplayName: "Role Matrix Org",
			AdminGroupID:  "role-matrix-admin-grp",
			EditorGroupID: pgtype.Text{String: "role-matrix-editor-grp", Valid: true},
			ReaderGroupID: pgtype.Text{String: "role-matrix-reader-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		editorCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "role-matrix-editor-grp")}
		readerCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "role-matrix-reader-grp")}

		// Unowned pipeline for the PipelineService/RestoreRevision Entry
		// below — created directly (not over HTTP) since no revision history
		// is needed: authorizeOwnership refuses the reader before
		// RestoreRevision ever looks one up.
		rmp, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "role-matrix-restore-target", Contents: "// x\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "ui", CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		roleMatrixPipelineID = rmp.ID.String()

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// An org editor is capped below every org-admin-only procedure that
	// decides what the org IS (as opposed to what it runs). Mutation-run
	// red proof for each: temporarily changing the named
	// procedureRequirements entry in rpc_interceptor.go to
	// auth.RoleOrgEditor made the matching case below fail (403 became
	// 200/other), confirming the assertion actually exercises that entry;
	// reverted before commit.
	DescribeTable("an org editor is refused permission_denied on org-admin-only writes",
		func(procedure string, extraFields map[string]any) {
			body := map[string]any{"org_id": orgID}
			for k, v := range extraFields {
				body[k] = v
			}
			resp := postConnectJSON(server, procedure, editorCookie, body)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
			Expect(connectErrorCode(resp)).To(Equal("permission_denied"))
		},
		// Mutation run: rpc_interceptor.go:106
		// (DestinationServiceCreateDestinationProcedure) -> auth.RoleOrgEditor.
		Entry("DestinationService/CreateDestination", "/shepherd.mgmt.v1.DestinationService/CreateDestination",
			map[string]any{"name": "d", "type": "prometheus", "url": "http://prom:9090"}),
		// Mutation run: rpc_interceptor.go (TenantRouteServiceCreateTenantRouteProcedure) -> auth.RoleOrgEditor.
		Entry("TenantRouteService/CreateTenantRoute", "/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute",
			map[string]any{"kind": "otlp", "gateway_mode": "managed", "gateway_name": "shepherd-receiver-gw"}),
		// Mutation run: rpc_interceptor.go (GitOpsServiceCreateCredentialProcedure) -> auth.RoleOrgEditor.
		Entry("GitOpsService/CreateCredential", "/shepherd.mgmt.v1.GitOpsService/CreateCredential",
			map[string]any{"name": "cred", "kind": "pat", "client_secret": "s"}),
		// Mutation run: rpc_interceptor.go (TeamServiceCreateTeamProcedure) -> auth.RoleOrgEditor.
		Entry("TeamService/CreateTeam", "/shepherd.mgmt.v1.TeamService/CreateTeam",
			map[string]any{"name": "team", "idp_group_id": "grp"}),
	)

	// pipelineIDPlaceholder in an Entry's extraFields is substituted with a
	// pipeline created fresh in THIS Describe's BeforeEach (see below) —
	// Entry() arguments are evaluated once, at spec-tree construction, long
	// before BeforeEach ever runs, so the real (DB-generated) id cannot be
	// embedded in the table literal directly.
	const pipelineIDPlaceholder = "$ROLE_MATRIX_PIPELINE_ID$"

	// A viewer (org-reader) is denied every org-editor-gated authoring
	// procedure — WizardService, VisualService, SimulateService all sit at
	// auth.RoleOrgEditor, strictly above reader. These are pinning
	// duplicates of coverage that already exists per-service
	// (rpc_wizard_test.go, rpc_simulate_test.go, visual_rest_test.go);
	// gathered here so the whole editor-floor shape is visible in one
	// table.
	DescribeTable("an org reader (viewer) is refused permission_denied on org-editor-gated authoring procedures",
		func(procedure string, extraFields map[string]any) {
			body := map[string]any{"org_id": orgID}
			for k, v := range extraFields {
				if v == pipelineIDPlaceholder {
					v = roleMatrixPipelineID
				}
				body[k] = v
			}
			resp := postConnectJSON(server, procedure, readerCookie, body)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
			Expect(connectErrorCode(resp)).To(Equal("permission_denied"))
		},
		Entry("WizardService/CommitWizard", "/shepherd.mgmt.v1.WizardService/CommitWizard",
			map[string]any{"kind": "app-observability", "name": "n", "state": map[string]any{}}),
		Entry("VisualService/Validate", "/shepherd.mgmt.v1.VisualService/Validate", map[string]any{"graph": map[string]any{}}),
		Entry("SimulateService/SimulateRelabel", "/shepherd.mgmt.v1.SimulateService/SimulateRelabel",
			map[string]any{"rules": []any{}, "sample_targets": []any{}}),
		// F-REVISIONS backend: RestoreRevision's interceptor row is the same
		// auth.RoleOrgReader gate UpdatePipeline uses, with authorizeOwnership
		// (org-editor-or-above for an unowned pipeline) doing the fine-grained
		// refusal — see rpc_revisions_test.go for the full editor-succeeds /
		// reader-refused pair this mirrors. authorizeOwnership runs before the
		// revision is even looked up, so a real pipeline id with no revision
		// history still proves the refusal. Mutation run: replacing
		// authorizeOwnership's body with `return nil` in rpc_pipeline.go made
		// this Entry fail (200 instead of 403); reverted before commit.
		Entry("PipelineService/RestoreRevision", "/shepherd.mgmt.v1.PipelineService/RestoreRevision",
			map[string]any{"id": pipelineIDPlaceholder, "revision": 1}),
	)

	It("refuses a mutating Connect request that omits the CSRF header", func() {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/shepherd.mgmt.v1.DestinationService/CreateDestination",
			nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		// Deliberately NOT setting X-Requested-With — every other helper in
		// this package (postConnectJSON, postJSON) always sets it.
		req.AddCookie(editorCookie)
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})

	// W3-5: SessionMiddleware rejects a session whose id_token_expires has
	// passed even though expires_at (the session-row TTL) has not (D7).
	It("rejects a session whose ID token has expired even though expires_at has not", func() {
		idExpired := pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
		sessID := "role-matrix-id-token-expired-sess"
		groupsJSON := []byte(`["role-matrix-editor-grp"]`)
		_, err := st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessID, UserOid: "user-" + sessID, Email: sessID + "@example.com", DisplayName: "Expired ID Token",
			GroupIds:       groupsJSON,
			IsAppAdmin:     false,
			IDTokenExpires: idExpired,
			ExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:         "oidc",
		})
		Expect(err).NotTo(HaveOccurred())
		expiredCookie := &http.Cookie{Name: "shepherd_session", Value: sessID}

		resp := postConnectJSON(server, "/shepherd.mgmt.v1.MeService/GetMe", expiredCookie, map[string]any{})
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})
})
