//go:build ignore

package selfmonitoring_test

// This file is used to regenerate golden files — see
// internal/wizard/clustermetrics/gen_goldens_test.go for the exact
// procedure this repo's toolchain requires.

import (
	"os"
	"testing"

	sm "shepherd/internal/wizard/selfmonitoring"
)

func TestGenGoldens(t *testing.T) {
	w := &sm.Wizard{}
	cases := []struct {
		name  string
		state map[string]any
	}{
		{"metrics-only", map[string]any{
			"job_name":          "alloy-self",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-prod",
			"logs_enabled":      false,
			"cluster_pattern":   "prod-.*",
		}},
		{"metrics-and-logs", map[string]any{
			"job_name":          "alloy-self",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-prod",
			"logs_enabled":      true,
			"log_path":          "/var/log/alloy/*.log",
			"logs_dest_name":    "loki-prod",
			"cluster_pattern":   "prod-.*",
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
