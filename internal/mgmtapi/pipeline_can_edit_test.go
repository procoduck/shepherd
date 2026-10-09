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
)

// F3 (2026-10-09 walkthrough): an org viewer on the owning team could write
// the pipeline over the API, but the UI — gated on the org role alone — showed
// it read-only. Pipeline.can_edit carries the server's own answer, computed by
// the ownership check the write paths run; these specs pin both that answer
// per persona and that it agrees with what UpdatePipeline actually does.
var _ = Describe("F3: Pipeline.can_edit", Label("integration"), func() {
	var (
		ctx                      context.Context
		cancel                   context.CancelFunc
		st                       *store.Store
		server                   *httptest.Server
		orgID                    pgtype.UUID
		ownedByA, unowned        sqlc.Pipeline
		ownedByB                 sqlc.Pipeline
		viewerInTeam, viewerOnly *http.Cookie
		editor, admin            *http.Cookie
	)

	canEdit := func(body map[string]any) bool {
		v, ok := body["canEdit"]
		return ok && v == true
	}
	get := func(cookie *http.Cookie, id pgtype.UUID) map[string]any {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/GetPipeline", map[string]any{
			"orgId": orgID.String(), "id": id.String(),
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		return g11DecodeBody(resp)
	}
	listCanEdit := func(cookie *http.Cookie) map[string]bool {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/ListPipelines", map[string]any{
			"orgId": orgID.String(),
		}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		body := g11DecodeBody(resp)
		out := map[string]bool{}
		items, ok := body["items"].([]any)
		Expect(ok).To(BeTrue(), "items: %v", body)
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			Expect(ok).To(BeTrue())
			name, ok := item["name"].(string)
			Expect(ok).To(BeTrue())
			out[name] = canEdit(item)
		}
		return out
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "f3-org", DisplayName: "F3 Org", AdminGroupID: "f3-admin",
			EditorGroupID: pgtype.Text{String: "f3-editor", Valid: true},
			ReaderGroupID: pgtype.Text{String: "f3-reader", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		teamA, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{OrgID: orgID, Name: "team-a", IdpGroupID: pgtype.Text{String: "f3-team-a", Valid: true}})
		Expect(err).NotTo(HaveOccurred())
		teamB, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{OrgID: orgID, Name: "team-b", IdpGroupID: pgtype.Text{String: "f3-team-b", Valid: true}})
		Expect(err).NotTo(HaveOccurred())

		mk := func(name string, owner pgtype.UUID) sqlc.Pipeline {
			p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: orgID, Name: name, Source: "ui", Contents: "// " + name,
				Matchers: json.RawMessage(`["env=\"prod\""]`), OwnerTeamID: owner,
			})
			Expect(err).NotTo(HaveOccurred())
			return p
		}
		ownedByA = mk("owned-by-a", teamA.ID)
		ownedByB = mk("owned-by-b", teamB.ID)
		unowned = mk("unowned", pgtype.UUID{})

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))

		viewerInTeam = g11Session(ctx, st, "f3-viewer-in-team", []string{"f3-reader", "f3-team-a"})
		viewerOnly = g11Session(ctx, st, "f3-viewer", []string{"f3-reader"})
		editor = g11Session(ctx, st, "f3-editor", []string{"f3-editor"})
		admin = g11Session(ctx, st, "f3-admin", []string{"f3-admin"})
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("is true on Get and List for an org viewer on the owning team, for that team's pipeline only", func() {
		Expect(canEdit(get(viewerInTeam, ownedByA.ID))).To(BeTrue())
		Expect(canEdit(get(viewerInTeam, ownedByB.ID))).To(BeFalse())
		Expect(canEdit(get(viewerInTeam, unowned.ID))).To(BeFalse())
		Expect(listCanEdit(viewerInTeam)).To(Equal(map[string]bool{
			"owned-by-a": true, "owned-by-b": false, "unowned": false,
		}))
	})

	It("is false everywhere for an org viewer on no team", func() {
		Expect(canEdit(get(viewerOnly, ownedByA.ID))).To(BeFalse())
		Expect(listCanEdit(viewerOnly)).To(Equal(map[string]bool{
			"owned-by-a": false, "owned-by-b": false, "unowned": false,
		}))
	})

	It("is true everywhere for an org editor and an org admin, owned or not", func() {
		for _, c := range []*http.Cookie{editor, admin} {
			Expect(canEdit(get(c, unowned.ID))).To(BeTrue())
			Expect(listCanEdit(c)).To(Equal(map[string]bool{
				"owned-by-a": true, "owned-by-b": true, "unowned": true,
			}))
		}
	})

	It("rides on the write responses too", func() {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": ownedByA.ID.String(), "name": "owned-by-a",
			"contents": "// edited by the team", "matchers": []string{`env="prod"`},
		}, viewerInTeam)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(canEdit(g11DecodeBody(resp))).To(BeTrue())

		resp = g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/DisablePipeline", map[string]any{
			"orgId": orgID.String(), "id": ownedByA.ID.String(),
		}, viewerInTeam)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(canEdit(g11DecodeBody(resp))).To(BeTrue())
	})

	It("predicts exactly what UpdatePipeline allows, for every persona and pipeline", func() {
		personas := map[string]*http.Cookie{
			"viewer-in-team": viewerInTeam, "viewer": viewerOnly, "editor": editor, "admin": admin,
		}
		for who, cookie := range personas {
			for _, p := range []sqlc.Pipeline{ownedByA, ownedByB, unowned} {
				predicted := canEdit(get(cookie, p.ID))
				resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
					"orgId": orgID.String(), "id": p.ID.String(), "name": p.Name,
					"contents": "// written by " + who, "matchers": []string{`env="prod"`},
				}, cookie)
				_ = g11DecodeBody(resp)
				Expect(resp.StatusCode == http.StatusOK).To(Equal(predicted),
					"%s on %s: can_edit=%v, UpdatePipeline status %d", who, p.Name, predicted, resp.StatusCode)
			}
		}
	})
})
