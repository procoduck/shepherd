package chartvalues_test

import (
	"os"
	"testing"

	"shepherd/internal/chartvalues"
)

// TestGenGoldens regenerates testdata/golden/*.values.yaml from the fixtures
// in fixtures_test.go. NOT a regular test — skipped unless GEN_GOLDENS=1 is
// explicitly set, matching internal/receiver/gen_goldens_test.go's
// convention. CI always skips it. Run after changing render.go, review the
// diff, and never run it just to make a failing golden test pass — fix the
// renderer.
//
//	GEN_GOLDENS=1 go test ./internal/chartvalues/ -run TestGenGoldens -v
func TestGenGoldens(t *testing.T) {
	if os.Getenv("GEN_GOLDENS") == "" {
		t.Skip("set GEN_GOLDENS=1 to regenerate")
	}
	for _, name := range fixtureNames {
		out, err := chartvalues.Render(fixture(name))
		if err != nil {
			t.Fatalf("%s: render: %v", name, err)
		}
		path := "testdata/golden/" + name + ".values.yaml"
		if err := os.WriteFile(path, out, 0o644); err != nil { //nolint:gosec // test-only golden regeneration
			t.Fatalf("%s: write golden: %v", name, err)
		}
		t.Logf("wrote %s", path)
	}
}
