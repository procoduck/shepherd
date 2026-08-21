package signals

import (
	"errors"
	"testing"
)

func TestEnforce(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		sig     Set
		wantErr error // nil means Enforce must return nil
	}{
		{"metrics role, metrics signal: allowed", "metrics", NewSet(Metrics), nil},
		{"metrics role, logs signal: refused", "metrics", NewSet(Logs), ErrSignalMismatch},
		{"metrics role, mixed metrics+logs: refused", "metrics", NewSet(Metrics, Logs), ErrSignalMismatch},
		{"logs role, logs signal: allowed", "logs", NewSet(Logs), nil},
		{"logs role, traces signal: refused", "logs", NewSet(Traces), ErrSignalMismatch},
		{"receiver role, metrics+logs+traces: allowed", "receiver", NewSet(Metrics, Logs, Traces), nil},
		{"receiver role, metrics only: allowed", "receiver", NewSet(Metrics), nil},
		{"receiver role, profiles: refused", "receiver", NewSet(Profiles), ErrSignalMismatch},
		{"receiver role, traces+profiles: refused", "receiver", NewSet(Traces, Profiles), ErrSignalMismatch},
		{"singleton role, everything: allowed", "singleton", NewSet(Metrics, Logs, Traces, Profiles), nil},
		{"singleton role, empty: allowed", "singleton", Set{}, nil},
		{"any role, empty (unproven-but-empty) signal set: allowed", "metrics", Set{}, nil},
		{"unknown role: refused regardless of signal", "bogus-role", NewSet(Metrics), ErrUnknownRole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Enforce(tc.role, tc.sig)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Enforce(%q, %s) = %v, want nil", tc.role, tc.sig, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Enforce(%q, %s) = %v, want an error wrapping %v", tc.role, tc.sig, err, tc.wantErr)
			}
		})
	}
}

// TestPolicies_CoverKnownRoles guards against the policy table silently
// falling out of sync with internal/agentapi's validRoles (internal/agentapi/
// service.go). The role list is a literal here, not an import, because W1's
// territory (docs/gateway-tier-plan.md §8) is internal/signals/** only and
// this package must not depend on internal/agentapi — but that means drift
// between the two lists is exactly the failure mode Enforce's ErrUnknownRole
// exists to catch, so this test pins the list this package was built against
// and fails if a row goes missing.
func TestPolicies_CoverKnownRoles(t *testing.T) {
	knownRoles := []string{"metrics", "logs", "singleton", "receiver"}
	for _, role := range knownRoles {
		t.Run(role, func(t *testing.T) {
			policy, ok := Policies[role]
			if !ok {
				t.Fatalf("role %q has no Policies row", role)
			}
			if policy.Rationale == "" {
				t.Fatalf("role %q has no Rationale — a policy table with no rationale is a claim, not a policy", role)
			}
			if !policy.Unrestricted && policy.Allowed.Empty() {
				t.Fatalf("role %q is neither Unrestricted nor has any Allowed signal — nothing could ever be served to it", role)
			}
		})
	}
}

// TestEnforce_UnrestrictedIsExplicitNotDefault asserts that "unrestricted"
// only ever applies to a role with Unrestricted: true set — never to a role
// simply missing every Allowed signal, and never to a role missing from the
// table entirely. Removing this distinction (say, by treating "no row" as
// "no restriction") is precisely the failure mode ErrUnknownRole exists to
// prevent.
func TestEnforce_UnrestrictedIsExplicitNotDefault(t *testing.T) {
	err := Enforce("some-role-nobody-registered", NewSet(Metrics, Logs, Traces, Profiles))
	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("Enforce on an unregistered role = %v, want ErrUnknownRole (never a silent allow)", err)
	}
}
