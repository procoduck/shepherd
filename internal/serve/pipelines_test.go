package serve_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/merge"
	"shepherd/internal/serve"
	"shepherd/internal/store/sqlc"
)

func uuidFrom(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Bytes[15] = b
	u.Valid = true
	return u
}

// TestPipelinesFromRows pins the row -> merge.Pipeline conversion both serve
// paths share (#296): the current revision reaches Revision (and from there
// the header), a git row carries its repo link's collector, a legacy row with
// no revision keeps 0, and a row whose matchers do not decode is dropped.
func TestPipelinesFromRows(t *testing.T) {
	collector := uuidFrom(9)
	rows := []sqlc.ListEnabledPipelinesForMergeRow{
		{ID: uuidFrom(1), Name: "alpha", Contents: "// a", Matchers: json.RawMessage(`["cluster=\"c1\""]`), Source: "ui", CurrentRevision: 3},
		{ID: uuidFrom(2), Name: "from-git", Contents: "// g", Matchers: json.RawMessage(`[]`), Source: "git", CurrentRevision: 7, RepoLinkCollectorID: collector},
		{ID: uuidFrom(3), Name: "legacy", Contents: "// l", Matchers: json.RawMessage(`["cluster=\"c1\""]`), Source: "ui"},
		{ID: uuidFrom(4), Name: "broken", Contents: "// b", Matchers: json.RawMessage(`not json`), Source: "ui", CurrentRevision: 2},
	}

	got := serve.PipelinesFromRows(rows)
	want := []merge.Pipeline{
		{ID: uuidFrom(1).String(), Name: "alpha", Contents: "// a", Matchers: []string{`cluster="c1"`}, Source: "ui", Revision: 3},
		{ID: uuidFrom(2).String(), Name: "from-git", Contents: "// g", Matchers: []string{}, Source: "git", Revision: 7, RepoLinkCollectorID: collector.String()},
		{ID: uuidFrom(3).String(), Name: "legacy", Contents: "// l", Matchers: []string{`cluster="c1"`}, Source: "ui", Revision: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d pipelines, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ID != w.ID || g.Name != w.Name || g.Contents != w.Contents || g.Source != w.Source ||
			g.Revision != w.Revision || g.RepoLinkCollectorID != w.RepoLinkCollectorID ||
			strings.Join(g.Matchers, "|") != strings.Join(w.Matchers, "|") {
			t.Errorf("pipeline %d:\n got %+v\nwant %+v", i, g, w)
		}
	}
}

// TestComputeServed_HeaderCarriesEachRevision: the header lists every served
// pipeline with its own revision, and a revision bump alone moves the hash —
// which is why every serve path must fill Revision identically (a path that
// left it 0 would flip the hash back and forth between polls).
func TestComputeServed_HeaderCarriesEachRevision(t *testing.T) {
	alpha := scrapePipeline("p1", "alpha", []string{`cluster="c1"`})
	alpha.Revision = 3
	beta := scrapePipeline("p2", "beta", []string{`cluster="c1"`})
	beta.Revision = 12
	coll := serve.Collector{ID: "coll-1", Cluster: "c1", Role: "metrics"}

	got, err := serve.ComputeServed(context.Background(), serve.Deps{}, coll, []merge.Pipeline{alpha, beta})
	if err != nil {
		t.Fatalf("ComputeServed: %v", err)
	}
	for _, line := range []string{"//   - alpha (rev 3)\n", "//   - beta (rev 12)\n"} {
		if !strings.Contains(got.Content, line) {
			t.Errorf("header is missing %q:\n%s", line, got.Content)
		}
	}

	beta.Revision = 13
	bumped, err := serve.ComputeServed(context.Background(), serve.Deps{}, coll, []merge.Pipeline{alpha, beta})
	if err != nil {
		t.Fatalf("ComputeServed (bumped): %v", err)
	}
	if bumped.Hash == got.Hash {
		t.Fatal("a revision bump must change the served hash: the revision is part of the served bytes")
	}
}
