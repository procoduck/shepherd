package serve_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"shepherd/internal/merge"
	"shepherd/internal/serve"
)

// fakeInline rewrites the one import.git block the tests use.
func fakeInline(_ context.Context, contents string) (string, error) {
	if strings.Contains(contents, "BROKEN") {
		return "", errors.New("module not cached")
	}
	return strings.ReplaceAll(contents, `import.git "m" { }`, `import.string "m" { content = "" }`), nil
}

// TestServedFormLeavesOtherPipelinesByteIdentical is the hash-stability
// guarantee of #390's inlining: a pipeline without an import.git block — and
// every pipeline of a non-strict org (nil inliner) — reaches ComputeServed
// exactly as stored, so its served bytes and hash do not move; only a strict
// org's pipeline that uses a pinned import changes.
func TestServedFormLeavesOtherPipelinesByteIdentical(t *testing.T) {
	pipelines := []merge.Pipeline{
		{ID: "1", Name: "plain", Contents: "logging { level = \"info\" }\n", Matchers: []string{}, Source: "ui"},
		{ID: "2", Name: "comment", Contents: "// import.git mentioned only here\nlogging { level = \"warn\" }\n", Matchers: []string{}, Source: "ui"},
	}
	deps := serve.Deps{Schema: mustSchema(t)}
	coll := serve.Collector{ID: "00000000-0000-0000-0000-000000000001", Cluster: "c1", Role: "metrics"}

	before, err := serve.ComputeServed(context.Background(), deps, coll, pipelines)
	if err != nil {
		t.Fatal(err)
	}
	for name, inline := range map[string]func(context.Context, string) (string, error){"strict": fakeInline, "not strict": nil} {
		served, err := serve.ServedForm(context.Background(), inline, pipelines)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		after, err := serve.ComputeServed(context.Background(), deps, coll, served)
		if err != nil {
			t.Fatal(err)
		}
		if after.Content != before.Content || after.Hash != before.Hash {
			t.Errorf("%s: served bytes changed for pipelines without import.git:\n%s\n---\n%s", name, before.Content, after.Content)
		}
	}
}

func TestServedFormInlinesAStrictOrgsPinnedImportAndFailsClosed(t *testing.T) {
	pinned := []merge.Pipeline{{ID: "1", Name: "p", Contents: "import.git \"m\" { }\n", Matchers: []string{}, Source: "ui"}}
	served, err := serve.ServedForm(context.Background(), fakeInline, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(served[0].Contents, "import.git") || pinned[0].Contents != "import.git \"m\" { }\n" {
		t.Errorf("served %q, stored %q: want the served form inlined and the input untouched", served[0].Contents, pinned[0].Contents)
	}
	if out, err := serve.ServedForm(context.Background(), nil, pinned); err != nil || out[0].Contents != pinned[0].Contents {
		t.Errorf("a non-strict org's import.git passes through unchanged, got %q", out[0].Contents)
	}
	broken := []merge.Pipeline{{ID: "2", Name: "b", Contents: "import.git \"m\" { } // BROKEN\n", Matchers: []string{}, Source: "ui"}}
	if _, err := serve.ServedForm(context.Background(), fakeInline, broken); err == nil {
		t.Error("a pinned import that cannot be inlined must fail the serve, never pass through")
	}
}

// Role enforcement runs on the served form (#390 with #406): an instance of a
// pinned import.git module is Unknown — worst case, dropped from restricted
// roles — but the inlined import.string module carries its declare body's
// signals, so a metrics-only module reaches a role=metrics collector.
func TestServedFormReachesARestrictedRoleThroughComputeServed(t *testing.T) {
	module := "declare \"out\" {\n  prometheus.remote_write \"w\" {\n    endpoint {\n      url = \"https://mimir.example.com/api/v1/push\"\n    }\n  }\n}\n"
	stored := "import.git \"m\" {\n  repository = \"https://git.example.com/team/modules.git\"\n" +
		"  revision   = \"0123456789abcdef0123456789abcdef01234567\"\n  path       = \"m.alloy\"\n}\nm.out \"x\" { }\n"
	inline := func(_ context.Context, contents string) (string, error) {
		i := strings.Index(contents, "m.out")
		return "import.string \"m\" {\n  content = " + strconv.Quote(module) + "\n}\n" + contents[i:], nil
	}
	pipelines := []merge.Pipeline{{ID: "1", Name: "pinned", Contents: stored, Matchers: []string{`cluster="c1"`}, Source: "ui"}}
	deps := serve.Deps{Schema: mustSchema(t)}
	coll := serve.Collector{ID: "00000000-0000-0000-0000-000000000002", Cluster: "c1", Role: "metrics"}

	raw, err := serve.ComputeServed(context.Background(), deps, coll, pipelines)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Exclusions) != 1 {
		t.Fatalf("the stored import.git form should be excluded from role=metrics (Unknown), got %v", raw.Exclusions)
	}
	served, err := serve.ServedForm(context.Background(), inline, pipelines)
	if err != nil {
		t.Fatal(err)
	}
	res, err := serve.ComputeServed(context.Background(), deps, coll, served)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Exclusions) != 0 || !strings.Contains(res.Content, `import.string "m"`) {
		t.Errorf("the inlined metrics-only module should reach role=metrics; exclusions %v, content:\n%s", res.Exclusions, res.Content)
	}
}
