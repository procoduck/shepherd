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

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// G11 (docs/gateway-tier-plan.md): "A team member can write what their team
// owns and CANNOT write another team's resources in the same org; removing
// the ownership check makes named specs red." This suite proves both
// halves over the real Connect wire (newRPCWiringRouter, the same helper
// rpc_destination_test.go/rpc_wiring_test.go use), against
// auth.AuthorizeOwnership (internal/auth/authz.go) as called individually
// by each PipelineService write handler (rpc_pipeline.go) — not a
// unit-level call to AuthorizeOwnership directly, so this exercises the
// same path the frontend actually drives.
var _ = Describe("G11: PipelineService scoped write (team ownership)", Label("integration"), func() {
	var (
		ctx            context.Context
		cancel         context.CancelFunc
		st             *store.Store
		server         *httptest.Server
		orgID          pgtype.UUID
		teamA, teamB   sqlc.Team
		ownedByA       sqlc.Pipeline
		ownedByB       sqlc.Pipeline
		unowned        sqlc.Pipeline
		memberACookie  *http.Cookie
		memberBCookie  *http.Cookie
		adminCookie    *http.Cookie
		outsiderCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "g11-org", DisplayName: "G11 Org", AdminGroupID: "g11-admin-group",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		teamA, err = st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{OrgID: orgID, Name: "team-a", IdpGroupID: "g11-team-a"})
		Expect(err).NotTo(HaveOccurred())
		teamB, err = st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{OrgID: orgID, Name: "team-b", IdpGroupID: "g11-team-b"})
		Expect(err).NotTo(HaveOccurred())

		ownedByA, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: "owned-by-a", Source: "ui", Matchers: json.RawMessage("[]"),
			OwnerTeamID: teamA.ID,
		})
		Expect(err).NotTo(HaveOccurred())
		ownedByB, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: "owned-by-b", Source: "ui", Matchers: json.RawMessage("[]"),
			OwnerTeamID: teamB.ID,
		})
		Expect(err).NotTo(HaveOccurred())
		unowned, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: "unowned", Source: "ui", Matchers: json.RawMessage("[]"),
		})
		Expect(err).NotTo(HaveOccurred())

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))

		memberACookie = g11Session(ctx, st, "member-a", []string{"g11-team-a"})
		memberBCookie = g11Session(ctx, st, "member-b", []string{"g11-team-b"})
		adminCookie = g11Session(ctx, st, "admin", []string{"g11-admin-group"})
		outsiderCookie = g11Session(ctx, st, "outsider", []string{"some-unrelated-group"})
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("lets a team member update the pipeline their team owns", func() {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": ownedByA.ID.String(), "name": "owned-by-a", "contents": "// updated",
		}, memberACookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("lets team B's member update the pipeline team B owns (symmetric to team A's case)", func() {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": ownedByB.ID.String(), "name": "owned-by-b", "contents": "// updated by b",
		}, memberBCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("REFUSES a team member writing another team's pipeline in the same org (the named G11 spec)", func() {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": ownedByB.ID.String(), "name": "owned-by-b", "contents": "// hijacked",
		}, memberACookie)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		payload := g11DecodeBody(resp)
		Expect(payload["code"]).To(Equal("permission_denied"))

		// Confirm the write truly did not happen — not just a well-formed
		// refusal that still let the mutation through.
		stored, err := st.Queries.GetPipelineByID(ctx, ownedByB.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Contents).NotTo(ContainSubstring("hijacked"))
	})

	It("refuses a team member deleting/enabling/disabling another team's pipeline", func() {
		for _, proc := range []string{"DeletePipeline", "EnablePipeline", "DisablePipeline"} {
			resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/"+proc, map[string]any{
				"orgId": orgID.String(), "id": ownedByB.ID.String(),
			}, memberACookie)
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden), "procedure %s", proc)
		}
	})

	It("refuses a team member writing an UNOWNED pipeline (admin-only, matching pre-W10 behavior)", func() {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": unowned.ID.String(), "name": "unowned", "contents": "// x",
		}, memberACookie)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})

	It("still lets an org admin write any team's pipeline (bypass preserved)", func() {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": ownedByB.ID.String(), "name": "owned-by-b", "contents": "// admin edit",
		}, adminCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("refuses an org member on no team at all from writing an owned pipeline", func() {
		resp := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": ownedByA.ID.String(), "name": "owned-by-a", "contents": "// x",
		}, outsiderCookie)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	})

	It("lets a team member CREATE a pipeline owned by their own team, but not by another team or unowned", func() {
		ok := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"orgId": orgID.String(), "name": "new-by-a", "contents": "", "source": "ui", "ownerTeamId": teamA.ID.String(),
		}, memberACookie)
		Expect(ok.StatusCode).To(Equal(http.StatusOK))

		forOtherTeam := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"orgId": orgID.String(), "name": "new-by-a-for-b", "contents": "", "source": "ui", "ownerTeamId": teamB.ID.String(),
		}, memberACookie)
		Expect(forOtherTeam.StatusCode).To(Equal(http.StatusForbidden))

		unownedAttempt := g11PostConnect(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"orgId": orgID.String(), "name": "new-by-a-unowned", "contents": "", "source": "ui",
		}, memberACookie)
		Expect(unownedAttempt.StatusCode).To(Equal(http.StatusForbidden))
	})
})

func g11Session(ctx context.Context, st *store.Store, userOID string, groupIDs []string) *http.Cookie {
	groupsJSON, err := json.Marshal(groupIDs)
	Expect(err).NotTo(HaveOccurred())
	sessionID := fmt.Sprintf("g11-session-%s-%d", userOID, time.Now().UnixNano())
	_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
		ID: sessionID, UserOid: userOID, Email: userOID + "@example.com", DisplayName: userOID,
		GroupIds: groupsJSON, IsAppAdmin: false,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		Source:    "test",
	})
	Expect(err).NotTo(HaveOccurred())
	return &http.Cookie{Name: "shepherd_session", Value: sessionID}
}

func g11PostConnect(server *httptest.Server, procedure string, body map[string]any, cookie *http.Cookie) *http.Response {
	buf, err := json.Marshal(body)
	Expect(err).NotTo(HaveOccurred())
	req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(string(buf)))
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

func g11DecodeBody(resp *http.Response) map[string]any {
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	b, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	var payload map[string]any
	Expect(json.Unmarshal(b, &payload)).To(Succeed())
	return payload
}
