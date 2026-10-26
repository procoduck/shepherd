package serve_test

import (
	"context"
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

// A check that cannot finish excludes the pipeline (fail closed) and is not
// cached: the next recompute checks it again.
func TestStrictTenantCheckFailsClosedWhenBusy(t *testing.T) {
	restore := validate.SetTenantLintDeadline(0)
	src := "logging { level = \"warn\" }\n// busy\n"
	problem := strictServing().TenantProblem(context.Background(), src)
	restore()
	if !strings.Contains(problem, "could not finish") {
		t.Fatalf("a busy check must exclude the pipeline, got %q", problem)
	}
	if again := strictServing().TenantProblem(context.Background(), src); again != "" {
		t.Errorf("a busy verdict must not be cached; got %q", again)
	}
}
