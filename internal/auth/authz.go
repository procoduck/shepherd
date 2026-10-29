package auth

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// requirementKind is the shape of a Requirement. Its zero value is not a
// kind: a zero Requirement clears nothing.
type requirementKind uint8

const (
	reqAuthenticated requirementKind = iota + 1
	reqAppAdmin
	reqOrg
	reqAppAdminOrOrg
)

// Requirement is what a caller must hold to proceed: the vocabulary of the
// shepherd.mgmt.v1 authz interceptor's procedure table
// (internal/mgmtapi/rpc_interceptor.go). Build one with the Require*
// constructors; the zero value denies everyone.
//
// Every org requirement is also cleared by an app admin — app admin is the
// cross-org flag above every org role (spec §7.2).
type Requirement struct {
	kind    requirementKind
	minRole Role
}

// RequireAuthenticated is cleared by any authenticated caller.
func RequireAuthenticated() Requirement { return Requirement{kind: reqAuthenticated} }

// RequireAppAdmin is cleared by an app admin only. No org role, however high,
// clears it, and neither does any service account.
func RequireAppAdmin() Requirement { return Requirement{kind: reqAppAdmin} }

// RequireOrg is cleared by an app admin, or by a caller holding at least
// minRole in the org the request names.
func RequireOrg(minRole Role) Requirement { return Requirement{kind: reqOrg, minRole: minRole} }

// RequireAppAdminOrOrg is for a procedure that serves an app admin across
// every org and an org role-holder within the org the request names
// (AdminService.SearchGroups: global search for an app admin, scoped search
// for an org admin). Who clears it is exactly who clears RequireOrg(minRole);
// it is a separate constructor so the table says the procedure has a
// cross-org mode.
func RequireAppAdminOrOrg(minRole Role) Requirement {
	return Requirement{kind: reqAppAdminOrOrg, minRole: minRole}
}

// AuthenticatedOnly reports whether r is RequireAuthenticated.
func (r Requirement) AuthenticatedOnly() bool {
	switch r.kind {
	case reqAuthenticated:
		return true
	case reqAppAdmin, reqOrg, reqAppAdminOrOrg:
		return false
	}
	return false
}

// OrgMinimum returns the org role that clears r for a caller who is not an
// app admin, and false when no org role does (RequireAppAdmin,
// RequireAuthenticated, or the zero Requirement).
func (r Requirement) OrgMinimum() (Role, bool) {
	switch r.kind {
	case reqOrg, reqAppAdminOrOrg:
		return r.minRole, r.minRole.rank() > 0
	case reqAuthenticated, reqAppAdmin:
		return "", false
	}
	return "", false
}

// String renders r for diagnostics and the requirement golden test:
// "authenticated", "app-admin", "org>=<role>", "app-admin|org>=<role>".
func (r Requirement) String() string {
	switch r.kind {
	case reqAuthenticated:
		return "authenticated"
	case reqAppAdmin:
		return "app-admin"
	case reqOrg:
		return "org>=" + r.minRole.String()
	case reqAppAdminOrOrg:
		return "app-admin|org>=" + r.minRole.String()
	}
	return "invalid"
}

// Sentinel errors returned by Authorize. Callers (HTTP middleware, the
// Connect authz interceptor) map these to their own wire representation —
// an HTTP status + legacy JSON body for middleware, a connect.Code for the
// interceptor.
var (
	ErrUnauthenticated = errors.New("auth: unauthenticated")
	ErrForbidden       = errors.New("auth: forbidden")
	ErrInvalidOrgID    = errors.New("auth: invalid org id")
	ErrOrgNotFound     = errors.New("auth: org not found")
)

