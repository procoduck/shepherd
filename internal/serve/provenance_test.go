package serve_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"shepherd/internal/beacon"
	"shepherd/internal/merge"
	"shepherd/internal/serve"
)

// linesOf slices content by a 1-based inclusive line span.
func linesOf(t *testing.T, content string, s serve.LineSpan) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	if s.First < 1 || s.Last < s.First || s.Last > len(lines) {
		t.Fatalf("span %+v out of range for %d lines", s, len(lines))
	}
	return strings.Join(lines[s.First-1:s.Last], "\n")
}

// TestComputeServed_Provenance is F-CONTRIB (#423): ComputeServed returns,
// beside the served bytes, which pipelines contributed (with the line span of
// each one's block in THOSE bytes — the D6 baseline included and marked) and
// which matched but were excluded, and why. The spans are checked against the
// served text itself, baseline appended, so they cannot point at the merge
// step's text instead of what the collector receives.
func TestComputeServed_Provenance(t *testing.T) {
	alpha := scrapePipeline("id-alpha", "alpha", []string{`cluster="c1"`, `role="metrics"`})
	alpha.Revision = 3
	beta := scrapePipeline("id-beta", "beta", []string{`cluster=~"c.*"`})
	broken := scrapePipeline("id-broken", "broken", []string{`cluster=~"(`})
	refused := scrapePipeline("id-refused", "refused", []string{`cluster="c1"`})
	refused.TenantProblem = "2:3 tenant \"other\" is not one this org owns"
	elsewhere := scrapePipeline("id-else", "elsewhere", []string{`cluster="c2"`})
	coll := serve.Collector{ID: "coll-1", Cluster: "c1", Role: "metrics"}
	bl := beacon.NewBaselineConfig("https://shepherd.example.test" + beacon.WritePath)
	deps := serve.Deps{Schema: mustSchema(t), BeaconBaseline: bl}

	got, err := serve.ComputeServed(context.Background(), deps, coll, []merge.Pipeline{beta, refused, alpha, broken, elsewhere})
	if err != nil {
		t.Fatalf("ComputeServed: %v", err)
	}
	p := got.Provenance
	if len(p.Contributors) != 2 {
		t.Fatalf("contributors = %+v, want alpha and beta", p.Contributors)
	}
	for i, want := range []struct{ id, name string }{{"id-alpha", "alpha"}, {"id-beta", "beta"}} {
		c := p.Contributors[i]
		if c.PipelineID != want.id || c.Name != want.name {
			t.Fatalf("contributor %d = %+v, want %s", i, c, want.name)
		}
		block := linesOf(t, got.Content, c.Lines)
		label := merge.DeclareLabel(want.name)
		if !strings.HasPrefix(block, `declare "`+label+`" {`) || !strings.HasSuffix(block, label+` "default" { }`) {
			t.Fatalf("lines %+v of the served text are not %s's block:\n%s", c.Lines, want.name, block)
		}
		if c.Block != label {
			t.Fatalf("block = %q, want %q", c.Block, label)
		}
	}
	if c := p.Contributors[0]; c.Revision != 3 || c.Source != "ui" || len(c.Matchers) != 2 ||
		c.Matchers[1] != (serve.MatcherRecord{Matcher: `role="metrics"`, Label: "role", Value: "metrics", Present: true}) {
		t.Fatalf("alpha's record = %+v", c)
	}

	if p.Baseline == nil {
		t.Fatalf("baseline span missing although the baseline was appended")
	}
	baseline, err := beacon.RenderBaselinePipeline(func() beacon.BaselineConfig { b := bl; b.CollectorID = "coll-1"; return b }())
	if err != nil {
		t.Fatalf("rendering baseline: %v", err)
	}
	if got := linesOf(t, got.Content, *p.Baseline); got != strings.TrimRight(baseline, "\n") {
		t.Fatalf("baseline lines %+v are not the baseline:\n%s", *p.Baseline, got)
	}

	kinds := map[string]merge.ExclusionKind{}
	for _, ex := range p.Excluded {
		kinds[ex.PipelineID] = ex.Kind
		if ex.Reason == "" || ex.Name == "" {
			t.Fatalf("exclusion without name or reason: %+v", ex)
		}
	}
	want := map[string]merge.ExclusionKind{"id-broken": merge.ExclusionUnparsableMatcher, "id-refused": merge.ExclusionTenantCheck}
	if len(kinds) != len(want) || kinds["id-broken"] != want["id-broken"] || kinds["id-refused"] != want["id-refused"] {
		t.Fatalf("excluded kinds = %v, want %v", kinds, want)
	}

	// The stored form round-trips. The database stamps the record with the
	// row write's computed_at, in to_jsonb's format.
	at := time.Date(2026, 10, 28, 2, 40, 17, 123456000, time.UTC)
	p.ComputedAt = "2026-10-28T02:40:17.123456+00:00"
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, ok := serve.DecodeProvenance(raw, got.Hash, at)
	if !ok || len(back.Contributors) != 2 || back.Contributors[1].Lines != p.Contributors[1].Lines || back.Baseline == nil {
		t.Fatalf("decoded provenance = %+v (ok=%v)", back, ok)
	}
	if _, ok := serve.DecodeProvenance(nil, got.Hash, at); ok {
		t.Fatalf("a row with no provenance decoded as recorded")
	}
	// A row whose content a pre-0049 binary rewrote keeps the old record:
	// it describes other bytes, so it is not shown.
	if _, ok := serve.DecodeProvenance(raw, merge.HashContent("other content"), at); ok {
		t.Fatalf("provenance recorded for another hash decoded as this row's")
	}
	// A later write that kept the record (a pre-0049 replica's) moved
	// computed_at: the record no longer describes this write.
	if _, ok := serve.DecodeProvenance(raw, got.Hash, at.Add(time.Second)); ok {
		t.Fatalf("provenance stamped by an earlier write decoded as this row's")
	}
}

// TestComputeServed_ProvenanceRoleExclusionAndNoBaseline: a role exclusion
// carries the disallowed signals, and a deployment without the beacon has no
// baseline span. The served bytes are those of the golden — provenance is
// metadata only.
func TestComputeServed_ProvenanceRoleExclusionAndNoBaseline(t *testing.T) {
	logs := merge.Pipeline{
		ID: "id-logs", Name: "logs", Source: "ui", Matchers: []string{`cluster="c1"`},
		Contents: "loki.source.file \"pods\" {\n  targets    = []\n  forward_to = []\n}\n",
	}
	coll := serve.Collector{ID: "coll-1", Cluster: "c1", Role: "metrics"}
	got, err := serve.ComputeServed(context.Background(), serve.Deps{Schema: mustSchema(t)}, coll, []merge.Pipeline{logs})
	if err != nil {
		t.Fatalf("ComputeServed: %v", err)
	}
	if got.Provenance.Baseline != nil {
		t.Fatalf("baseline span %+v without a baseline", *got.Provenance.Baseline)
	}
	if len(got.Provenance.Contributors) != 0 {
		t.Fatalf("contributors = %+v", got.Provenance.Contributors)
	}
	ex := got.Provenance.Excluded
	if len(ex) != 1 || ex[0].Kind != merge.ExclusionRole || ex[0].PipelineID != "id-logs" ||
		len(ex[0].Disallowed) != 1 || ex[0].Disallowed[0] != "logs" {
		t.Fatalf("excluded = %+v, want logs excluded by role with disallowed [logs]", ex)
	}
}
