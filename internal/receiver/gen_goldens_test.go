package receiver_test

import (
	"os"
	"testing"

	"shepherd/internal/receiver"
)

// TestGenGoldens regenerates testdata/*.golden.alloy from the fixtures in
// fixtures_test.go. NOT a regular test — skipped unless GEN_GOLDENS=1 is
// explicitly set, matching internal/visual/gen_goldens_test.go's convention.
// CI always skips it. Run after changing render.go, review the diff, and
// never run it just to make a failing golden test pass — fix the renderer.
//
//	GEN_GOLDENS=1 go test ./internal/receiver/ -run TestGenGoldens -v
func TestGenGoldens(t *testing.T) {
	if os.Getenv("GEN_GOLDENS") == "" {
		t.Skip("set GEN_GOLDENS=1 to regenerate")
	}
	for _, name := range fixtureNames {
		out, err := receiver.Render(fixture(name))
		if err != nil {
			t.Fatalf("%s: render: %v", name, err)
		}
		path := "testdata/" + name + ".golden.alloy"
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil { //nolint:gosec // test-only golden regeneration
			t.Fatalf("%s: write golden: %v", name, err)
		}
		t.Logf("wrote %s", path)
	}
}