// Authorize decides whether sess clears req in the org named by orgID
// (consulted only for org requirements; may be empty otherwise). It is the
// one role decision for a human session; the Connect authz interceptor and
// the per-handler checks call it.
func Authorize(ctx context.Context, st *store.Store, sess *Session, orgID string, req Requirement) error {
	if sess == nil {
		return ErrUnauthenticated
	}
	switch req.kind {
	case reqAuthenticated:
		return nil
	case reqAppAdmin:
		if !sess.IsAppAdmin {
			return ErrForbidden
		}
		return nil
	case reqOrg, reqAppAdminOrOrg:
		return authorizeOrgAccess(ctx, st, sess, orgID, req.minRole)
	}
	return ErrForbidden
}

// authorizeOrgAccess is the org half of Authorize. minRole is a MINIMUM: the caller names the least privileged role that may
// proceed, and anything ranking at or above it passes (admin > editor >
// viewer, see Role.AtLeast). App Admin short-circuits every org.
//
// Two independent paths reach a role and they do not mix — a local session
// resolves from org_members and returns; an OIDC session matches its groups
// claim against the org's admin_group_id / editor_group_id / viewer_group_id,
// falling back to collector-level group_assignments for the viewer floor.
func authorizeOrgAccess(ctx context.Context, st *store.Store, sess *Session, orgIDStr string, minRole Role) error {
	if sess == nil {
		return ErrUnauthenticated
	}
	if minRole.rank() == 0 {
		return ErrForbidden // a requirement built from a non-role clears nothing
	}
	if sess.IsAppAdmin {
		return nil
	}

	var orgID pgtype.UUID
	if err := orgID.Scan(orgIDStr); err != nil {
		return ErrInvalidOrgID
	}

	org, err := st.Queries.GetOrgByID(ctx, orgID)
	if err != nil {
		return ErrOrgNotFound
	}

	// A LOCAL session resolves its role from org_members and stops here.
	//
	// It deliberately does not fall through to the group checks below: a local
	// user has no IdP groups, so those would all be false anyway, and more
	// importantly the two mechanisms must not combine. One session has one
	// source, so "why does this person have access" has one answer rather than
	// two places to look.
	if sess.Source == SourceLocal && sess.UserID.Valid {
		stored, roleErr := st.Queries.GetOrgMemberRole(ctx, sqlc.GetOrgMemberRoleParams{OrgID: orgID, UserID: sess.UserID})
		switch {
		case roleErr == nil:
			// A stored value that is not a role (the CHECK constraint should
			// never let one through) clears nothing.
			role, err := ParseRole(stored)
			if err != nil || !role.AtLeast(minRole) {
				return ErrForbidden
			}
			return nil
		case !errors.Is(roleErr, pgx.ErrNoRows):
			return ErrForbidden
		}

		// W3-7: no org_members row, but this mirrors the OIDC path's team
		// fallback below (W10) rather than stopping here -- a local user has
		// no groups claim, so ListTeamsByOrgAndGroups can never reach them,
		// and without this a local user who is only a team member (0017)
		// was refused the same viewer floor a group-backed team member gets
		// for free.
		if !RoleViewer.AtLeast(minRole) {
			return ErrForbidden
		}
		isMember, err := st.Queries.IsUserMemberOfAnyTeamInOrg(ctx,
			sqlc.IsUserMemberOfAnyTeamInOrgParams{OrgID: orgID, UserID: sess.UserID})
		if err != nil || !isMember {
			return ErrForbidden
		}
		return nil
	}

	// hasGroup treats an empty candidate as "no group configured" rather than
	// as a value to match. An org whose admin_group_id is "" would otherwise be
	// administrable by any session carrying an empty string in its groups
	// claim, and the Microsoft Graph path does not filter those out.
	hasGroup := func(candidate string) bool {
		return candidate != "" && slices.Contains(sess.GroupIDs, candidate)
	}
	isOrgAdmin := hasGroup(org.AdminGroupID)
	isOrgEditor := org.EditorGroupID.Valid && hasGroup(org.EditorGroupID.String)
	isOrgViewer := org.ViewerGroupID.Valid && hasGroup(org.ViewerGroupID.String)

	switch {
	case isOrgAdmin:
		return nil
	case isOrgEditor:
		if RoleEditor.AtLeast(minRole) {
			return nil
		}
		return ErrForbidden
	}

	// Below editor: only the viewer floor can still be satisfied.
	if !RoleViewer.AtLeast(minRole) {
		return ErrForbidden
	}

	if !isOrgViewer {
		// Scoped to THIS org. An unscoped version of this query granted the
		// viewer floor in every org to anyone holding one assignment in any
		// org -- see the query's own comment.
		collectorIDs, err := st.Queries.ListCollectorIDsByGroupMembershipInOrg(ctx,
			sqlc.ListCollectorIDsByGroupMembershipInOrgParams{GroupIds: sess.GroupIDs, OrgID: org.ID})
		if err == nil && len(collectorIDs) > 0 {
			return nil
		}
		// W10 (docs/gateway-tier-plan.md §4): extend group_assignments'
		// "IdP group grants access" model up from the collector level to the
		// org level. A team member has no collector-level group_assignments row
		// of their own — teams own pipelines, not collectors — so without this
		// fallback a service-owner persona who is on a team but assigned to no
		// collector would fail this viewer gate entirely and never
		// reach the fine-grained ownership check (AuthorizeOwnership) that is
		// the actual point of W10. Team membership earns the same viewer
		// floor group_assignments already grants; it does not by itself grant
		// WRITE — that is the team owner-write grant (G11), enforced
		// per-resource by AuthorizeOwnership, not here.
		teams, err := st.Queries.ListTeamsByOrgAndGroups(ctx, sqlc.ListTeamsByOrgAndGroupsParams{
			OrgID:   org.ID,
			Column2: sess.GroupIDs,
		})
		if err != nil || len(teams) == 0 {
			return ErrForbidden
		}
	}
	return nil
}

