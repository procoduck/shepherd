package auth

import (
	"errors"
	"fmt"
)

// Role is an org role (spec §7.2): viewer < editor < admin, each including the
// one below it. It is the one role vocabulary — org_members.role,
// service_accounts.role, the requirement table and the API all use it.
//
// Editor sits between admin and viewer: it can author what an org runs
// (pipelines, wizards, visual builder, simulation) without being able to
// change what the org IS (destinations, tenant routes, git credentials, teams,
// service accounts).
//
// App admin is not a Role. It is a separate, cross-org flag
// (Session.IsAppAdmin), so no org role, however high, is ever an app admin.
//
// The zero value is not a role: it satisfies no requirement, and ParseRole
// never returns it without an error.
type Role string

// The three org roles, in ascending order. Their values are the stored form.
const (
	RoleViewer Role = "viewer"
	RoleEditor Role = "editor"
	RoleAdmin  Role = "admin"
)

// ErrUnknownRole is returned by ParseRole for a value that is not a role.
var ErrUnknownRole = errors.New("auth: unknown role")

// ParseRole is the one conversion from a stored or wire string to a Role
// (Role's string form is the stored value). It fails closed: anything that is
// not exactly "viewer", "editor" or "admin" is an error, never a default.
func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case RoleViewer, RoleEditor, RoleAdmin:
		return r, nil
	}
	return "", fmt.Errorf("%w %q", ErrUnknownRole, s)
}

// String is the role's stored form.
func (r Role) String() string { return string(r) }

// rank orders the roles so a minimum can be compared numerically; 0 is "not
// a role" and clears nothing.
func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// AtLeast reports whether r clears the minimum role min. A value that is not a
// role, on either side, clears nothing.
func (r Role) AtLeast(minRole Role) bool {
	have, need := r.rank(), minRole.rank()
	return have > 0 && need > 0 && have >= need
}
