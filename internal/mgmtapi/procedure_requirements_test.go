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
func describeRequirement(t *testing.T, req string) string {
	t.Helper()
	switch req {
	case auth.RoleAny:
		return "human=authenticated\tsa=any"
	case auth.RoleAppAdmin:
		return "human=app-admin\tsa=deny"
	case reqAppOrOrgAdmin:
		return "human=app-admin|org>=admin\tsa=same-org,tier>=admin"
	case auth.RoleOrgAdmin:
		return "human=org>=admin\tsa=same-org,tier>=admin"
	case auth.RoleOrgEditor:
		return "human=org>=editor\tsa=same-org,tier>=editor"
	case auth.RoleOrgReader:
		return "human=org>=viewer\tsa=same-org,tier>=viewer"
	}
	t.Fatalf("unknown requirement %q", req)
	return ""
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
