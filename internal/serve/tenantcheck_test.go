package serve_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"shepherd/internal/gateway"
	"shepherd/internal/merge"
	"shepherd/internal/serve"
	"shepherd/internal/tenancy"
	"shepherd/internal/validate"
)

// strictServing is the Serving of a strict org owning "acme", for text with
// no import.git (which reads nothing from a database).
func strictServing() *serve.Serving {
	return serve.NewServing(nil, tenancy.Policy{Strict: true, Owned: []gateway.OrgTenant{{Tenant: "acme", Kind: gateway.TenantExact}}})
}

// The serve-time strict tenant check changes served bytes only for a strict
// org with a pipeline it refuses: a non-strict org, and a strict org whose
// pipelines all pass, are served byte-identical to ServedForm alone.
func TestStrictTenantCheckHashStability(t *testing.T) {
	deps := serve.Deps{Schema: mustSchema(t)}
	coll := serve.Collector{ID: "00000000-0000-0000-0000-000000000005", Cluster: "c1", Role: "singleton"}
	clean := []merge.Pipeline{
		{ID: "1", Name: "plain", Contents: "logging { level = \"info\" }\n", Matchers: []string{`cluster="c1"`}, Source: "ui"},
		{ID: "2", Name: "owned", Contents: "loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n" +
			"  external_labels = { team = \"a\" }\n}\n", Matchers: []string{`cluster="c1"`}, Source: "ui"},
	}
	refused := append(append([]merge.Pipeline{}, clean...), merge.Pipeline{
		ID: "3", Name: "computed", Matchers: []string{`cluster="c1"`}, Source: "ui",
		Contents: "loki.write \"c\" {\n  endpoint {\n    url = \"https://l.example/push\"\n  }\n  external_labels = encoding.from_json(\"{}\")\n}\n",
	})
	ctx := context.Background()
	for _, k8s := range []string{"logs", "metrics"} {
		data, err := os.ReadFile("../signals/testdata/k8smon-" + k8s + ".alloy")
		if err != nil {
			t.Fatal(err)
		}
		clean = append(clean, merge.Pipeline{ID: "k8s-" + k8s, Name: "k8smon-" + k8s, Contents: string(data), Matchers: []string{`cluster="c1"`}, Source: "ui"})
	}

	for name, c := range map[string]struct {
		p       []merge.Pipeline
		serving *serve.Serving
	}{
		"non-strict, with a pipeline the check would refuse": {refused, nil},
		"strict, every pipeline passing":                     {clean, strictServing()},
	} {
		want, err := serve.ServedForm(ctx, servedInline(c.serving), c.p)
		if err != nil {
			t.Fatal(err)
		}
		wantRes, err := serve.ComputeServed(ctx, deps, coll, want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.serving.Form(ctx, c.p)
		if err != nil {
			t.Fatal(err)
		}
		gotRes, err := serve.ComputeServed(ctx, deps, coll, got)
		if err != nil {
			t.Fatal(err)
		}
		if gotRes.Hash != wantRes.Hash || gotRes.Content != wantRes.Content {
			t.Errorf("%s: served bytes changed:\n%s\n---\n%s", name, wantRes.Content, gotRes.Content)
		}
		if len(gotRes.Exclusions) != 0 {
			t.Errorf("%s: excluded %+v", name, gotRes.Exclusions)
		}
	}

	got, err := strictServing().Form(ctx, refused)
	if err != nil {
		t.Fatal(err)
	}
	res, err := serve.ComputeServed(ctx, deps, coll, got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "// Excluded (1) - strict tenant check:\n//   - computed: ") ||
		strings.Contains(res.Content, `declare "pipe_computed"`) || !strings.Contains(res.Content, `declare "pipe_owned"`) {
		t.Errorf("the refused pipeline must be left out and named in the header:\n%s", res.Content)
	}
	if refused[2].TenantProblem != "" {
		t.Error("the input must not be modified")
	}
}

// servedInline is what ServedForm inlines for serving: nil (stored text) for
// an org not in strict mode, the strict served form otherwise.
func servedInline(s *serve.Serving) serve.Inliner {
	if s == nil {
		return nil
	}
	return strictInline
}

// A check that cannot finish fails the load (the collector keeps its last
// good config through RecordFailure) rather than serving a config without
// the pipeline, and is not cached: the next recompute checks it again.
func TestStrictTenantCheckBusyFailsTheLoad(t *testing.T) {
	restore := validate.SetTenantLintDeadline(0)
	src := "logging { level = \"warn\" }\n// busy\n"
	p := []merge.Pipeline{{ID: "1", Name: "p", Contents: src, Matchers: []string{`cluster="c1"`}, Source: "ui"}}
	_, err := strictServing().Form(context.Background(), p)
	restore()
	if !errors.Is(err, validate.ErrLintBusy) {
		t.Fatalf("a busy check must fail the load with ErrLintBusy, got %v", err)
	}
	got, err := strictServing().Form(context.Background(), p)
	if err != nil || got[0].TenantProblem != "" {
		t.Errorf("a busy verdict must not be cached; got %v, %q", err, got[0].TenantProblem)
	}
}

// Strict tenant mode accepts only classified components (#421): a stored
// pipeline using a refused one is excluded at serve time, and the verdict
// cache is keyed by the org's admin-allowed components, so allowing the
// component (or taking it off again) changes the verdict on the same text.
func TestStrictTenantCheckComponentsAndTheCacheKey(t *testing.T) {
	ctx := context.Background()
	src := "otelcol.auth.google \"g\" { }\n"
	p := tenancy.Policy{Strict: true, Owned: []gateway.OrgTenant{{Tenant: "acme", Kind: gateway.TenantExact}}}
	pipes := []merge.Pipeline{{ID: "1", Name: "google", Contents: src, Matchers: []string{`cluster="c1"`}, Source: "ui"}}
	verdict := func(p tenancy.Policy) string {
		t.Helper()
		got, err := serve.NewServing(nil, p).Form(ctx, pipes)
		if err != nil {
			t.Fatal(err)
		}
		return got[0].TenantProblem
	}
	if v := verdict(p); !strings.Contains(v, validate.StrictComponentRefusal) {
		t.Fatalf("a refused component must exclude the pipeline, got %q", v)
	}
	allowed := p
	allowed.AllowedComponents = []string{"otelcol.auth.google"}
	if v := verdict(allowed); v != "" {
		t.Errorf("an admin-allowed component must not exclude the pipeline (cached verdict reused?), got %q", v)
	}
	if v := verdict(p); !strings.Contains(v, validate.StrictComponentRefusal) {
		t.Errorf("taking the component off again must exclude the pipeline again, got %q", v)
	}
	still := allowed
	pipes[0].Contents = "otelcol.auth.google \"g\" { }\nloki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n" +
		"    tenant_id = \"globex\"\n  }\n}\n"
	if v := verdict(still); !strings.Contains(v, `tenant "globex" is not one this organisation owns`) {
		t.Errorf("every other check still applies with the component allowed, got %q", v)
	}
}

// A verdict that read pinned modules is not cached: the module cache can
// change (a module fetched later) without the text or the policy changing.
func TestStrictTenantCheckDoesNotCacheModuleVerdicts(t *testing.T) {
	src := "import.git \"m\" {\n  repository = \"https://git.example.com/m.git\"\n  revision   = \"0123456789abcdef0123456789abcdef01234567\"\n  path       = \"m.alloy\"\n}\n"
	if serve.CacheableVerdict(src) {
		t.Error("a pipeline with import.git must not have its verdict cached")
	}
	if !serve.CacheableVerdict("logging { level = \"info\" }\n") {
		t.Error("a pipeline without modules should be cached")
	}
}
