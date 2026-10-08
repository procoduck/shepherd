package mgmtapi

import (
	"testing"

	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// roleExclusionWarnings is reached from RenderWizard, but no catalog wizard can
// trip it any more: since #289 every wizard emits a role matcher consistent
// with its signals (wizard.Register checks it). The helper stays as the safety
// net for a wizard that gets that wrong, so it is pinned here directly with a
// pipeline whose matchers select a role that refuses its signals.
func TestRoleExclusionWarnings(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	p := merge.Pipeline{
		ID:   "00000000-0000-4000-8000-000000000001",
		Name: "metrics-only",
		Contents: `prometheus.scrape "app" {
  targets    = [{"__address__" = "app:9090"}]
  forward_to = []
}
`,
		Matchers: []string{`cluster=~"prod-.*"`},
	}
	matched := []map[string]string{
		{"cluster": "prod-eu", "role": "logs", "id": "a"},
		{"cluster": "prod-eu", "role": "metrics", "id": "b"},
		{"cluster": "prod-us", "role": "logs", "id": "c"},
	}

	got := roleExclusionWarnings(reg, p, matched)
	want := "Excluded from 2 collector(s): prod-eu/logs, prod-us/logs — its signals (metrics) are not allowed on role logs."
	if len(got) != 1 || got[0] != want {
		t.Fatalf("roleExclusionWarnings = %q, want [%q]", got, want)
	}

	if got := roleExclusionWarnings(reg, p, matched[1:2]); len(got) != 0 {
		t.Fatalf("metrics collector only: got %q, want none", got)
	}
	if got := roleExclusionWarnings(nil, p, matched); got != nil {
		t.Fatalf("nil registry: got %q, want nil", got)
	}
}
