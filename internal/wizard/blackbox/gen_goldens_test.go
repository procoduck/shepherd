//go:build ignore

package blackbox_test

// This file is used to regenerate golden files — see
// internal/wizard/clustermetrics/gen_goldens_test.go for the exact
// procedure this repo's toolchain requires.

import (
	"os"
	"testing"

	bb "shepherd/internal/wizard/blackbox"
)

func TestGenGoldens(t *testing.T) {
	w := &bb.Wizard{}
	cases := []struct {
		name  string
		state map[string]any
	}{
		{"http", map[string]any{
			"probe_targets":     "https://example.com,https://example.org",
			"module":            "http_2xx",
			"job_name":          "blackbox-http",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
		}},
		{"tcp", map[string]any{
			"probe_targets":     "db.internal:5432",
			"module":            "tcp_connect",
			"job_name":          "blackbox-tcp",
			"scrape_interval":   "30s",
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
