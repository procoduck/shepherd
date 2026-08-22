package onboarding_test

import (
	"os"
	"testing"

	"shepherd/internal/onboarding"
)

// TestGenGoldens regenerates testdata/*.golden from the fixtures in
// render_test.go. NOT a regular test — skipped unless GEN_GOLDENS=1 is
// explicitly set, matching internal/receiver/gen_goldens_test.go's and
// internal/gateway/route_test.go's convention. CI always skips it. Run
// after changing any renderer, review the diff, and never run it just to
// make a failing golden test pass — fix the renderer.
//
//	GEN_GOLDENS=1 go test ./internal/onboarding/ -run TestGenGoldens -v
func TestGenGoldens(t *testing.T) {
	if os.Getenv("GEN_GOLDENS") == "" {
		t.Skip("set GEN_GOLDENS=1 to regenerate")
	}
	for _, name := range fixtureNames() {
		bundle, err := onboarding.Render(fixtures[name])
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		for artifact, content := range map[string]string{
			"env":       bundle.Env,
			"lambda":    bundle.Lambda,
			"terraform": bundle.Terraform,
			"sam":       bundle.SAM,
			"cdk":       bundle.CDK,
			"k8s":       bundle.K8s,
			"sdk-notes": bundle.SDKNotes,
		} {
			path := goldenPath(name, artifact)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // test-only golden regeneration
				t.Fatalf("%s/%s: write golden: %v", name, artifact, err)
			}
			t.Logf("wrote %s", path)
		}
	}
}
