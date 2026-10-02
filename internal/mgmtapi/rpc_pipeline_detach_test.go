package mgmtapi_test

import (
	"context"
	"encoding/json"
	"io"
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

// PipelineService.DetachFromWizard (#262 follow-up): the way out for a
// wizard pipeline whose text an operator wants to own. In place — same id,
// revisions, owner team, matchers, enabled — it becomes a "ui" pipeline with
// no wizard kind, state or render fingerprint, so a destination update no
// longer regenerates (or refuses because of) it. Red run: the RPC answered
// unimplemented.
var _ = Describe("PipelineService.DetachFromWizard", Label("integration"), func() {
	var (
		ctx                    context.Context
		cancel                 context.CancelFunc
		st                     *store.Store
		server                 *httptest.Server
		org, other             pgtype.UUID
		admin, editor, reader  *http.Cookie
		mimir                  sqlc.Destination
		teamID                 pgtype.UUID
		teamMember, nonMember  *http.Cookie
		ownedByTeam, unowned   sqlc.Pipeline
		commitAs               func(name string) sqlc.Pipeline
		call                   func(cookie *http.Cookie, procedure string, body map[string]any) (int, map[string]any)
		detach                 func(cookie *http.Cookie, o pgtype.UUID, p sqlc.Pipeline) (int, map[string]any)
		pipeline               func(id pgtype.UUID) sqlc.Pipeline
		revisionsOf            func(id pgtype.UUID) []sqlc.PipelineRevision
		auditRowsFor           func(action string) []sqlc.AuditLog
		updatedMimirWithNewURL func() (int, map[string]any)
		newMimirURL            string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 8}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "detach-org", DisplayName: "Detach", AdminGroupID: "detach-admin",
			EditorGroupID: pgtype.Text{String: "detach-editor", Valid: true},
			ReaderGroupID: pgtype.Text{String: "detach-reader", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		org = o.ID
		p, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "detach-other", DisplayName: "Other", AdminGroupID: "detach-other-admin"})
		Expect(err).NotTo(HaveOccurred())
		other = p.ID
		team, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{OrgID: org, Name: "obs", IdpGroupID: pgtype.Text{String: "detach-team", Valid: true}})
		Expect(err).NotTo(HaveOccurred())
		teamID = team.ID

		mimir, err = st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: org, Name: "mimir", Type: "prometheus", Url: "https://mimir.example.com/api/v1/push",
			AuthMode: "none", Extra: json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())
		newMimirURL = "https://mimir-new.example.com/api/v1/push"

		admin = newAppAdminSession(ctx, st)
		editor = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "detach-editor")}
		reader = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "detach-reader")}
		teamMember = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "detach-team")}
		nonMember = reader
		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))

		call = func(cookie *http.Cookie, procedure string, body map[string]any) (int, map[string]any) {
			resp := postConnectJSON(server, "/shepherd.mgmt.v1."+procedure, cookie, body)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			b, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			var out map[string]any
			Expect(json.Unmarshal(b, &out)).To(Succeed(), string(b))
			return resp.StatusCode, out
		}
		pipeline = func(id pgtype.UUID) sqlc.Pipeline {
			p, err := st.Queries.GetPipelineByID(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			return p
		}
		revisionsOf = func(id pgtype.UUID) []sqlc.PipelineRevision {
			revs, err := st.Queries.ListPipelineRevisions(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			return revs
		}
		auditRowsFor = func(action string) []sqlc.AuditLog {
			rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: org, Column3: action, Limit: 100})
			Expect(err).NotTo(HaveOccurred())
			return rows
		}
		commitAs = func(name string) sqlc.Pipeline {
			code, out := call(admin, "WizardService/CommitWizard", map[string]any{
				"org_id": org.String(), "kind": "self-monitoring", "name": name,
				"state": map[string]any{"metrics_dest_name": "mimir", "logs_enabled": false},
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			p, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: org, Name: name})
			Expect(err).NotTo(HaveOccurred())
			return p
		}
		detach = func(cookie *http.Cookie, o pgtype.UUID, p sqlc.Pipeline) (int, map[string]any) {
			return call(cookie, "PipelineService/DetachFromWizard", map[string]any{"orgId": o.String(), "id": p.ID.String()})
		}
		updatedMimirWithNewURL = func() (int, map[string]any) {
			return call(admin, "DestinationService/UpdateDestination", map[string]any{
				"orgId": org.String(), "id": mimir.ID.String(), "name": "mimir", "type": "prometheus",
				"url": newMimirURL, "authMode": "none",
			})
		}

		ownedByTeam = commitAs("owned")
		_, err = st.Queries.SetPipelineOwnerTeam(ctx, sqlc.SetPipelineOwnerTeamParams{ID: ownedByTeam.ID, OwnerTeamID: teamID})
		Expect(err).NotTo(HaveOccurred())
		code, out := call(admin, "PipelineService/EnablePipeline", map[string]any{"orgId": org.String(), "id": ownedByTeam.ID.String()})
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		ownedByTeam = pipeline(ownedByTeam.ID)
		unowned = commitAs("unowned")
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("keeps the pipeline's id, contents, matchers, enabled, owner and revisions; drops everything wizard", func() {
		revsBefore := revisionsOf(ownedByTeam.ID)
		code, out := detach(teamMember, org, ownedByTeam)
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		Expect(out["id"]).To(Equal(ownedByTeam.ID.String()))
		Expect(out["source"]).To(Equal("ui"))

		got := pipeline(ownedByTeam.ID)
		Expect(got.Source).To(Equal("ui"))
		Expect(got.WizardKind.Valid).To(BeFalse())
		Expect(got.WizardState).To(BeNil())
		Expect(got.WizardRenderSha256.Valid).To(BeFalse())
		Expect(got.Contents).To(Equal(ownedByTeam.Contents))
		Expect(got.Matchers).To(MatchJSON(ownedByTeam.Matchers))
		Expect(got.Enabled).To(BeTrue())
		Expect(got.OwnerTeamID).To(Equal(teamID))

		revs := revisionsOf(ownedByTeam.ID)
		Expect(revs).To(HaveLen(len(revsBefore) + 1))
		Expect(revs[0].ChangeNote).To(Equal("detached from wizard"))
		Expect(revs[0].Contents).To(Equal(got.Contents))
		Expect(revs[0].WizardState).To(BeNil())

		rows := auditRowsFor("pipeline.detach")
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].ResourceID).To(Equal(ownedByTeam.ID.String()))
	})

	It("takes the pipeline out of destination re-renders", func() {
		code, out := detach(admin, org, unowned)
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		detachedContents := pipeline(unowned.ID).Contents

		code, out = updatedMimirWithNewURL()
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		Expect(pipeline(unowned.ID).Contents).To(Equal(detachedContents), "a detached pipeline is the operator's text")
		Expect(pipeline(ownedByTeam.ID).Contents).To(ContainSubstring(newMimirURL), "the still-attached one is re-rendered")
		for _, r := range auditRowsFor("pipeline.rerender") {
			Expect(r.ResourceID).NotTo(Equal(unowned.ID.String()))
		}

		// …and a hand edit after detaching no longer blocks destination edits.
		code, out = call(admin, "PipelineService/UpdatePipeline", map[string]any{
			"orgId": org.String(), "id": unowned.ID.String(), "name": "unowned",
			"contents": detachedContents + "\n// mine now\n", "matchers": []string{`role="singleton"`},
		})
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		newMimirURL = "https://mimir-third.example.com/api/v1/push"
		code, out = updatedMimirWithNewURL()
		Expect(code).To(Equal(http.StatusOK), "%v", out)
	})

	It("is the same capability as editing: an org editor may, a reader and a non-member of the owning team may not", func() {
		code, out := detach(editor, org, unowned)
		Expect(code).To(Equal(http.StatusOK), "%v", out)

		code, out = detach(reader, org, ownedByTeam)
		Expect(code).To(Equal(http.StatusForbidden), "%v", out)
		Expect(out["code"]).To(Equal("permission_denied"))
		code, _ = detach(nonMember, org, ownedByTeam)
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(pipeline(ownedByTeam.ID).Source).To(Equal("wizard"))
	})

	It("does not reach another org's pipeline", func() {
		code, out := detach(admin, other, ownedByTeam)
		Expect(code).To(Equal(http.StatusNotFound), "%v", out)
		Expect(pipeline(ownedByTeam.ID).Source).To(Equal("wizard"))
	})

	It("refuses a pipeline that is not a wizard pipeline", func() {
		code, out := call(admin, "PipelineService/CreatePipeline", map[string]any{
			"orgId": org.String(), "name": "plain", "contents": "// plain", "matchers": []string{},
		})
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		plain, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: org, Name: "plain"})
		Expect(err).NotTo(HaveOccurred())
		code, out = detach(admin, org, plain)
		Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
		Expect(out["code"]).To(Equal("failed_precondition"))
	})
})
