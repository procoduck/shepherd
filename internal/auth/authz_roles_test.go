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
// assertions are the negative ones — an editor must NOT clear an org-admin
// requirement.
var _ = Describe("Org role ladder", Label("integration"), func() {
	const (
		adminGroup  = "grp-admin"
		editorGroup = "grp-editor"
		readerGroup = "grp-reader"
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
			ReaderGroupID: pgtype.Text{String: readerGroup, Valid: true},
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
	newLocalUser := func(login, role string) *auth.Session {
		u, err := st.Queries.CreateUser(ctx, sqlc.CreateUserParams{
			Login: login, Email: login + "@example.com", DisplayName: login,
			PasswordHash: "x", IsAppAdmin: false, MustChangePassword: false,
		})
		Expect(err).NotTo(HaveOccurred())
		if role != "" {
			var oid pgtype.UUID
			Expect(oid.Scan(orgID)).To(Succeed())
			_, err = st.Queries.UpsertOrgMember(ctx, sqlc.UpsertOrgMemberParams{
				OrgID: oid, UserID: u.ID, Role: role,
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
		admin, editor, reader bool
	}

	assertLadder := func(sess *auth.Session, want expectation) {
		GinkgoHelper()
		for _, tc := range []struct {
			minRole string
			allowed bool
		}{
			{auth.RoleOrgAdmin, want.admin},
			{auth.RoleOrgEditor, want.editor},
			{auth.RoleOrgReader, want.reader},
		} {
			err := auth.Authorize(ctx, st, sess, orgID, tc.minRole)
			if tc.allowed {
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("expected %s to be allowed", tc.minRole))
			} else {
				Expect(err).To(MatchError(auth.ErrForbidden), fmt.Sprintf("expected %s to be refused", tc.minRole))
			}
		}
	}

	Describe("group-derived (OIDC)", func() {
		It("an admin group member clears every requirement", func() {
			assertLadder(oidcSession(adminGroup), expectation{admin: true, editor: true, reader: true})
		})

		It("an editor group member authors but is refused org-admin", func() {
			assertLadder(oidcSession(editorGroup), expectation{admin: false, editor: true, reader: true})
		})

		It("a reader group member is refused both write tiers", func() {
			assertLadder(oidcSession(readerGroup), expectation{admin: false, editor: false, reader: true})
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
				org.ID.String(), auth.RoleOrgEditor)
			Expect(err).To(MatchError(auth.ErrForbidden))
		})
	})

	Describe("locally assigned (org_members)", func() {
		It("admin clears every requirement", func() {
			assertLadder(newLocalUser("l-admin", auth.OrgRoleAdmin), expectation{admin: true, editor: true, reader: true})
		})

		It("editor authors but is refused org-admin", func() {
			assertLadder(newLocalUser("l-editor", auth.OrgRoleEditor), expectation{admin: false, editor: true, reader: true})
		})

		It("viewer is refused both write tiers", func() {
			assertLadder(newLocalUser("l-viewer", auth.OrgRoleViewer), expectation{admin: false, editor: false, reader: true})
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
			sess := newLocalUser("l-mixed", auth.OrgRoleViewer)
			sess.GroupIDs = []string{adminGroup, editorGroup}
			assertLadder(sess, expectation{admin: false, editor: false, reader: true})
		})
	})

	It("an app admin clears every requirement regardless of path", func() {
		assertLadder(&auth.Session{IsAppAdmin: true, Source: auth.SourceOIDC},
			expectation{admin: true, editor: true, reader: true})
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
			sess := newLocalUser("l-owner-admin", auth.OrgRoleAdmin)
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, "")).To(Succeed())
			Expect(auth.AuthorizeOwnership(ctx, st, sess, orgID, groupTeam.ID.String())).To(Succeed())
		})

		It("a viewer writes nothing, owned or not", func() {
			sess := oidcSession(readerGroup)
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
			sess := newLocalUser("l-member", auth.OrgRoleViewer)
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

	// A collector-level group assignment is a reader-equivalent grant, but only
	// inside the org that owns the collector. The query behind this fallback
	// once filtered on group id alone, so one assignment anywhere cleared the
	// reader floor everywhere -- and invisibly, because GetMe builds its org
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
		Expect(auth.Authorize(ctx, st, sess, orgID, auth.RoleOrgReader)).To(Succeed(),
			"the assignment is in this org, so the reader floor should be cleared")
		Expect(auth.Authorize(ctx, st, sess, other.ID.String(), auth.RoleOrgReader)).
			To(MatchError(auth.ErrForbidden),
				"an assignment in another org must not grant any access here")
	})
})
