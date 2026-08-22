//go:build ignore

package podlogs_test

// This file is used to regenerate golden files via:
//   go test -run=TestGenGoldens ./internal/wizard/podlogs/  (after temporarily
//   stripping this build tag — see clustermetrics' sibling file for why
//   `-tags ignore` itself is avoided in this repo's toolchain).
// Not part of the normal test suite.

import (
	"os"
	"testing"

	pl "shepherd/internal/wizard/podlogs"
)

func TestGenGoldens(t *testing.T) {
	w := &pl.Wizard{}
	cases := []struct {
		name  string
		state map[string]any
	}{
		{"raw", map[string]any{
			"namespace_pattern": "prod-.*",
			"logs_dest_name":    "loki-prod",
			"cluster_pattern":   "prod-.*",
		}},
		{"json-format", map[string]any{
			"namespace_pattern": "staging-.*",
			"log_format":        "json",
			"logs_dest_name":    "loki-staging",
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
