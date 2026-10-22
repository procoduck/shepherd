package auth_test

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// The org role ladder has three rungs (admin > editor > viewer) reached by two
// independent paths — an OIDC groups claim, or an org_members row for a local
// user. This spec drives auth.Authorize, the single function both the chi
// middleware and the Connect interceptor delegate to, across the full
// role x requirement matrix for both paths.
//
// The editor rung is why this exists: before it, "may author a pipeline" and
// "may re-point where telemetry ships" were one permission, so the interesting
// assertions are the negative ones — an editor must NOT clear an admin
// requirement.
var _ = Describe("Org role ladder", Label("integration"), func() {
	const (
		adminGroup  = "grp-admin"
		editorGroup = "grp-editor"
		viewerGroup = "grp-viewer"
	)

	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		orgID  string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "acme", DisplayName: "Acme",
			AdminGroupID:  adminGroup,
			EditorGroupID: pgtype.Text{String: editorGroup, Valid: true},
			ViewerGroupID: pgtype.Text{String: viewerGroup, Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = org.ID.String()
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	// newLocalUser creates a local user with the given org_members role and
	// returns a session shaped the way the local login path builds one.
	newLocalUser := func(login string, role auth.Role) *auth.Session {
		u, err := st.Queries.CreateUser(ctx, sqlc.CreateUserParams{
			Login: login, Email: login + "@example.com", DisplayName: login,
			PasswordHash: "x", IsAppAdmin: false, MustChangePassword: false,
		})
		Expect(err).NotTo(HaveOccurred())
		if role != "" {
			var oid pgtype.UUID
			Expect(oid.Scan(orgID)).To(Succeed())
			_, err = st.Queries.UpsertOrgMember(ctx, sqlc.UpsertOrgMemberParams{
				OrgID: oid, UserID: u.ID, Role: role.String(),
			})
			Expect(err).NotTo(HaveOccurred())
		}
		return &auth.Session{ID: "s-" + login, UserID: u.ID, Source: auth.SourceLocal}
	}

	oidcSession := func(groups ...string) *auth.Session {
		return &auth.Session{ID: "s-oidc", UserOID: "oid", GroupIDs: groups, Source: auth.SourceOIDC}
	}

	// each row: what the caller holds -> which requirements it clears.
	type expectation struct {
		admin, editor, viewer bool
	}

	assertLadder := func(sess *auth.Session, want expectation) {
		GinkgoHelper()
		for _, tc := range []struct {
			minRole auth.Role
			allowed bool
		}{
			{auth.RoleAdmin, want.admin},
			{auth.RoleEditor, want.editor},
			{auth.RoleViewer, want.viewer},
		} {
			err := auth.Authorize(ctx, st, sess, orgID, auth.RequireOrg(tc.minRole))
			if tc.allowed {
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("expected %s to be allowed", tc.minRole))
			} else {
				Expect(err).To(MatchError(auth.ErrForbidden), fmt.Sprintf("expected %s to be refused", tc.minRole))
			}
		}
	}

	Describe("group-derived (OIDC)", func() {
		It("an admin group member clears every requirement", func() {
			assertLadder(oidcSession(adminGroup), expectation{admin: true, editor: true, viewer: true})
		})

		It("an editor group member authors but is refused admin", func() {
			assertLadder(oidcSession(editorGroup), expectation{admin: false, editor: true, viewer: true})
		})

		It("a viewer group member is refused both write tiers", func() {
			assertLadder(oidcSession(viewerGroup), expectation{admin: false, editor: false, viewer: true})
		})

		It("an unrelated group clears nothing", func() {
			assertLadder(oidcSession("grp-unrelated"), expectation{})
		})

		It("an org with no editor group does not admit one", func() {
			org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
				Name: "no-editors", DisplayName: "No Editors", AdminGroupID: adminGroup,
			})
			Expect(err).NotTo(HaveOccurred())
			// An empty editor_group_id must not match a session carrying an
			// empty-string group: NULL means "no editor tier", not "everyone".
			err = auth.Authorize(ctx, st, &auth.Session{GroupIDs: []string{""}, Source: auth.SourceOIDC},
				org.ID.String(), auth.RequireOrg(auth.RoleEditor))
			Expect(err).To(MatchError(auth.ErrForbidden))
		})
	})

	Describe("locally assigned (org_members)", func() {
		It("admin clears every requirement", func() {
			assertLadder(newLocalUser("l-admin", auth.RoleAdmin), expectation{admin: true, editor: true, viewer: true})
		})

		It("editor authors but is refused admin", func() {
			assertLadder(newLocalUser("l-editor", auth.RoleEditor), expectation{admin: false, editor: true, viewer: true})
		})

		It("viewer is refused both write tiers", func() {
			assertLadder(newLocalUser("l-viewer", auth.RoleViewer), expectation{admin: false, editor: false, viewer: true})
		})

		It("a local user with no membership row clears nothing", func() {
			assertLadder(newLocalUser("l-none", ""), expectation{})
		})

		// The two paths must not combine: one session has one source, so
		// "why does this person have access" has a single answer. A local
		// session carrying group IDs must be judged on its membership row
		// alone — otherwise a stale or attacker-influenced groups list would
		// silently promote a local viewer.
		It("does not let group IDs promote a local session", func() {
			sess := newLocalUser("l-mixed", auth.RoleViewer)
			sess.GroupIDs = []string{adminGroup, editorGroup}
			assertLadder(sess, expectation{admin: false, editor: false, viewer: true})
		})
	})

	// W3-7: the OIDC path grants the viewer floor to anyone on a team in the
	// org (authz.go's team fallback, ListTeamsByOrgAndGroups) even with no
	// admin/editor/viewer group match. The local path did not have the same
	// fallback -- a local user with no org_members row was refused
	// outright, even when a team_members row said otherwise. This unifies
	// the two: local team membership clears the viewer floor the same way
	// group-backed team membership does.
	It("a local user who is only a team member clears the viewer floor", func() {
		var oid pgtype.UUID
		Expect(oid.Scan(orgID)).To(Succeed())
		team, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
			OrgID: oid, Name: "local-only-team",
		})
		Expect(err).NotTo(HaveOccurred())

		sess := newLocalUser("l-team-only", "") // no org_members row
		Expect(st.Queries.AddTeamMember(ctx, sqlc.AddTeamMemberParams{
			TeamID: team.ID, UserID: sess.UserID,
		})).To(Succeed())

		assertLadder(sess, expectation{admin: false, editor: false, viewer: true})
	})

	// A local user on no team at all, still with no org_members row, must
	// stay refused -- the fallback grants exactly what team membership
	// earns, not a blanket floor for every local user.
	It("a local user on no team and no org_members row clears nothing", func() {
		sess := newLocalUser("l-no-team", "")
		assertLadder(sess, expectation{})
	})

	// A team in ANOTHER org must not leak the viewer floor here, mirroring
	// the cross-org guard AuthorizeOwnership already enforces for writes.
	It("membership in a team from another org does not clear the viewer floor here", func() {
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "authz-w37-other", DisplayName: "Other", AdminGroupID: "authz-w37-other-admin",
		})
		Expect(err).NotTo(HaveOccurred())
		foreignTeam, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
			OrgID: other.ID, Name: "foreign-team",
		})
		Expect(err).NotTo(HaveOccurred())

		sess := newLocalUser("l-foreign-team", "")
		Expect(st.Queries.AddTeamMember(ctx, sqlc.AddTeamMemberParams{
			TeamID: foreignTeam.ID, UserID: sess.UserID,
		})).To(Succeed())

		assertLadder(sess, expectation{})
	})

	// W3-7b: ResolveOrgRole is the GetMe-facing counterpart to
	// authorizeOrgAccess -- authorizeOrgAccess answers "does this session
	// clear requirement X" and already grants the viewer floor to
	// a local user who is only a team member (W3-7, above). ResolveOrgRole
	// answers "what is this session's role", full stop, and drives what the
	// UI offers; before this, it still returned "" for the exact same
	// session, so GetMe reported no org at all while the server was already
	// granting reads under it.
	Describe("ResolveOrgRole", func() {
		It("resolves a local team-only member to the viewer role", func() {
			var oid pgtype.UUID
			Expect(oid.Scan(orgID)).To(Succeed())
			team, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
				OrgID: oid, Name: "resolve-local-only-team",
			})
			Expect(err).NotTo(HaveOccurred())

			sess := newLocalUser("l-resolve-team-only", "") // no org_members row
			Expect(st.Queries.AddTeamMember(ctx, sqlc.AddTeamMemberParams{
				TeamID: team.ID, UserID: sess.UserID,
			})).To(Succeed())

			org, err := st.Queries.GetOrgByID(ctx, oid)
			Expect(err).NotTo(HaveOccurred())
			Expect(resolveRole(ctx, st, sess, org)).To(Equal(auth.RoleViewer),
				"a local user who is only a team member should resolve to the viewer role, "+
					"matching the viewer floor authorizeOrgAccess already grants them")
		})

		// The fallback grants exactly what team membership earns, not a
		// blanket floor for every local user with no org_members row.
		It("resolves nothing for a local user on no team and no org_members row", func() {
			var oid pgtype.UUID
			Expect(oid.Scan(orgID)).To(Succeed())
			sess := newLocalUser("l-resolve-no-team", "")

			org, err := st.Queries.GetOrgByID(ctx, oid)
			Expect(err).NotTo(HaveOccurred())
			Expect(resolveRole(ctx, st, sess, org)).To(BeEmpty())
		})

		// A team in ANOTHER org must not leak the viewer role here, mirroring
		// the cross-org guard the Authorize-side fallback already enforces.
		It("does not resolve a role from a team in another org", func() {
			other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
				Name: "resolve-w37b-other", DisplayName: "Other", AdminGroupID: "resolve-w37b-other-admin",
			})
			Expect(err).NotTo(HaveOccurred())
			foreignTeam, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
				OrgID: other.ID, Name: "resolve-foreign-team",
			})
			Expect(err).NotTo(HaveOccurred())

			sess := newLocalUser("l-resolve-foreign-team", "")
			Expect(st.Queries.AddTeamMember(ctx, sqlc.AddTeamMemberParams{
				TeamID: foreignTeam.ID, UserID: sess.UserID,
			})).To(Succeed())

			var oid pgtype.UUID
			Expect(oid.Scan(orgID)).To(Succeed())
			org, err := st.Queries.GetOrgByID(ctx, oid)
			Expect(err).NotTo(HaveOccurred())
			Expect(resolveRole(ctx, st, sess, org)).To(BeEmpty())
		})

		// A local user WITH an org_members row must keep reading straight off
		// it -- the team fallback only fires on the pgx.ErrNoRows path, never
		// overriding an explicit role.
		It("prefers the org_members role over team membership when both exist", func() {
			var oid pgtype.UUID
			Expect(oid.Scan(orgID)).To(Succeed())
			team, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
				OrgID: oid, Name: "resolve-editor-and-team",
			})
			Expect(err).NotTo(HaveOccurred())

			sess := newLocalUser("l-resolve-editor-and-team", auth.RoleEditor)
			Expect(st.Queries.AddTeamMember(ctx, sqlc.AddTeamMemberParams{
				TeamID: team.ID, UserID: sess.UserID,
			})).To(Succeed())

			org, err := st.Queries.GetOrgByID(ctx, oid)
			Expect(err).NotTo(HaveOccurred())
			Expect(resolveRole(ctx, st, sess, org)).To(Equal(auth.RoleEditor))
		})
	})

	It("an app admin clears every requirement regardless of path", func() {
		assertLadder(&auth.Session{IsAppAdmin: true, Source: auth.SourceOIDC},
			expectation{admin: true, editor: true, viewer: true})
	})

	// AuthorizeOwnership is the scoped-write decision: it gates who may write a
	// pipeline, given the team that owns it. Everything at editor or above
	// writes anything in the org; below that, only the owning team's members do.
	Describe("scoped write (AuthorizeOwnership)", func() {
		var (
			groupTeam sqlc.Team // backed by an IdP group
			localTeam sqlc.Team // backed by explicit members only
		)

		BeforeEach(func() {
			var oid pgtype.UUID
			Expect(oid.Scan(orgID)).To(Succeed())
			var err error
			groupTeam, err = st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
				OrgID: oid, Name: "group-team",
				IdpGroupID: pgtype.Text{String: "grp-team", Valid: true},
			})
			Expect(err).NotTo(HaveOccurred())
			localTeam, err = st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
				OrgID: oid, Name: "local-team",
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("an org editor writes owned and unowned pipelines alike", func() {
			sess := oidcSession(editorGroup)
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, "")).To(Succeed())
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, groupTeam.ID.String())).To(Succeed())
		})

		// Regression: the check was an inline slices.Contains against
		// org.AdminGroupID, so an org admin who is a local user -- and
		// therefore has no groups claim at all -- was refused every write.
		It("a local org admin writes owned and unowned pipelines alike", func() {
			sess := newLocalUser("l-owner-admin", auth.RoleAdmin)
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, "")).To(Succeed())
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, groupTeam.ID.String())).To(Succeed())
		})

		It("a viewer writes nothing, owned or not", func() {
			sess := oidcSession(viewerGroup)
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, "")).To(MatchError(auth.ErrForbidden))
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, groupTeam.ID.String())).To(MatchError(auth.ErrForbidden))
		})

		It("a team's IdP group grants write to what that team owns, and nothing else", func() {
			sess := oidcSession("grp-team")
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, groupTeam.ID.String())).To(Succeed())
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, localTeam.ID.String())).To(MatchError(auth.ErrForbidden))
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, "")).To(MatchError(auth.ErrForbidden))
		})

		It("an explicit member writes what their team owns, and nothing else", func() {
			sess := newLocalUser("l-member", auth.RoleViewer)
			Expect(st.Queries.AddTeamMember(ctx, sqlc.AddTeamMemberParams{
				TeamID: localTeam.ID, UserID: sess.UserID,
			})).To(Succeed())
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, localTeam.ID.String())).To(Succeed())
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, groupTeam.ID.String())).To(MatchError(auth.ErrForbidden))
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, "")).To(MatchError(auth.ErrForbidden))
		})

		// A group-less team stores NULL, not ''. If it stored '', a session
		// whose claim contained an empty string would match it and inherit
		// write access to that team's pipelines.
		It("a team with no IdP group matches no groups claim", func() {
			for _, groups := range [][]string{{""}, {"grp-team"}, nil} {
				Expect(auth.AuthorizeOwnership(ctx, st, oidcSession(groups...), orgID, localTeam.ID.String())).
					To(MatchError(auth.ErrForbidden))
			}
		})

		It("a team from another org never authorizes a write", func() {
			other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
				Name: "other", DisplayName: "Other", AdminGroupID: "grp-other-admin",
			})
			Expect(err).NotTo(HaveOccurred())
			foreign, err := st.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
				OrgID: other.ID, Name: "foreign",
				IdpGroupID: pgtype.Text{String: "grp-team", Valid: true},
			})
			Expect(err).NotTo(HaveOccurred())
			// Same group, so membership itself is satisfied -- only the
			// cross-org check stands between this session and the write.
			Expect(auth.AuthorizeOwnership(ctx, st, oidcSession("grp-team"), orgID, foreign.ID.String())).
				To(MatchError(auth.ErrForbidden))
		})
	})

	// A collector-level group assignment is a viewer grant, but only
	// inside the org that owns the collector. The query behind this fallback
	// once filtered on group id alone, so one assignment anywhere cleared the
	// viewer floor everywhere -- and invisibly, because GetMe builds its org
	// list from group matches and never showed the extra orgs.
	It("a collector assignment grants read in its own org and nowhere else", func() {
		var oid pgtype.UUID
		Expect(oid.Scan(orgID)).To(Succeed())

		cluster, err := st.Queries.UpsertCluster(ctx, "assigned-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: oid})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{
			ClusterID: cluster.ID, Role: "metrics",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateGroupAssignment(ctx, sqlc.CreateGroupAssignmentParams{
			CollectorID: collector.ID, GroupID: "grp-assigned", GroupDisplayName: "Assigned",
		})
		Expect(err).NotTo(HaveOccurred())

		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "other-org", DisplayName: "Other", AdminGroupID: "grp-other-admin",
		})
		Expect(err).NotTo(HaveOccurred())

		sess := oidcSession("grp-assigned")
		Expect(auth.Authorize(ctx, st, sess, orgID, auth.RequireOrg(auth.RoleViewer))).To(Succeed(),
			"the assignment is in this org, so the viewer floor should be cleared")
		Expect(auth.Authorize(ctx, st, sess, other.ID.String(), auth.RequireOrg(auth.RoleViewer))).
			To(MatchError(auth.ErrForbidden),
				"an assignment in another org must not grant any access here")
	})
})

// resolveRole is auth.ResolveOrgRole with "no role" as the empty Role, so a
// spec can assert on one value.
func resolveRole(ctx context.Context, st *store.Store, sess *auth.Session, org sqlc.Org) auth.Role {
	role, ok := auth.ResolveOrgRole(ctx, st, sess, org)
	if !ok {
		return ""
	}
	return role
}
