package serve_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"shepherd/internal/beacon"
	"shepherd/internal/merge"
	"shepherd/internal/serve"
)

// TestComputeServed_ModuleInstanceReachesRestrictedRole is #378 through the
// real serve path (merge with role enforcement, the D6 baseline, Stage 1):
// the verbatim grafana/k8s-monitoring-rendered config (feature modules
// inlined as declares plus `<name> "feature"` instances, and the
// destination's OTLP bridge) assigned to a role=metrics or role=logs
// collector is served. Before, the module instance was an unknown component
// and the bridge's otel.any input counted as every OTLP signal, so both
// widened the pipeline and the role excluded it. The baseline stays outside
// every declare.
func TestComputeServed_ModuleInstanceReachesRestrictedRole(t *testing.T) {
	deps := serve.Deps{Schema: mustSchema(t), BeaconBaseline: beacon.NewBaselineConfig("https://shepherd.example.test" + beacon.WritePath)}
	for _, tc := range []struct {
		role, file, instance string
	}{
		{"metrics", "k8smon-metrics.alloy", `annotation_autodiscovery "feature"`},
		{"logs", "k8smon-logs.alloy", `pod_logs_via_loki "feature"`},
	} {
		t.Run(tc.role, func(t *testing.T) {
			data, err := os.ReadFile("../signals/testdata/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			p := merge.Pipeline{
				ID: "p1", Name: "k8s-monitoring", Contents: string(data),
				Matchers: []string{`cluster="c1"`}, Source: "ui",
			}
			coll := serve.Collector{ID: "coll-1", Cluster: "c1", Role: tc.role}
			got, err := serve.ComputeServed(context.Background(), deps, coll, []merge.Pipeline{p})
			if err != nil {
				t.Fatalf("ComputeServed: %v", err)
			}
			if len(got.Exclusions) != 0 {
				t.Fatalf("pipeline excluded from role %s: %+v", tc.role, got.Exclusions)
			}
			if !strings.Contains(got.Content, `declare "pipe_k8s_monitoring" {`) || !strings.Contains(got.Content, tc.instance) {
				t.Fatalf("served content lacks the pipeline or its module instance:\n%s", got.Content)
			}
			// The baseline is appended after the last pipeline's instance,
			// at the top level, not inside a declare.
			baseline := strings.Index(got.Content, "\nprometheus.remote_write \"")
			if baseline < 0 || baseline < strings.LastIndex(got.Content, `pipe_k8s_monitoring "default"`) {
				t.Fatalf("baseline missing or not after the pipelines:\n%s", got.Content)
			}
		})
	}
}

// TestComputeServed_ImportGitModuleStillExcluded: an instance of a module an
// import.git block loads cannot be classified from the text, so a restricted
// role still excludes the pipeline (worst case), naming the instance.
func TestComputeServed_ImportGitModuleStillExcluded(t *testing.T) {
	p := merge.Pipeline{
		ID: "p1", Name: "git-mods", Matchers: []string{`cluster="c1"`}, Source: "ui",
		Contents: `import.git "mods" {
  repository = "https://example.com/mods.git"
  revision   = "main"
  path       = "m.alloy"
}

mods.feature "x" { }
`,
	}
	coll := serve.Collector{ID: "coll-1", Cluster: "c1", Role: "metrics"}
	got, err := serve.ComputeServed(context.Background(), serve.Deps{Schema: mustSchema(t)}, coll, []merge.Pipeline{p})
	if err != nil {
		t.Fatalf("ComputeServed: %v", err)
	}
	if len(got.Exclusions) != 1 || !strings.Contains(got.Exclusions[0].Unproven, "mods.feature") {
		t.Fatalf("Exclusions = %+v, want git-mods excluded as unproven (mods.feature)", got.Exclusions)
	}
	if strings.Contains(got.Content, `declare "pipe_git_mods"`) {
		t.Fatalf("excluded pipeline was served:\n%s", got.Content)
	}
}
