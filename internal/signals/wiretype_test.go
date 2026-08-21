package signals

import (
	"slices"
	"sort"
	"testing"

	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// testRegistry builds a *schema.Registry against the real, embedded schema
// artifact — the same construction internal/server and internal/mgmtapi use
// in production (schema.New(schema.Embedded, version.AlloySchemaVersion)) —
// so these tests exercise this package against the artifact a real build
// actually ships, not a hand-rolled stub.
func testRegistry(t *testing.T) *schema.Registry {
	t.Helper()
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}
	return reg
}

func artifactComponents(t *testing.T, reg *schema.Registry) map[string]any {
	t.Helper()
	merged, _, err := reg.Get(reg.CurrentVersion())
	if err != nil {
		t.Fatalf("reg.Get(%s): %v", reg.CurrentVersion(), err)
	}
	components, ok := merged["components"].(map[string]any)
	if !ok {
		t.Fatalf("schema %s: components is not a map", reg.CurrentVersion())
	}
	return components
}

// TestWireTypesExhaustive is the single most important test in this package.
// It loads the REAL, shipped schema artifact (not a fixture) and enumerates
// every distinct wire "type" string across every component's inputs[] and
// outputs[] arrays, then asserts wireTypeSignals classifies every one of
// them. A future Alloy version bump that introduces a wire type this
// package has never seen must make THIS test fail — loudly, at build time —
// rather than let classifyWireType silently return "no signal" for it via a
// missing-key zero value.
//
// It also pins the exact observed set (not just "every found type is
// classified") so a wire type silently disappearing from the artifact, or a
// new one appearing, is visible even before touching wireTypeSignals at all.
func TestWireTypesExhaustive(t *testing.T) {
	reg := testRegistry(t)
	components := artifactComponents(t, reg)

	found := map[string]bool{}
	for _, raw := range components {
		comp, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, wt := range wireTypesOf(comp) {
			found[wt] = true
		}
	}
	if len(found) == 0 {
		t.Fatalf("no wire types found in the artifact at all; the fixture or the extraction is broken")
	}

	gotObserved := make([]string, 0, len(found))
	for wt := range found {
		gotObserved = append(gotObserved, wt)
	}
	sort.Strings(gotObserved)

	// Observed against alloy-v1.18.1 at the time this package was written.
	wantObserved := []string{
		"loki.logs", "otel.any", "otel.logs", "otel.metrics", "otel.traces",
		"prom.metrics", "pyroscope.profiles", "targets",
	}
	if !slices.Equal(wantObserved, gotObserved) {
		t.Fatalf("wire types observed in the shipped artifact = %v, want exactly %v "+
			"(if this is a deliberate schema change, update wantObserved AND wireTypeSignals together, "+
			"with a rationale for the new/removed type)", gotObserved, wantObserved)
	}

	var unclassified []string
	for wt := range found {
		if _, ok := classifyWireType(wt); !ok {
			unclassified = append(unclassified, wt)
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		t.Fatalf("wire type(s) present in the shipped artifact have no entry in wireTypeSignals: %v — "+
			"a schema bump introduced a wire type this package does not classify; see wiretype.go", unclassified)
	}
}

// TestOtelAnyNeverCarriesProfiles substantiates wireTypeSignals' otel.any
// rationale against the real artifact: otel.any is classified as
// {Metrics, Logs, Traces} and deliberately excludes Profiles on the evidence
// that no component in this schema exposes both an otel.any port and a
// pyroscope.profiles port. If a future Alloy version adds an OTel-model
// profiling bridge, this test is what notices the assumption went stale.
func TestOtelAnyNeverCarriesProfiles(t *testing.T) {
	reg := testRegistry(t)
	components := artifactComponents(t, reg)

	for name, raw := range components {
		comp, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		types := wireTypesOf(comp)
		hasAny := slices.Contains(types, "otel.any")
		hasProfiles := slices.Contains(types, "pyroscope.profiles")
		if hasAny && hasProfiles {
			t.Fatalf("component %q declares both an otel.any port and a pyroscope.profiles port; "+
				"wiretype.go's otel.any rationale (excluding Profiles) no longer matches the schema "+
				"and must be revisited", name)
		}
	}
}

func TestClassifyWireType(t *testing.T) {
	cases := []struct {
		wireType string
		want     Set
		wantOK   bool
	}{
		{"prom.metrics", NewSet(Metrics), true},
		{"otel.metrics", NewSet(Metrics), true},
		{"loki.logs", NewSet(Logs), true},
		{"otel.logs", NewSet(Logs), true},
		{"otel.traces", NewSet(Traces), true},
		{"pyroscope.profiles", NewSet(Profiles), true},
		{"otel.any", NewSet(Metrics, Logs, Traces), true},
		{"targets", Set{}, true},
		{"bogus.wiretype", nil, false},
		{"", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.wireType, func(t *testing.T) {
			got, ok := classifyWireType(tc.wireType)
			if ok != tc.wantOK {
				t.Fatalf("classifyWireType(%q) ok = %v, want %v", tc.wireType, ok, tc.wantOK)
			}
			if ok && !got.Equal(tc.want) {
				t.Fatalf("classifyWireType(%q) = %s, want %s", tc.wireType, got, tc.want)
			}
		})
	}
}

func TestWireTypesOf(t *testing.T) {
	comp := map[string]any{
		"inputs": []any{
			map[string]any{"prop": "targets", "role": "accepts", "type": "targets"},
			map[string]any{"prop": "forward_to", "role": "produces", "type": "prom.metrics"},
		},
		"outputs": []any{
			map[string]any{"export": "input", "role": "accepts", "type": "otel.any"},
			// duplicate type across inputs/outputs must be de-duplicated
			map[string]any{"export": "targets", "role": "produces", "type": "targets"},
		},
	}
	got := wireTypesOf(comp)
	want := []string{"otel.any", "prom.metrics", "targets"}
	if len(got) != len(want) {
		t.Fatalf("wireTypesOf = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wireTypesOf = %v, want %v", got, want)
		}
	}
}
