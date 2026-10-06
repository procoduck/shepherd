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

// Regression coverage for W3-8: GetMe's role resolution must apply the same
// empty-group guard authorizeOrgAccess's hasGroup already does (authz.go).
// Before ResolveOrgRole existed, GetMe did its own inline
// slices.Contains(sess.GroupIDs, org.AdminGroupID) with no guard, so an org
// whose admin_group_id is "" (never configured) reported every session
// carrying an empty string in its groups claim as an admin of that org —
// offering admin actions in the UI that the server (authorizeOrgAccess)
// would then correctly refuse.
var _ = Describe("MeService GetMe role resolution", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		authHandler *auth.Handler
		server      *httptest.Server
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler = auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	postConnect := func(procedure string, body map[string]any, cookie *http.Cookie) *http.Response {
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

	decodeBody := func(resp *http.Response) map[string]any {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var payload map[string]any
		Expect(json.Unmarshal(body, &payload)).To(Succeed())
		return payload
	}

	It("does not report admin for an org whose admin_group_id is empty, to a session carrying an empty group", func() {
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "me-roles-empty-admin-group", DisplayName: "Empty Admin Group", AdminGroupID: "",
		})
		Expect(err).NotTo(HaveOccurred())

		groupsJSON, err := json.Marshal([]string{""})
		Expect(err).NotTo(HaveOccurred())
		sessionID := fmt.Sprintf("me-roles-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "me-roles-user-oid", Email: "me-roles-user@example.com", DisplayName: "Me Roles User",
			GroupIds:   groupsJSON,
			IsAppAdmin: false,
			ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:     "test",
		})
		Expect(err).NotTo(HaveOccurred())
		cookie := &http.Cookie{Name: "shepherd_session", Value: sessionID}

		resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", map[string]any{}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		payload := decodeBody(resp)

		// protojson omits an empty repeated field entirely rather than
		// emitting `[]`, so "orgs" may be absent from the payload when this
		// session clears no org at all — that absence is itself a pass here,
		// not a shape to assert on.
		orgsRaw, present := payload["orgs"]
		if !present {
			return
		}
		orgs, ok := orgsRaw.([]any)
		Expect(ok).To(BeTrue(), "expected orgs, when present, to be an array")
		for _, e := range orgs {
			entry, ok := e.(map[string]any)
			Expect(ok).To(BeTrue())
			if entry["id"] == org.ID.String() {
				Fail(fmt.Sprintf("org with empty admin_group_id must not appear in the membership list for a session with no real group, got role %q", entry["role"]))
			}
		}
	})

	// W3-7b: authorizeOrgAccess already grants the reader-equivalent floor
	// to a local user who has no org_members row but is a member of a team
	// in the org (W3-7). Before ResolveOrgRole grew the matching fallback,
	// GetMe called auth.ResolveOrgRole (rpc_me.go) and got "" for this exact
	// session, so the org was omitted from the response entirely — the UI
	// showed no org while the server was already granting reads under it.
	It("reports the viewer role for a local user who is only a team member", func() {
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "me-roles-local-team-only", DisplayName: "Local Team Only", AdminGroupID: "me-roles-local-team-only-admin",
		})
		Expect(err).NotTo(HaveOccurred())

		user, err := st.Queries.CreateUser(ctx, sqlc.CreateUserParams{
			Login: "me-roles-team-user", Email: "me-roles-team-user@example.com", DisplayName: "Team User",
			PasswordHash: "x", IsAppAdmin: false, MustChangePassword: false,
		})
		Expect(err).NotTo(HaveOccurred())

		team, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
			OrgID: org.ID, Name: "me-roles-local-only-team",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.AddTeamMember(ctx, sqlc.AddTeamMemberParams{
			TeamID: team.ID, UserID: user.ID,
		})).To(Succeed())

		sessionID := fmt.Sprintf("me-roles-local-session-%d", time.Now().UnixNano())
		groupsJSON, err := json.Marshal([]string{})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserID: user.ID, UserOid: "local:" + user.Login, Email: user.Email, DisplayName: user.DisplayName,
			GroupIds:   groupsJSON,
			IsAppAdmin: false,
			ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:     auth.SourceLocal,
		})
		Expect(err).NotTo(HaveOccurred())
		cookie := &http.Cookie{Name: "shepherd_session", Value: sessionID}

		resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", map[string]any{}, cookie)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		payload := decodeBody(resp)

		orgsRaw, present := payload["orgs"]
		Expect(present).To(BeTrue(), "the team-only local user should see at least this org")
		orgs, ok := orgsRaw.([]any)
		Expect(ok).To(BeTrue(), "expected orgs to be an array")

		found := false
		for _, e := range orgs {
			entry, ok := e.(map[string]any)
			Expect(ok).To(BeTrue())
			if entry["id"] == org.ID.String() {
				found = true
				Expect(entry["role"]).To(Equal(auth.OrgRoleViewer),
					"a local user who is only a team member should resolve to the viewer role")
			}
		}
		Expect(found).To(BeTrue(), "org membership by team should have appeared in GetMe's org list")
	})

	// #261 (maintainer decision 2026-10-06): OrgMembership carries the org's
	// own orgs.tenant_id so the destination form can pre-fill it for a
	// member who cannot read the org row. Red run: before the field existed
	// the membership had no tenantId.
	It("returns the org's own tenant to a member, and none for an org without one", func() {
		withTenant, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "me-tenant-acme", DisplayName: "Acme", AdminGroupID: "me-tenant-admins",
			ReaderGroupID: pgtype.Text{String: "me-tenant-readers", Valid: true},
			TenantID:      pgtype.Text{String: "acme", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		without, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "me-tenant-none", DisplayName: "No tenant", AdminGroupID: "me-tenant-none-admins",
			ReaderGroupID: pgtype.Text{String: "me-tenant-readers", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())

		groupsJSON, err := json.Marshal([]string{"me-tenant-readers"})
		Expect(err).NotTo(HaveOccurred())
		sessionID := fmt.Sprintf("me-tenant-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "me-tenant-user", Email: "me-tenant@example.com", DisplayName: "Reader",
			GroupIds: groupsJSON, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", map[string]any{}, &http.Cookie{Name: "shepherd_session", Value: sessionID})
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		orgs, ok := decodeBody(resp)["orgs"].([]any)
		Expect(ok).To(BeTrue())
		byID := map[string]map[string]any{}
		for _, e := range orgs {
			entry, ok := e.(map[string]any)
			Expect(ok).To(BeTrue())
			id, _ := entry["id"].(string) //nolint:errcheck // asserted via the map lookups below
			byID[id] = entry
		}
		Expect(byID).To(HaveKey(withTenant.ID.String()))
		Expect(byID).To(HaveKey(without.ID.String()))
		Expect(byID[withTenant.ID.String()]).To(HaveKeyWithValue("tenantId", "acme"))
		Expect(byID[without.ID.String()]).NotTo(HaveKey("tenantId"), "an org without a tenant reports none")
	})
})
