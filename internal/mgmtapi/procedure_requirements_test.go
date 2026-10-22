package mgmtapi

import (
	"os"
	"sort"
	"strings"
	"testing"

	"shepherd/internal/auth"
)

// describeRequirement renders a procedureRequirements entry's effective
// semantics: who may call it as a human session and as a service account.
//
// The human half is the requirement's own rendering (auth.Authorize's
// app-admin and org paths are exercised against a database by
// internal/auth's role ladder and this package's role matrix). The service
// account half is EVALUATED: authorizeServiceAccountProcedure is run for a
// same-org account at each tier and for a foreign-org account, and the line
// records the lowest tier that passes.
func describeRequirement(t *testing.T, req auth.Requirement) string {
	t.Helper()
	const org, otherOrg = "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	sa := func(orgID string, tier auth.Role) serviceAccountIdentity {
		return serviceAccountIdentity{ID: "sa", OrgID: orgID, Name: "sa", Role: tier.String(), Capability: capabilityApply}
	}

	human := "human=" + req.String()
	if req.AuthenticatedOnly() {
		human = "human=authenticated"
	}

	var saDesc string
	switch {
	case authorizeServiceAccountProcedure(sa(otherOrg, auth.RoleAdmin), org, req) == nil:
		saDesc = "sa=any"
	default:
		saDesc = "sa=deny"
		for _, tier := range []auth.Role{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin} {
			if authorizeServiceAccountProcedure(sa(org, tier), org, req) == nil {
				saDesc = "sa=same-org,tier>=" + tier.String()
				break
			}
		}
	}
	return human + "\t" + saDesc
}

// TestProcedureRequirementsGolden pins every procedure's effective
// authorization to testdata/procedure_requirements.golden, generated from
// main before the role refactor (step 7a, #361). The refactor must reproduce
// it byte for byte.
func TestProcedureRequirementsGolden(t *testing.T) {
	lines := make([]string, 0, len(procedureRequirements))
	for proc, req := range procedureRequirements {
		lines = append(lines, proc+"\t"+describeRequirement(t, req))
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"
	want, err := os.ReadFile("testdata/procedure_requirements.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("procedureRequirements semantics differ from the golden.\n--- got ---\n%s", got)
	}
}