// ResolveOrgRole reports the role a session holds in org, and false if none:
// for a local session read straight off org_members (RoleViewer when there is
// no org_members row but the user is a member of a team in the org — W3-7b,
// mirroring authorizeOrgAccess's own W3-7 fallback); for an OIDC session
// resolved from its groups claim against the org's admin/editor/viewer group,
// in that priority order.
//
// It is the GetMe-facing counterpart to authorizeOrgAccess: that function
// answers "does this session clear requirement X", gated by a minimum; this
// one answers "what is this session's role", full stop, which is what drives
// the UI's own gating (offering the pipeline editor, the admin screens, ...).
// Kept separate rather than derived from authorizeOrgAccess because the two
// ask different questions — the "for is-app-admin, treat as admin in every
// org" answer lives in the caller (rpc_me.go), which already special-cases
// IsAppAdmin before this is reached.
//
// The empty-group guard matters here for exactly the reason it matters in
// authorizeOrgAccess's hasGroup: an org whose admin_group_id is "" (never
// configured) must not be reported as admin to a session whose groups claim
// happens to carry an empty string.
func ResolveOrgRole(ctx context.Context, st *store.Store, sess *Session, org sqlc.Org) (Role, bool) {
	if sess.Source == SourceLocal && sess.UserID.Valid {
		stored, err := st.Queries.GetOrgMemberRole(ctx, sqlc.GetOrgMemberRoleParams{OrgID: org.ID, UserID: sess.UserID})
		switch {
		case err == nil:
			role, parseErr := ParseRole(stored)
			return role, parseErr == nil
		case !errors.Is(err, pgx.ErrNoRows):
			return "", false
		}

		// W3-7b: mirrors authorizeOrgAccess's own W3-7 team fallback -- a
		// local user with no org_members row but who is a member of a team
		// in this org already clears the viewer floor there, so this must
		// report it rather than nothing, or the UI hides an org the server is
		// already granting reads under.
		isMember, err := st.Queries.IsUserMemberOfAnyTeamInOrg(ctx,
			sqlc.IsUserMemberOfAnyTeamInOrgParams{OrgID: org.ID, UserID: sess.UserID})
		if err != nil || !isMember {
			return "", false
		}
		return RoleViewer, true
	}

	hasGroup := func(candidate string) bool {
		return candidate != "" && slices.Contains(sess.GroupIDs, candidate)
	}
	switch {
	case hasGroup(org.AdminGroupID):
		return RoleAdmin, true
	case org.EditorGroupID.Valid && hasGroup(org.EditorGroupID.String):
		return RoleEditor, true
	case org.ViewerGroupID.Valid && hasGroup(org.ViewerGroupID.String):
		return RoleViewer, true
	default:
		return "", false
	}
}

