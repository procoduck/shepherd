package mgmtapi

import (
	"errors"
	"fmt"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/auth"
)

// errRoleUnspecified is returned when a write leaves a role unset.
// ROLE_UNSPECIFIED never grants anything, so it is never stored.
var errRoleUnspecified = errors.New("role is required (ROLE_VIEWER, ROLE_EDITOR or ROLE_ADMIN)")

// roleToProto is the one auth.Role -> wire conversion. A value that is not a
// role (unreachable: auth.Role values come from auth.ParseRole or its
// constants) is reported as ROLE_UNSPECIFIED, which the SPA treats as no
// access.
func roleToProto(r auth.Role) mgmtv1.Role {
	switch r {
	case auth.RoleViewer:
		return mgmtv1.Role_ROLE_VIEWER
	case auth.RoleEditor:
		return mgmtv1.Role_ROLE_EDITOR
	case auth.RoleAdmin:
		return mgmtv1.Role_ROLE_ADMIN
	}
	return mgmtv1.Role_ROLE_UNSPECIFIED
}

// roleFromProto is the one wire -> auth.Role conversion, for writes. It fails
// closed: ROLE_UNSPECIFIED and any number outside the enum are errors.
func roleFromProto(r mgmtv1.Role) (auth.Role, error) {
	switch r {
	case mgmtv1.Role_ROLE_VIEWER:
		return auth.RoleViewer, nil
	case mgmtv1.Role_ROLE_EDITOR:
		return auth.RoleEditor, nil
	case mgmtv1.Role_ROLE_ADMIN:
		return auth.RoleAdmin, nil
	case mgmtv1.Role_ROLE_UNSPECIFIED:
		return "", errRoleUnspecified
	}
	return "", fmt.Errorf("unknown role %d", int32(r))
}

// storedRoleToProto converts a stored role column (org_members.role,
// service_accounts.role) for a response, through auth.ParseRole — the one
// DB string -> auth.Role conversion.
func storedRoleToProto(stored string) mgmtv1.Role {
	r, err := auth.ParseRole(stored)
	if err != nil {
		return mgmtv1.Role_ROLE_UNSPECIFIED
	}
	return roleToProto(r)
}
