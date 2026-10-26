package mgmtapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"
)

// #362: a viewer service account never passes the write gate. Its stored
// capability is NULL (read as ""), which is not apply; and even an
// in-memory identity pairing the viewer tier with apply — a row the
// service_accounts_role_capability CHECK refuses — is refused on its tier,
// so the guarantee does not rest on the database constraint alone.
func TestRequireWriteAuthorizedRefusesViewerTier(t *testing.T) {
	const org = "00000000-0000-4000-8000-0000000000bb"
	const principal = "owner@example.com"
	for _, capability := range []string{"", capabilityPropose, capabilityApply} {
		sa := serviceAccountIdentity{
			ID: "sa", OrgID: org, Name: "sa", Role: "viewer", Capability: capability,
			OnBehalfOf: principal, DelegatedPrincipal: principal,
		}
		err := requireWriteAuthorized(withServiceAccount(context.Background(), sa))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("viewer tier, capability %q: got %v, want PermissionDenied", capability, err)
		}
	}

	// The same identity at editor tier with apply passes, so the refusal
	// above is the tier's, not a fixture that fails everything.
	sa := serviceAccountIdentity{
		ID: "sa", OrgID: org, Name: "sa", Role: "editor", Capability: capabilityApply,
		OnBehalfOf: principal, DelegatedPrincipal: principal,
	}
	if err := requireWriteAuthorized(withServiceAccount(context.Background(), sa)); err != nil {
		t.Fatalf("editor tier with apply: got %v, want nil", err)
	}
}