// AuthorizeOwnership decides who may write a pipeline (G11,
// docs/gateway-tier-plan.md). Two grants reach it, checked in order:
//
//  1. Org role: an app admin, or an org editor or admin of orgID, writes any
//     pipeline in the org, owned or not. This delegates to authorizeOrgAccess
//     rather than restating it, so both ways of holding a role work here.
//  2. Team owner-write (authorizeTeamOwnerWrite): a member of the team that
//     owns the pipeline writes that pipeline, whatever their org role.
//
// ownerTeamID is the pipeline's current (or, for CreatePipeline, requested)
// owner_team_id. Empty means unowned: only the org-role grant reaches an
// unowned pipeline, preserving the pre-W10 behavior for pipelines that predate
// teams or were deliberately left platform-owned.
func AuthorizeOwnership(ctx context.Context, st *store.Store, sess *Session, orgID, ownerTeamID string) error {
	if sess == nil {
		return ErrUnauthenticated
	}
	if sess.IsAppAdmin {
		return nil
	}

	var oid pgtype.UUID
	if err := oid.Scan(orgID); err != nil {
		return ErrInvalidOrgID
	}
	// Editor or above writes anything in its org, owned or not. This also
	// covers the org-exists check: authorizeOrgAccess returns ErrOrgNotFound,
	// which the default branch passes through unchanged.
	switch err := authorizeOrgAccess(ctx, st, sess, orgID, RoleEditor); {
	case err == nil:
		return nil
	case errors.Is(err, ErrForbidden):
		// Not an editor. Fall through to the team owner-write grant.
	default:
		return err
	}
	return authorizeTeamOwnerWrite(ctx, st, sess, oid, ownerTeamID)
}

// authorizeTeamOwnerWrite is the team owner-write grant (spec §7.2): a member
// of the team that owns a pipeline may write it, independent of any org role.
// A non-empty ownerTeamID requires the team to (a) exist, (b) belong to orgID
// — a team id from another org must never authorize a write here, the same
// cross-org confusion loadOwnedDestination/loadPipeline guard against for
// resource ids — and (c) have the caller as a member, by IdP group or by an
// explicit team_members row (0017). An unowned pipeline (ownerTeamID "") has
// no owning team, so the grant never reaches it.
func authorizeTeamOwnerWrite(ctx context.Context, st *store.Store, sess *Session, orgID pgtype.UUID, ownerTeamID string) error {
	if ownerTeamID == "" {
		return ErrForbidden // unowned pipeline: org editor/admin only.
	}
	var tid pgtype.UUID
	if err := tid.Scan(ownerTeamID); err != nil {
		return ErrForbidden
	}
	team, err := st.Queries.GetTeamByID(ctx, tid)
	if err != nil || team.OrgID != orgID {
		return ErrForbidden
	}

	// A local session has no groups claim, so it can only match the explicit
	// half; an OIDC session has no users row, so it can only match the group
	// half. Checking the one that applies keeps the "one session, one source"
	// rule authorizeOrgAccess states.
	if sess.Source == SourceLocal && sess.UserID.Valid {
		member, err := st.Queries.IsTeamMember(ctx, sqlc.IsTeamMemberParams{TeamID: tid, UserID: sess.UserID})
		if err != nil || !member {
			return ErrForbidden
		}
		return nil
	}

	// A team with no group (0017) is backed by explicit members only; an
	// empty/absent group must never match a session, however its claim looks.
	if !team.IdpGroupID.Valid || !slices.Contains(sess.GroupIDs, team.IdpGroupID.String) {
		return ErrForbidden
	}
	return nil
}
