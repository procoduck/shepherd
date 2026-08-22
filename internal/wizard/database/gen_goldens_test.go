//go:build ignore

package database_test

// This file is used to regenerate golden files — see
// internal/wizard/clustermetrics/gen_goldens_test.go for the exact
// procedure this repo's toolchain requires.

import (
	"os"
	"testing"

	db "shepherd/internal/wizard/database"
)

func TestGenGoldens(t *testing.T) {
	w := &db.Wizard{}
	cases := []struct {
		name  string
		state map[string]any
	}{
		{"postgres", map[string]any{
			"engine":            "postgres",
			"connection_env":    "APP_PG_DSN",
			"job_name":          "app-db",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
		}},
		{"mysql", map[string]any{
			"engine":            "mysql",
			"connection_env":    "APP_MYSQL_DSN",
			"job_name":          "app-db",
			"scrape_interval":   "30s",
			"metrics_dest_name": "prom-staging",
			"cluster_pattern":   "staging-.*",
		}},
		{"redis", map[string]any{
			"engine":            "redis",
			"connection_env":    "APP_REDIS_ADDR",
			"job_name":          "app-cache",
			"scrape_interval":   "15s",
			"metrics_dest_name": "prom-prod",
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
