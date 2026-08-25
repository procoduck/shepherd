package auth

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Role requirement levels accepted by Authorize. These are also the exact
// vocabulary used by the shepherd.mgmt.v1 Connect authz interceptor's
// procedure->requirement map (internal/mgmtapi/rpc_interceptor.go).
const (
	RoleAny      = "any"
	RoleAppAdmin = "app-admin"
	RoleOrgAdmin = "org-admin"
	// RoleOrgEditor sits between admin and reader: it can author what an org
	// runs (pipelines, wizards, visual builder, simulation) without being able
	// to change what the org IS (destinations, tenant routes, git credentials,
	// teams, service accounts). Before it existed, anyone who needed to write a
	// pipeline also got the ability to rotate a tenant route.
	RoleOrgEditor = "org-editor"
	RoleOrgReader = "org-reader"
)

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

// Authorize is the role-decision logic shared by RequireAppAdmin and
// RequireOrgAccess, factored out so non-HTTP callers (the Connect authz
// interceptor) can reuse it without going through net/http middleware.
//
// min is one of RoleAny, RoleAppAdmin, RoleOrgAdmin, RoleOrgReader. orgID is
// only consulted for RoleOrgAdmin/RoleOrgReader and may be empty otherwise.
func Authorize(ctx context.Context, st *store.Store, sess *Session, orgID string, min string) error {
	switch min {
	case RoleAny:
		if sess == nil {
			return ErrUnauthenticated
		}
		return nil
	case RoleAppAdmin:
		if sess == nil {
			return ErrUnauthenticated
		}
		if !sess.IsAppAdmin {
			return ErrForbidden
		}
		return nil
	case RoleOrgAdmin:
		return authorizeOrgAccess(ctx, st, sess, orgID, RoleOrgAdmin)
	case RoleOrgEditor:
		return authorizeOrgAccess(ctx, st, sess, orgID, RoleOrgEditor)
	case RoleOrgReader:
		return authorizeOrgAccess(ctx, st, sess, orgID, RoleOrgReader)
	default:
		return ErrForbidden
	}
}

// authorizeOrgAccess is the extracted role decision behind RequireOrgAccess.
// minRole is a MINIMUM: the caller names the least privileged role that may
// proceed, and anything ranking at or above it passes (admin > editor >
// viewer, see orgRoleRank). App Admin short-circuits every org.
//
// Two independent paths reach a role and they do not mix — a local session
// resolves from org_members and returns; an OIDC session matches its groups
// claim against the org's admin_group_id / editor_group_id / reader_group_id,
// falling back to collector-level group_assignments for the viewer floor.
func authorizeOrgAccess(ctx context.Context, st *store.Store, sess *Session, orgIDStr string, minRole string) error {
	if sess == nil {
		return ErrUnauthenticated
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
		role, roleErr := st.Queries.GetOrgMemberRole(ctx, sqlc.GetOrgMemberRoleParams{OrgID: orgID, UserID: sess.UserID})
		if roleErr != nil {
			return ErrForbidden
		}
		if orgRoleRank(localToRequirement(role)) < orgRoleRank(minRole) {
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
	isOrgReader := org.ReaderGroupID.Valid && hasGroup(org.ReaderGroupID.String)

	switch {
	case isOrgAdmin:
		return nil
	case isOrgEditor:
		if orgRoleRank(RoleOrgEditor) >= orgRoleRank(minRole) {
			return nil
		}
		return ErrForbidden
	}

	// Below editor: only the reader floor can still be satisfied.
	if orgRoleRank(RoleOrgReader) < orgRoleRank(minRole) {
		return ErrForbidden
	}

	if !isOrgReader {
		// Scoped to THIS org. An unscoped version of this query granted the
		// reader floor in every org to anyone holding one assignment in any
		// org -- see the query's own comment.
		collectorIDs, err := st.Queries.ListCollectorIDsByGroupMembershipInOrg(ctx,
			sqlc.ListCollectorIDsByGroupMembershipInOrgParams{Column1: sess.GroupIDs, OrgID: org.ID})
		if err == nil && len(collectorIDs) > 0 {
			return nil
		}
		// W10 (docs/gateway-tier-plan.md §4): extend group_assignments'
		// "IdP group grants access" model up from the collector level to the
		// org level. A team member has no collector-level group_assignments row
		// of their own — teams own pipelines, not collectors — so without this
		// fallback a service-owner persona who is on a team but assigned to no
		// collector would fail this reader-equivalent gate entirely and never
		// reach the fine-grained ownership check (AuthorizeOwnership) that is
		// the actual point of W10. Team membership earns the same
		// reader-equivalent baseline group_assignments already grants; it does
		// not by itself grant WRITE — that is G11, enforced per-resource by
		// AuthorizeOwnership, not here.
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

// orgRoleRank orders the org roles so a floor can be compared numerically.
// Higher is more capable; admin satisfies every floor.
func orgRoleRank(role string) int {
	switch role {
	case RoleOrgAdmin:
		return 3
	case RoleOrgEditor:
		return 2
	case RoleOrgReader:
		return 1
	default:
		return 0
	}
}

// localToRequirement maps an org_members.role value onto the requirement
// vocabulary. Two names for the same three levels is a wart, kept because the
// stored values are Grafana's ("admin"/"editor"/"viewer") and the requirement
// constants are this API's ("org-admin"/...); translating in one function is
// better than either half changing to match the other.
func localToRequirement(role string) string {
	switch role {
	case OrgRoleAdmin:
		return RoleOrgAdmin
	case OrgRoleEditor:
		return RoleOrgEditor
	case OrgRoleViewer:
		return RoleOrgReader
	default:
		return ""
	}
}

// AuthorizeOwnership enforces G11 (docs/gateway-tier-plan.md): a team
// member may write only the pipelines their team owns. Callers above that
// rung bypass it — app admins, org admins, and (since the editor role) org
// editors, whose whole definition is authoring what the org runs. That upper
// check delegates to authorizeOrgAccess rather than restating it, so both
// ways of holding a role work here: an org admin who is a local user has no
// groups claim, and the previous inline slices.Contains against
// org.AdminGroupID silently refused them.
//
// ownerTeamID is the pipeline's current (or, for CreatePipeline, requested)
// owner_team_id. Empty means unowned: only an org admin or editor may write
// an unowned pipeline, preserving the pre-W10 "org-admin edits everything"
// behavior for pipelines that predate teams or were deliberately left
// platform-owned. A non-empty ownerTeamID requires the team to (a) exist,
// (b) belong to orgID — a team id from another org must never authorize a
// write here, the same cross-org confusion loadOwnedDestination/loadPipeline
// guard against for resource ids — and (c) have the caller as a member, by
// IdP group or by an explicit team_members row (0017).
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
	switch err := authorizeOrgAccess(ctx, st, sess, orgID, RoleOrgEditor); {
	case err == nil:
		return nil
	case errors.Is(err, ErrForbidden):
		// Not an editor. Fall through to the team-scoped check below, which
		// is the whole point of the rung beneath.
	default:
		return err
	}

	if ownerTeamID == "" {
		return ErrForbidden // unowned pipeline: org admin/editor only.
	}
	var tid pgtype.UUID
	if err := tid.Scan(ownerTeamID); err != nil {
		return ErrForbidden
	}
	team, err := st.Queries.GetTeamByID(ctx, tid)
	if err != nil || team.OrgID != oid {
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
