package chartvalues_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"shepherd/internal/chartvalues"
)

// TestHelmTemplateCleanly is G9's second half: the generated values, layered
// onto the REAL pinned chart the way an operator actually would (`-f
// this-file.yaml`, alongside their own values), must `helm template`
// cleanly — no error, for every fixture (docs/gateway-tier-plan.md G9: "per
// feature combination").
//
// Deliberately NOT self-skipping when `helm` or network is unavailable,
// matching internal/wizard/appobservability's real-Alloy-binary test
// (gen_goldens_test.go's sibling, "Wizard output against the real Alloy
// binary"): a guard that quietly passes in an environment missing its
// prerequisite is worse than no guard. It fetches the exact PinnedChartVersion
// release via `helm pull` — the same artifact testdata/values.schema.json
// was vendored from — so a drift between the vendored schema and what the
// real chart's templates actually accept would show up here even if it
// somehow slipped past schema_test.go's checks.
//
// A second, small values fragment is layered alongside Render's output:
// exactly the one workaround render.go's RequiredOperatorExtraEnvVar and its
// header comment document as the operator's responsibility (a real chart
// quirk, not this package's own auth — see render.go's doc comment on that
// constant for why Render does not, and should not, supply it itself). This
// mirrors the actual delivery model (§5: "meant to sit alongside the
// operator's own -f values"), so the test proves what will really happen,
// not an artificially standalone case nobody deploys.
func TestHelmTemplateCleanly(t *testing.T) {
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("no helm binary on PATH — this guard must not silently pass; install helm to run it")
	}

	chartDir := pullPinnedChart(t, helmBin)

	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			spec := fixture(name)
			generated, err := chartvalues.Render(spec)
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			dir := t.TempDir()
			generatedPath := filepath.Join(dir, "shepherd-layer.yaml")
			if err := os.WriteFile(generatedPath, generated, 0o600); err != nil {
				t.Fatalf("writing generated values: %v", err)
			}

			supplementPath := filepath.Join(dir, "operator-supplement.yaml")
			if err := os.WriteFile(supplementPath, operatorExtraEnvSupplement(spec.Roles), 0o600); err != nil {
				t.Fatalf("writing operator supplement: %v", err)
			}

			cmd := exec.Command(helmBin, "template", "testrelease", chartDir,
				"-f", generatedPath, "-f", supplementPath)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("helm template failed: %v\n--- stderr ---\n%s", err, stderr.String())
			}

			out := stdout.String()
			if !containsAll(out, "remotecfg {", `url = "`+trimSlash(spec.ShepherdURL)+`"`) {
				t.Errorf("helm template output is missing the expected remotecfg block for %s:\n%s", name, out)
			}
		})
	}
}

// TestHelmTemplateFailsWithoutOperatorWorkaround is the red run backing
// render.go's RequiredOperatorExtraEnvVar claim and its header-comment
// instructions to the operator: applying Render's OWN output alone (no
// operator-supplement.yaml) must fail helm template with the chart's own
// GCLOUD_RW_API_KEY validation error. If a future PinnedChartVersion bump
// ever removes that requirement, this test goes red — which is the correct
// signal to delete the workaround and simplify render.go, not to touch this
// assertion.
func TestHelmTemplateFailsWithoutOperatorWorkaround(t *testing.T) {
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("no helm binary on PATH — this guard must not silently pass; install helm to run it")
	}
	chartDir := pullPinnedChart(t, helmBin)

	generated, err := chartvalues.Render(fixture("metrics-only"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	dir := t.TempDir()
	generatedPath := filepath.Join(dir, "shepherd-layer.yaml")
	if err := os.WriteFile(generatedPath, generated, 0o600); err != nil {
		t.Fatalf("writing generated values: %v", err)
	}

	cmd := exec.Command(helmBin, "template", "testrelease", chartDir, "-f", generatedPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		t.Fatal("expected helm template to fail without the operator's GCLOUD_RW_API_KEY workaround, but it succeeded — " +
			"either the chart no longer needs this (simplify render.go and its header comment) or this test regressed")
	}
	if !bytes.Contains(stderr.Bytes(), []byte(chartvalues.RequiredOperatorExtraEnvVar)) {
		t.Fatalf("helm template failed, but not with the expected %s error:\n%s",
			chartvalues.RequiredOperatorExtraEnvVar, stderr.String())
	}
}

func trimSlash(s string) string {
	if len(s) > 0 && s[len(s)-1] == '/' {
		return s[:len(s)-1]
	}
	return s
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !bytes.Contains([]byte(haystack), []byte(n)) {
			return false
		}
	}
	return true
}

// operatorExtraEnvSupplement builds the minimal values fragment satisfying
// render.go's RequiredOperatorExtraEnvVar for exactly the collectors this
// fixture's roles enable — scoped to those collectors deliberately: adding
// an extraEnv entry for a collector name Render never mentions would create
// a NEW, unwanted collectors.<name> entry (collectors.list.enabled iterates
// every key present under .Values.collectors), which would make this test
// pass for the wrong reason.
func operatorExtraEnvSupplement(roles []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("collectors:\n")
	for _, role := range roles {
		buf.WriteString("  alloy-" + role + ":\n")
		buf.WriteString("    alloy:\n")
		buf.WriteString("      extraEnv:\n")
		buf.WriteString("        - name: " + chartvalues.RequiredOperatorExtraEnvVar + "\n")
		buf.WriteString("          value: \"unused\"\n")
	}
	return buf.Bytes()
}

// pullPinnedChart fetches the exact PinnedChartVersion release of
// k8s-monitoring from Grafana's published chart repo into a fresh temp dir,
// once per test run, and returns the extracted chart directory. Failure is
// fatal, never a skip.
func pullPinnedChart(t *testing.T, helmBin string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(helmBin, "pull", "k8s-monitoring",
		"--repo", "https://grafana.github.io/helm-charts",
		"--version", chartvalues.PinnedChartVersion,
		"--untar", "-d", dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm pull k8s-monitoring@%s: %v\n--- stderr ---\n%s", chartvalues.PinnedChartVersion, err, stderr.String())
	}
	chartDir := filepath.Join(dir, "k8s-monitoring")
	if info, err := os.Stat(chartDir); err != nil || !info.IsDir() {
		t.Fatalf("helm pull did not produce %s", chartDir)
	}
	return chartDir
}
