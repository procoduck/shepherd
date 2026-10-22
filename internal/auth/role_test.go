package auth

import (
	"context"
	"errors"
	"testing"
)

func TestParseRoleFailsClosed(t *testing.T) {
	for _, s := range []string{"viewer", "editor", "admin"} {
		r, err := ParseRole(s)
		if err != nil || r.String() != s {
			t.Errorf("ParseRole(%q) = %q, %v", s, r, err)
		}
	}
	// The retired spellings are not roles any more.
	for _, s := range []string{"", "reader", "org-reader", "org-editor", "org-admin", "orgadmin", "Admin", "app-admin"} {
		if _, err := ParseRole(s); !errors.Is(err, ErrUnknownRole) {
			t.Errorf("ParseRole(%q) err = %v, want ErrUnknownRole", s, err)
		}
	}
}

func TestRoleAtLeast(t *testing.T) {
	roles := []Role{RoleViewer, RoleEditor, RoleAdmin}
	for i, have := range roles {
		for j, need := range roles {
			if got, want := have.AtLeast(need), i >= j; got != want {
				t.Errorf("%s.AtLeast(%s) = %v, want %v", have, need, got, want)
			}
		}
		if have.AtLeast("") || Role("").AtLeast(have) || Role("bogus").AtLeast(RoleViewer) {
			t.Errorf("a non-role must clear nothing and be cleared by nothing (have %s)", have)
		}
	}
}

// The zero Requirement and a requirement built from a non-role deny even an
// app admin: a table entry that forgot its constructor fails closed.
func TestMalformedRequirementDeniesEveryone(t *testing.T) {
	admin := &Session{IsAppAdmin: true}
	for _, req := range []Requirement{{}, RequireOrg(""), RequireOrg("reader"), RequireAppAdminOrOrg("")} {
		if err := Authorize(context.Background(), nil, admin, "", req); !errors.Is(err, ErrForbidden) {
			t.Errorf("Authorize(app admin, %v) = %v, want ErrForbidden", req, err)
		}
		if _, ok := req.OrgMinimum(); ok {
			t.Errorf("%v.OrgMinimum() reported an org role", req)
		}
	}
}
