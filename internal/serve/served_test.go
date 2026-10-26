package serve_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/merge"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/serve"
)

// strictInline is the real served form of a strict org for text without an
// import.git (pinnedimport.Inline reads nothing for such text): only the
// __tenant_id__ drops after labelmaps (#410).
func strictInline(ctx context.Context, contents string) (string, error) {
	return pinnedimport.Inline(ctx, nil, pgtype.UUID{}, nil, contents)
}

// #410's hash stability: a non-strict org's pipelines, and a strict org's
// pipelines without a labelmap that may write __tenant_id__, are served
// byte-identical; the k8s-monitoring chart output reaches the metrics and
// logs collectors with a drop after each labelmap.
func TestStrictLabelmapDropServedThroughComputeServed(t *testing.T) {
	deps := serve.Deps{Schema: mustSchema(t)}
	plain := []merge.Pipeline{
		{ID: "1", Name: "plain", Contents: "logging { level = \"info\" }\n", Matchers: []string{}, Source: "ui"},
		{ID: "2", Name: "relabel", Contents: "loki.relabel \"r\" {\n  forward_to = []\n  rule {\n    action      = \"labelmap\"\n    regex       = \"x_team\"\n    replacement = \"team\"\n  }\n}\n", Matchers: []string{}, Source: "ui"},
	}
	coll := serve.Collector{ID: "00000000-0000-0000-0000-000000000003", Cluster: "c1", Role: "singleton"}
	before, err := serve.ComputeServed(context.Background(), deps, coll, plain)
	if err != nil {
		t.Fatal(err)
	}
	served, err := serve.ServedForm(context.Background(), strictInline, plain)
	if err != nil {
		t.Fatal(err)
	}
	after, err := serve.ComputeServed(context.Background(), deps, coll, served)
	if err != nil {
		t.Fatal(err)
	}
	if after.Content != before.Content || after.Hash != before.Hash {
		t.Errorf("a strict org's pipelines without a labelmap needing the drop changed:\n%s\n---\n%s", before.Content, after.Content)
	}

	for role, drops := range map[string]int{"metrics": 2, "logs": 1} {
		data, err := os.ReadFile("../signals/testdata/k8smon-" + role + ".alloy")
		if err != nil {
			t.Fatal(err)
		}
		p := []merge.Pipeline{{ID: "p1", Name: "k8s-monitoring", Contents: string(data), Matchers: []string{`cluster="c1"`}, Source: "ui"}}
		coll := serve.Collector{ID: "coll-1", Cluster: "c1", Role: role}

		raw, err := serve.ComputeServed(context.Background(), deps, coll, p)
		if err != nil {
			t.Fatal(err)
		}
		nonStrict, err := serve.ServedForm(context.Background(), nil, p)
		if err != nil {
			t.Fatal(err)
		}
		ns, err := serve.ComputeServed(context.Background(), deps, coll, nonStrict)
		if err != nil {
			t.Fatal(err)
		}
		if ns.Hash != raw.Hash || strings.Contains(ns.Content, `regex  = "__tenant_id__"`) {
			t.Errorf("%s: a non-strict org's served bytes must not change", role)
		}

		strict, err := serve.ServedForm(context.Background(), strictInline, p)
		if err != nil {
			t.Fatal(err)
		}
		if p[0].Contents != string(data) {
			t.Fatal("the stored text must not be modified")
		}
		res, err := serve.ComputeServed(context.Background(), deps, coll, strict)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Exclusions) != 0 {
			t.Fatalf("%s: excluded: %+v", role, res.Exclusions)
		}
		if got := strings.Count(res.Content, `regex  = "__tenant_id__"`); got != drops {
			t.Errorf("%s: %d drops served, want %d:\n%s", role, got, drops, res.Content)
		}
		if res.Hash == raw.Hash {
			t.Errorf("%s: the strict served form should differ from the stored one", role)
		}
	}
}

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
