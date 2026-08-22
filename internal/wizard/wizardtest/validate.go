package wizardtest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shepherd/internal/config"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// AssertGoldensAgainstRealAlloy runs every committed golden in testdataDir
// through the real pinned Alloy binary's validate command (Stage1+Stage2),
// the same shape internal/receiver/alloy_validate_test.go and
// internal/wizard/appobservability/wizard_alloy_test.go use. A wizard
// package's own goldens are a byte comparison against text the package
// itself produced, so they would agree with anything the package emits —
// including syntax the real binary rejects (deprecated attributes, wrong
// block names). This is deliberately NOT self-skipping when no binary is
// available: it calls t.Fatal, not t.Skip, so CI without Alloy installed
// still either runs it via the docker shim or fails loudly rather than
// silently passing zero checks.
func AssertGoldensAgainstRealAlloy(t *testing.T, testdataDir string) {
	t.Helper()
	bin := AlloyBinary()
	if bin == "" {
		t.Fatal("no alloy binary and no docker to run the pinned image — this guard must not silently pass")
	}
	v := validate.New(&config.ValidateConfig{
		AlloyBinary:    bin,
		StabilityLevel: "experimental",
		Timeout:        2 * time.Minute,
	})

	goldens, err := filepath.Glob(filepath.Join(testdataDir, "*.golden.alloy"))
	if err != nil {
		t.Fatalf("glob goldens: %v", err)
	}
	if len(goldens) == 0 {
		t.Fatalf("no goldens found in %s — the guard would pass vacuously", testdataDir)
	}

	for _, g := range goldens {
		content, err := os.ReadFile(g) //nolint:gosec // g comes from filepath.Glob over a caller-fixed testdataDir, not external input
		if err != nil {
			t.Fatalf("read %s: %v", g, err)
		}
		res := v.Stages12(context.Background(), string(content))
		if !res.Valid {
			t.Errorf("%s is not accepted by alloy %s:\n%v\n--- config ---\n%s",
				g, version.AlloySchemaVersion, res.Diagnostics, content)
		}
	}
}
