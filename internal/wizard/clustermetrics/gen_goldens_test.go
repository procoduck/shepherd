//go:build ignore

package clustermetrics_test

// This file is used to regenerate golden files via:
//   go test -run=GenGoldens -tags ignore ./internal/wizard/clustermetrics/
// Not part of the normal test suite. Mirrors
// internal/wizard/appobservability/gen_goldens_test.go.

import (
	"os"
	"testing"

	cm "shepherd/internal/wizard/clustermetrics"
)

func TestGenGoldens(t *testing.T) {
	w := &cm.Wizard{}
	cases := []struct {
		name  string
		state map[string]any
	}{
		{"basic", map[string]any{
			"job_name":          "cluster-metrics",
			"scrape_interval":   "60s",
			"docker_only":       false,
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
		}},
		{"docker-only", map[string]any{
			"job_name":          "docker-cluster",
			"scrape_interval":   "30s",
			"docker_only":       true,
			"metrics_dest_name": "prom-staging",
			"cluster_pattern":   "staging-.*",
		}},
	}
	for _, c := range cases {
		res, err := w.Commit(c.state)
		if err != nil {
			t.Fatal(err)
		}
		path := "testdata/" + c.name + ".golden.alloy"
		if err := os.WriteFile(path, []byte(res.Contents), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wrote", path)
	}
}
