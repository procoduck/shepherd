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

	// The matched-collector list RenderWizard and PreviewMatches return marks
	// the same collectors, with the same reason, per collector.
	items := matchedCollectorsProto(reg, p, matched)
	wantReasons := []string{"its signals (metrics) are not allowed on role logs", "", "its signals (metrics) are not allowed on role logs"}
	if len(items) != len(wantReasons) {
		t.Fatalf("matchedCollectorsProto returned %d items, want %d", len(items), len(wantReasons))
	}
	for i, it := range items {
		if it.GetId() != matched[i]["id"] || it.GetExcludedReason() != wantReasons[i] {
			t.Fatalf("item %d = {id %q, excluded_reason %q}, want {id %q, excluded_reason %q}",
				i, it.GetId(), it.GetExcludedReason(), matched[i]["id"], wantReasons[i])
		}
	}
	for _, it := range matchedCollectorsProto(nil, p, matched) {
		if it.GetExcludedReason() != "" {
			t.Fatalf("nil registry: excluded_reason %q, want empty", it.GetExcludedReason())
		}
	}
}

// Several collectors can share a cluster and role; the warning counts them
// rather than repeating "prod/logs" once per collector.
func TestRoleExclusionWarnings_CountsRepeatedClusterRole(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	p := merge.Pipeline{
		Name: "metrics-only",
		Contents: `prometheus.scrape "app" {
  targets    = [{"__address__" = "app:9090"}]
  forward_to = []
}
`,
	}
	matched := []map[string]string{
		{"cluster": "prod", "role": "logs", "id": "a"},
		{"cluster": "dev", "role": "logs", "id": "b"},
		{"cluster": "prod", "role": "logs", "id": "c"},
		{"cluster": "prod", "role": "logs", "id": "d"},
	}
	got := roleExclusionWarnings(reg, p, matched)
	want := "Excluded from 4 collector(s): prod/logs ×3, dev/logs — its signals (metrics) are not allowed on role logs."
	if len(got) != 1 || got[0] != want {
		t.Fatalf("roleExclusionWarnings = %q, want [%q]", got, want)
	}
}

// A pipeline whose signal set cannot be proven is excluded on the assumed
// worst case; every operator-facing text must say so rather than claim the
// pipeline carries all of those signals.
func TestRoleExclusion_UnprovenSignalSetKeepsTheNote(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	p := merge.Pipeline{
		Name: "mystery",
		Contents: `totally.bogus.component "x" {
  foo = "bar"
}
`,
	}
	matched := []map[string]string{{"cluster": "prod", "role": "metrics", "id": "a"}}
	reason := "its signals (logs, traces, profiles) are not allowed on role metrics" +
		" (signal set not provable: unknown components [totally.bogus.component], unclassified wire types [] — assumed worst-case)"

	items := matchedCollectorsProto(reg, p, matched)
	if len(items) != 1 || items[0].GetExcludedReason() != reason {
		t.Fatalf("excluded_reason = %q, want %q", items[0].GetExcludedReason(), reason)
	}
	got := roleExclusionWarnings(reg, p, matched)
	want := "Excluded from 1 collector(s): prod/metrics — " + reason + "."
	if len(got) != 1 || got[0] != want {
		t.Fatalf("roleExclusionWarnings = %q, want [%q]", got, want)
	}
}
