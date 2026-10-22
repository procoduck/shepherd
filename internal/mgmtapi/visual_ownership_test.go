package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/version"
)

// #319 (maintainer-approved RBAC change, 2026-10-12): VisualService Render,
// Validate and UpgradeCheck are authorized PER PIPELINE when the request names
// one (pipeline_id) — anyone who may edit that pipeline, by the same check
// UpdatePipeline makes (requireWriteAuthorized + authorizePipelineOwnership) —
// and stay org editor for a new, unsaved graph (no pipeline_id). Simulate is
// unchanged: org editor, whatever team the caller is in.
var _ = Describe("#319: VisualService per-pipeline authorization", Label("integration"), func() {
	const (
		renderProc       = "/shepherd.mgmt.v1.VisualService/Render"
		validateProc     = "/shepherd.mgmt.v1.VisualService/Validate"
		upgradeCheckProc = "/shepherd.mgmt.v1.VisualService/UpgradeCheck"
	)
	visualProcs := []string{renderProc, validateProc, upgradeCheckProc}

	var (
		ctx                      context.Context
		cancel                   context.CancelFunc
		st                       *store.Store
		server                   *httptest.Server
		orgID                    pgtype.UUID
		ownedByA, ownedByB       sqlc.Pipeline
		unowned, otherOrgs       sqlc.Pipeline
		viewerInTeam, viewerOnly *http.Cookie
		editor, admin            *http.Cookie
	)

	graph := func() map[string]any {
		return map[string]any{
			"kind": "alloy-graph/v1", "schemaVersion": version.AlloySchemaVersion,
			"nodes": []map[string]any{{
				"id": "n1", "component": "prometheus.exporter.unix", "label": "unix",
				"position": map[string]any{"x": 0, "y": 0}, "props": map[string]any{},
			}},
		}
	}
	call := func(proc string, cookie *http.Cookie, pipelineID string) (int, map[string]any) {
		body := map[string]any{"orgId": orgID.String(), "graph": graph()}
		if pipelineID != "" {
			body["pipelineId"] = pipelineID
		}
		resp := g11PostConnect(server, proc, body, cookie)
		return resp.StatusCode, g11DecodeBody(resp)
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "vo-org", DisplayName: "VO Org", AdminGroupID: "vo-admin",
			EditorGroupID: pgtype.Text{String: "vo-editor", Valid: true},
			ViewerGroupID: pgtype.Text{String: "vo-viewer", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "vo-other", DisplayName: "VO Other", AdminGroupID: "vo-other-admin",
		})
		Expect(err).NotTo(HaveOccurred())

		teamA, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{OrgID: orgID, Name: "team-a", IdpGroupID: pgtype.Text{String: "vo-team-a", Valid: true}})
		Expect(err).NotTo(HaveOccurred())
		teamB, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{OrgID: orgID, Name: "team-b", IdpGroupID: pgtype.Text{String: "vo-team-b", Valid: true}})
		Expect(err).NotTo(HaveOccurred())

		mk := func(org pgtype.UUID, name string, owner pgtype.UUID) sqlc.Pipeline {
			p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org, Name: name, Source: "visual", Contents: "// " + name,
				Matchers: json.RawMessage(`["env=\"prod\""]`), OwnerTeamID: owner,
			})
			Expect(err).NotTo(HaveOccurred())
			return p
		}
		ownedByA = mk(orgID, "owned-by-a", teamA.ID)
		ownedByB = mk(orgID, "owned-by-b", teamB.ID)
		unowned = mk(orgID, "unowned", pgtype.UUID{})
		otherOrgs = mk(other.ID, "other-orgs", pgtype.UUID{})

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))

		viewerInTeam = g11Session(ctx, st, "vo-viewer-in-team", []string{"vo-viewer", "vo-team-a"})
		viewerOnly = g11Session(ctx, st, "vo-viewer", []string{"vo-viewer"})
		editor = g11Session(ctx, st, "vo-editor", []string{"vo-editor"})
		admin = g11Session(ctx, st, "vo-admin", []string{"vo-admin"})
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("admits a team-member viewer for their team's pipeline, and renders it", func() {
		for _, proc := range visualProcs {
			status, body := call(proc, viewerInTeam, ownedByA.ID.String())
			Expect(status).To(Equal(http.StatusOK), "%s: %v", proc, body)
		}
		_, body := call(renderProc, viewerInTeam, ownedByA.ID.String())
		Expect(body["content"]).To(ContainSubstring(`prometheus.exporter.unix "unix"`))
	})

	It("refuses a team-member viewer on another team's pipeline and on an unowned one", func() {
		for _, proc := range visualProcs {
			for _, p := range []sqlc.Pipeline{ownedByB, unowned} {
				status, body := call(proc, viewerInTeam, p.ID.String())
				Expect(status).To(Equal(http.StatusForbidden), "%s on %s: %v", proc, p.Name, body)
				Expect(body["code"]).To(Equal("permission_denied"))
			}
		}
	})

	It("keeps a new graph (no pipeline_id) org editor: a team-member viewer is refused, an editor admitted", func() {
		for _, proc := range visualProcs {
			status, body := call(proc, viewerInTeam, "")
			Expect(status).To(Equal(http.StatusForbidden), "%s: %v", proc, body)
			Expect(body["code"]).To(Equal("permission_denied"))
			status, body = call(proc, editor, "")
			Expect(status).To(Equal(http.StatusOK), "%s: %v", proc, body)
		}
	})

	It("refuses a plain viewer everywhere", func() {
		for _, proc := range visualProcs {
			for _, id := range []string{ownedByA.ID.String(), unowned.ID.String(), ""} {
				status, body := call(proc, viewerOnly, id)
				Expect(status).To(Equal(http.StatusForbidden), "%s on %q: %v", proc, id, body)
			}
		}
	})

	It("admits an org editor and an org admin on every pipeline, owned or not", func() {
		for _, c := range []*http.Cookie{editor, admin} {
			for _, proc := range visualProcs {
				for _, p := range []sqlc.Pipeline{ownedByA, ownedByB, unowned} {
					status, body := call(proc, c, p.ID.String())
					Expect(status).To(Equal(http.StatusOK), "%s on %s: %v", proc, p.Name, body)
				}
			}
		}
	})

	It("answers not_found for a pipeline in another org, and invalid_argument for a malformed id", func() {
		for _, proc := range visualProcs {
			status, body := call(proc, editor, otherOrgs.ID.String())
			Expect(status).To(Equal(http.StatusNotFound), "%s: %v", proc, body)
			status, body = call(proc, editor, "not-a-uuid")
			Expect(status).To(Equal(http.StatusBadRequest), "%s: %v", proc, body)
		}
	})

	It("agrees with Pipeline.can_edit for every persona and pipeline", func() {
		personas := map[string]*http.Cookie{
			"viewer-in-team": viewerInTeam, "viewer": viewerOnly, "editor": editor, "admin": admin,
		}
		for who, cookie := range personas {
			for _, p := range []sqlc.Pipeline{ownedByA, ownedByB, unowned} {
				resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/GetPipeline", map[string]any{
					"orgId": orgID.String(), "id": p.ID.String(),
				}, cookie)
				got := g11DecodeBody(resp)
				Expect(resp.StatusCode).To(Equal(http.StatusOK))
				predicted := got["canEdit"] == true
				status, _ := call(renderProc, cookie, p.ID.String())
				Expect(status == http.StatusOK).To(Equal(predicted),
					"%s on %s: can_edit=%v, Render status %d", who, p.Name, predicted, status)
			}
		}
	})

	It("leaves Simulate org editor: a team-member viewer is refused", func() {
		for proc, body := range map[string]map[string]any{
			"/shepherd.mgmt.v1.SimulateService/SimulateRelabel": {},
			"/shepherd.mgmt.v1.SimulateService/SimulateLogs":    {},
			"/shepherd.mgmt.v1.SimulateService/CreateRun":       {},
		} {
			body["orgId"] = orgID.String()
			resp := g11PostConnect(server, proc, body, viewerInTeam)
			got := g11DecodeBody(resp)
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden), "%s: %v", proc, got)
			Expect(got["code"]).To(Equal("permission_denied"))
		}
	})

	It("holds a service account to the UpdatePipeline check only when it names a pipeline", func() {
		proposeSecret, proposeID := g12MakeServiceAccount(ctx, st, orgID, "vo-propose", "propose")
		applySecret, applyID := g12MakeServiceAccount(ctx, st, orgID, "vo-apply", "apply")
		post := func(id, secret, onBehalfOf, pipelineID string) int {
			body := map[string]any{"orgId": orgID.String(), "graph": graph()}
			if pipelineID != "" {
				body["pipelineId"] = pipelineID
			}
			resp := g12PostConnect(server, renderProc, body, id, secret, onBehalfOf)
			_ = g11DecodeBody(resp)
			return resp.StatusCode
		}
		// A new graph: unchanged from before #319 — an editor-tier token of
		// either capability renders.
		Expect(post(proposeID, proposeSecret, "", "")).To(Equal(http.StatusOK))
		// A named pipeline: the write path's own gate — apply capability and a
		// verified on-behalf-of.
		Expect(post(proposeID, proposeSecret, "", unowned.ID.String())).To(Equal(http.StatusForbidden))
		Expect(post(applyID, applySecret, g12DelegatedPrincipal, unowned.ID.String())).To(Equal(http.StatusOK))
	})
})
