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
	RoleAny       = "any"
	RoleAppAdmin  = "app-admin"
	RoleOrgAdmin  = "org-admin"
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
		return authorizeOrgAccess(ctx, st, sess, orgID, true)
	case RoleOrgReader:
		return authorizeOrgAccess(ctx, st, sess, orgID, false)
	default:
		return ErrForbidden
	}
}

// authorizeOrgAccess is the extracted role decision behind RequireOrgAccess:
// App Admin can access any org; Org Admin is a member of the org's
// admin_group_id; Reader is a member of reader_group_id or any assigned
// group on any collector in the org. requireAdmin selects between the
// "orgadmin" and "reader" minimum roles.
func authorizeOrgAccess(ctx context.Context, st *store.Store, sess *Session, orgIDStr string, requireAdmin bool) error {
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

	isOrgAdmin := slices.Contains(sess.GroupIDs, org.AdminGroupID)
	isOrgReader := org.ReaderGroupID.Valid && slices.Contains(sess.GroupIDs, org.ReaderGroupID.String)

	if requireAdmin {
		if !isOrgAdmin {
			return ErrForbidden
		}
		return nil
	}

	if !isOrgAdmin && !isOrgReader {
		collectorIDs, err := st.Queries.ListCollectorIDsByGroupMembership(ctx, sess.GroupIDs)
		if err == nil && len(collectorIDs) > 0 {
			return nil
		}
		// W10 (docs/gateway-tier-plan.md §4): extend group_assignments'
		// "IdP group grants access" model up from the collector level to
		// the org level. A team member has no collector-level
		// group_assignments row of their own — teams own pipelines, not
		// collectors — so without this fallback a service-owner persona
		// who is on a team but assigned to no collector would fail this
		// reader-equivalent gate entirely and never reach the
		// fine-grained ownership check (AuthorizeOwnership) that is the
		// actual point of W10. Team membership earns the same
		// reader-equivalent baseline group_assignments already grants;
		// it does not by itself grant WRITE — that is G11, enforced
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

// AuthorizeOwnership enforces G11 (docs/gateway-tier-plan.md): a team
// member may write only the pipelines their team owns. App admins and org
// admins bypass — this is the same top rung authorizeOrgAccess's
// requireAdmin path already grants, restated here so PipelineService's
// write handlers have one call to make instead of two.
//
// ownerTeamID is the pipeline's current (or, for CreatePipeline, requested)
// owner_team_id. Empty means unowned: only an org admin may write an
// unowned pipeline, preserving the exact pre-W10 "org-admin edits
// everything" behavior for pipelines that predate teams or were
// deliberately left platform-owned. A non-empty ownerTeamID requires the
// team to (a) exist, (b) belong to orgID — a team id from another org must
// never authorize a write here, the same cross-org confusion
// loadOwnedDestination/loadPipeline guard against for resource ids — and
// (c) have the caller's session as a member via IdP group.
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
	org, err := st.Queries.GetOrgByID(ctx, oid)
	if err != nil {
		return ErrOrgNotFound
	}
	if slices.Contains(sess.GroupIDs, org.AdminGroupID) {
		return nil // org admin: writes anything in its org, owned or not.
	}

	if ownerTeamID == "" {
		return ErrForbidden // unowned pipeline: org-admin-only.
	}
	var tid pgtype.UUID
	if err := tid.Scan(ownerTeamID); err != nil {
		return ErrForbidden
	}
	team, err := st.Queries.GetTeamByID(ctx, tid)
	if err != nil || team.OrgID != oid {
		return ErrForbidden
	}
	if !slices.Contains(sess.GroupIDs, team.IdpGroupID) {
		return ErrForbidden
	}
	return nil
}
