package mgmtapi

import (
	"os"
	"path/filepath"
	"testing"

	"shepherd/internal/wizard"
)

// A wizard package registers itself in init(). That means a package the binary
// never imports cannot exist at runtime, however complete and well-tested it
// is — and its own tests will not notice, because a package's tests import
// that package by definition. Five catalog wizards shipped in exactly that
// state: full suites, goldens validated against the real Alloy binary, and
// absent from the running product. It was found by opening the Wizards page in
// a real deployment and seeing one wizard where there should have been six.
//
// This walks the source tree rather than a hand-kept list, so adding a wizard
// package without a blank import in rpc_wizard.go fails here.
//
// Red run, executed: removing the blank import for
// internal/wizard/clustermetrics fails this test with `wizard package
// "clustermetrics" is not registered`.
func TestEveryWizardPackageIsRegistered(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "wizard"))
	if err != nil {
		t.Fatalf("reading internal/wizard: %v", err)
	}

	registered := map[string]bool{}
	for _, kind := range wizard.Default().ListKinds() {
		registered[kind] = true
	}
	if len(registered) == 0 {
		t.Fatal("no wizards registered at all — this test would pass vacuously if it only " +
			"compared two empty sets")
	}

	var pkgs []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "wizardtest" {
			continue
		}
		pkgs = append(pkgs, e.Name())
	}
	if len(pkgs) == 0 {
		t.Fatal("found no wizard packages on disk — this test would prove nothing")
	}

	// A package directory name is not its wizard Kind (appobservability vs
	// app-observability), so match on the Kind with separators removed rather
	// than inventing a naming rule the packages do not follow.
	normalized := map[string]bool{}
	for kind := range registered {
		stripped := ""
		for _, r := range kind {
			if r != '-' && r != '_' {
				stripped += string(r)
			}
		}
		normalized[stripped] = true
	}

	for _, pkg := range pkgs {
		if !normalized[pkg] {
			t.Errorf("wizard package %q is not registered: it exists under internal/wizard/ but "+
				"nothing imports it, so its init() never runs and it cannot appear in the API or "+
				"the UI. Add a blank import in rpc_wizard.go.", pkg)
		}
	}
}
