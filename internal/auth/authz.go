package auth

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
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
		if err != nil || len(collectorIDs) == 0 {
			return ErrForbidden
		}
	}
	return nil
}
