package mgmtapi

import (
	"testing"

	"connectrpc.com/connect"

	"shepherd/internal/auth"
)

// A service account whose stored tier is not one the server recognises must
// reach nothing org-scoped. Before step 7a (#361) an unknown tier fell back
// to editor; now it denies.
func TestServiceAccountUnknownTierDenies(t *testing.T) {
	const org = "00000000-0000-4000-8000-0000000000aa"
	for _, tier := range []string{"", "bogus", "super-admin", "org-editor"} {
		sa := serviceAccountIdentity{ID: "sa", OrgID: org, Name: "sa", Role: tier, Capability: "apply"}
		err := authorizeServiceAccountProcedure(sa, org, auth.RequireOrg(auth.RoleViewer))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("tier %q on a viewer procedure: got %v, want PermissionDenied", tier, err)
		}
	}
}
