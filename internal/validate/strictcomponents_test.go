package validate_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/beacon"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
)

// shippedComponents is every component of every shipped schema, with the
// schemas that have it.
func shippedComponents(t *testing.T) (map[string][]string, string) {
	t.Helper()
	files, err := fs.Glob(schema.Embedded, "artifacts/alloy-v*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("listing the shipped schemas: %v (%d)", err, len(files))
	}
	sort.Strings(files)
	out := map[string][]string{}
	for _, f := range files {
		data, err := fs.ReadFile(schema.Embedded, f)
		if err != nil {
			t.Fatal(err)
		}
		var art struct {
			Components map[string]json.RawMessage `json:"components"`
		}
		if err := json.Unmarshal(data, &art); err != nil {
			t.Fatal(err)
		}
		for name := range art.Components {
			out[name] = append(out[name], f)
		}
	}
	return out, files[len(files)-1]
}

// TestEveryShippedComponentIsClassified is the repo test of #421: a schema
// bump that brings a component nobody has classified fails here, as does a
// classification of a component no schema has, a checked entry that names
// no check, a check that does not exist, or one that does not cover the
// component.
func TestEveryShippedComponentIsClassified(t *testing.T) {
	shipped, _ := shippedComponents(t)
	classes, err := validate.ComponentClassifications()
	if err != nil {
		t.Fatalf("reading the classifications: %v", err)
	}
	if len(shipped) < 150 {
		t.Fatalf("found only %d shipped components; the schema walk is broken", len(shipped))
	}
	for name := range shipped {
		if _, ok := classes[name]; !ok {
			t.Errorf("%s (in %v) has no strict classification: add components.%q.strict to "+
				"internal/schema/artifacts/overlay.json (strict_retired if the pinned schema no longer has it)",
				name, shipped[name], name)
		}
	}
	for name, c := range classes {
		if _, ok := shipped[name]; !ok {
			t.Errorf("%s is classified but no shipped schema has it", name)
		}
		if strings.TrimSpace(c.Reason) == "" || strings.ContainsAny(c.Reason, "\n\r") {
			t.Errorf("%s: the reason must be one non-empty line, got %q", name, c.Reason)
		}
		switch c.Class {
		case validate.ComponentChecked:
			if len(c.Checks) == 0 {
				t.Errorf("%s is checked but names no check (one of %v)", name, validate.StrictCheckNames())
			}
			for _, check := range c.Checks {
				known, covers := validate.StrictCheckCovers(check, name)
				switch {
				case !known:
					t.Errorf("%s names check %q, which does not exist (one of %v)", name, check, validate.StrictCheckNames())
				case !covers:
					t.Errorf("%s names check %q, which does not cover it", name, check)
				}
			}
		case validate.ComponentAllowed, validate.ComponentRefused:
			if len(c.Checks) > 0 {
				t.Errorf("%s is %s but names checks %v; only a checked entry names checks", name, c.Class, c.Checks)
			}
		}
	}
}

// TestStrictRetiredIsOnlyForRetiredComponents: strict_retired holds a
// component only an older shipped schema has; the pinned one's are
// classified in components (readClassifications refuses one in both).
func TestStrictRetiredIsOnlyForRetiredComponents(t *testing.T) {
	shipped, pinned := shippedComponents(t)
	data, err := fs.ReadFile(schema.Embedded, "artifacts/overlay.json")
	if err != nil {
		t.Fatal(err)
	}
	var ov struct {
		Retired map[string]json.RawMessage `json:"strict_retired"`
	}
	if err := json.Unmarshal(data, &ov); err != nil {
		t.Fatal(err)
	}
	for name := range ov.Retired {
		for _, f := range shipped[name] {
			if f == pinned {
				t.Errorf("%s is in strict_retired but the pinned schema %s has it; classify it in components", name, pinned)
			}
		}
	}
}

var _ = Describe("strict tenant mode accepts only classified components (#421)", func() {
	refused := func(src string, p validate.TenantPolicy) []string {
		var out []string
		for _, m := range errorsOf(validate.TenantLint(src, p)) {
			if strings.Contains(m, validate.StrictComponentRefusal) {
				out = append(out, m)
			}
		}
		return out
	}
	const google = "otelcol.auth.google \"g\" {\n}\n"
	const unknown = "loki.source.future \"f\" {\n  forward_to = []\n}\n"

	It("refuses a component classified refused, naming it and how to allow it", func() {
		c, ok := validate.ClassifyComponent("otelcol.auth.google")
		Expect(ok).To(BeTrue())
		Expect(c.Class).To(Equal(validate.ComponentRefused))
		Expect(refused(google, strictFor())).To(ConsistOf(SatisfyAll(
			ContainSubstring(`otelcol.auth.google "g"`),
			ContainSubstring("classified refused (token_header"),
			ContainSubstring("an application administrator can allow otelcol.auth.google for this organisation"),
		)))
	})

	It("refuses a component no shipped schema classifies", func() {
		Expect(refused(unknown, strictFor())).To(ConsistOf(SatisfyAll(
			ContainSubstring(`loki.source.future "f"`), ContainSubstring("has none"),
		)))
	})

	It("accepts classified allowed and checked components, and does nothing outside strict mode", func() {
		src := "prometheus.exporter.self \"s\" { }\nloki.echo \"e\" { }\n"
		Expect(validate.TenantLint(src, strictFor())).To(BeEmpty())
		Expect(validate.TenantLint(google+unknown, validate.TenantPolicy{})).To(BeEmpty())
	})

	It("refuses one everywhere the lint walks: a declare, a nested declare, a foreach template, a literal module", func() {
		inDeclare := "declare \"d\" {\n" + google + "}\nd \"x\" { }\n"
		nested := "declare \"a\" {\n  declare \"b\" {\n" + google + "  }\n  b \"y\" { }\n}\na \"x\" { }\n"
		template := "foreach \"f\" {\n  collection = [1]\n  var = \"i\"\n  template {\n" + google + "  }\n}\n"
		module := "import.string \"m\" {\n  content = " + `"declare \"inner\" {\n  otelcol.auth.google \"g\" { }\n}\n"` +
			"\n}\nm.inner \"x\" { }\n"
		for _, src := range []string{inDeclare, nested, template, module} {
			Expect(refused(src, strictFor())).To(HaveLen(1), src)
		}
	})

	It("refuses one in a pinned import.git module", func() {
		m := &fakeModules{
			allowed: map[string]bool{repoA: true},
			modules: map[validate.GitModule]string{{Repository: repoA, Revision: shaA, Path: "m.alloy"}: "declare \"out\" {\n" + google + "}\n"},
		}
		d := lintGit(gitImport("mod", repoA, shaA, "m.alloy")+"mod.out \"x\" { }\n", m)
		Expect(errorsOf(d)).To(ConsistOf(SatisfyAll(ContainSubstring(`import.git "mod" line 2`),
			ContainSubstring(validate.StrictComponentRefusal))))
	})

	It("judges custom components by their bodies: declare and module instances are not refused", func() {
		src := "declare \"writer\" {\n  loki.echo \"e\" { }\n  argument \"x\" { optional = true }\n  export \"y\" { value = 1 }\n}\n" +
			"writer \"w\" { }\n" +
			"import.string \"lib\" {\n  content = " + `"declare \"echo\" {\n  loki.echo \"e\" { }\n}\n"` + "\n}\n" +
			"lib.echo \"x\" { }\n" +
			"declare \"outer\" {\n  writer \"inner\" { }\n  lib.echo \"y\" { }\n}\nouter \"o\" { }\n"
		Expect(validate.TenantLint(src, strictFor())).To(BeEmpty())
	})

	It("judges only dotted names: Alloy's own blocks are Stage 2's", func() {
		Expect(validate.TenantLint("logging { level = \"info\" }\n", strictFor())).To(BeEmpty())
	})

	It("accepts a component an application administrator allowed for the org", func() {
		p := strictFor()
		p.AllowedComponents = map[string]bool{"otelcol.auth.google": true, "loki.source.future": true}
		Expect(validate.TenantLint(google+unknown, p)).To(BeEmpty())
	})

	It("still runs every other check on an admin-allowed component", func() {
		p := strictFor()
		p.AllowedComponents = map[string]bool{"loki.source.future": true, "otelcol.exporter.future": true}
		labels := "loki.source.future \"f\" {\n  forward_to = []\n  labels = { \"__tenant_id__\" = \"globex\" }\n}\n"
		headers := "otelcol.exporter.future \"e\" {\n  client {\n    headers = { \"X-Scope-OrgID\" = \"globex\" }\n  }\n}\n"
		rule := "loki.source.future \"r\" {\n  rule {\n    target_label = \"__tenant_id__\"\n    replacement  = \"globex\"\n  }\n}\n"
		for _, src := range []string{labels, headers, rule} {
			Expect(errorsOf(validate.TenantLint(src, p))).To(ConsistOf(ContainSubstring(`tenant "globex" is not one this organisation owns`)), src)
		}
	})

	It("does not let a refused component make TenantLiterals incomplete", func() {
		lits, complete, err := validate.TenantLiterals(google + writerModule("acme"))
		Expect(err).NotTo(HaveOccurred())
		Expect(complete).To(BeTrue())
		Expect(lits).To(ConsistOf("acme"))
	})

	It("is the same check on the served form a strict org is served", func() {
		p := strictFor()
		p.Inline = func(_ context.Context, s string) (string, error) { return s, nil }
		r := validate.WithTenantLint(context.Background(), validate.Result{Valid: true}, google, p)
		Expect(r.Valid).To(BeFalse())
		Expect(errorsOf(r.Diagnostics)).To(ContainElement(ContainSubstring(validate.StrictComponentRefusal)))
	})

	Describe("Shepherd's own pipelines and the k8s-monitoring chart's", func() {
		files := func() []string {
			var out []string
			for _, pattern := range []string{
				"../signals/testdata/k8smon-*.alloy",
				"../visual/testdata/k8s-monitoring/*.alloy",
				"../wizard/testdata/*.alloy",
				"../wizard/*/testdata/*.alloy",
				"../cli/seedfixtures/*.alloy",
			} {
				m, err := filepath.Glob(pattern)
				Expect(err).NotTo(HaveOccurred())
				Expect(m).NotTo(BeEmpty(), pattern)
				out = append(out, m...)
			}
			return out
		}

		It("use only components strict tenant mode accepts", func() {
			for _, f := range files() {
				b, err := os.ReadFile(f)
				Expect(err).NotTo(HaveOccurred())
				Expect(validate.StrictComponentRefusals(string(b))).To(BeEmpty(), f)
			}
		})

		It("include the D6 baseline Shepherd appends to every served config", func() {
			for _, cfg := range []beacon.BaselineConfig{
				beacon.NewBaselineConfig("https://shepherd.example.com/beacon/v1/write"),
				beacon.NewBaselineConfigOAuth2("https://shepherd.example.com/beacon/v1/write", "https://idp.example.com/token", []string{"s"}),
			} {
				text, err := beacon.RenderBaselinePipeline(cfg)
				Expect(err).NotTo(HaveOccurred())
				Expect(text).To(ContainSubstring("prometheus.remote_write"))
				Expect(validate.StrictComponentRefusals(text)).To(BeEmpty())
			}
		})

		It("keep passing the k8s-monitoring fixtures in strict mode", func() {
			for _, name := range []string{"metrics", "logs"} {
				Expect(errorsOf(validate.TenantLint(insertDrops(k8smon(name)), ownsAll()))).To(BeEmpty(), name)
			}
		})
	})

	Describe("CheckAllowableComponent", func() {
		It("accepts a refused or unclassified component name", func() {
			Expect(validate.CheckAllowableComponent("otelcol.auth.google")).To(Succeed())
			Expect(validate.CheckAllowableComponent("loki.source.future")).To(Succeed())
		})
		It("refuses a component strict mode already accepts, and anything not a component name", func() {
			for _, s := range []string{
				"loki.write", "prometheus.exporter.self", "", "loki", "Loki.write", "loki..write",
				"loki.write ", "1oki.write", "declare", strings.Repeat("a.", 70) + "b",
			} {
				Expect(validate.CheckAllowableComponent(s)).To(MatchError(validate.ErrComponentNotAllowable), s)
			}
		})
	})
})

var _ = Describe("strict tenant mode reads stage.truncate's label rules (#421)", func() {
	process := func(rule string) string {
		return "loki.process \"p\" {\n  forward_to = []\n  stage.match {\n    selector = \"{a=\\\"b\\\"}\"\n" +
			"    stage.truncate {\n      rule {\n        limit = \"8B\"\n" + rule + "      }\n    }\n  }\n}\n"
	}

	It("accepts line and extracted rules, and label rules listing other labels", func() {
		for _, rule := range []string{
			"",
			"        source_type = \"line\"\n",
			"        source_type = \"extracted\"\n",
			"        source_type = \"label\"\n        sources     = [\"app\", \"pod\"]\n",
		} {
			Expect(validate.TenantLint(process(rule), strictFor())).To(BeEmpty(), rule)
		}
	})

	It("refuses a label rule that may rewrite __tenant_id__, and a source_type it cannot read", func() {
		for rule, want := range map[string]string{
			"        source_type = \"label\"\n":                                        "list the labels in sources",
			"        source_type = \"label\"\n        sources = []\n":                  "list the labels in sources",
			"        source_type = \"label\"\n        sources = [\"__tenant_id__\"]\n": "truncates __tenant_id__",
			"        source_type = \"label\"\n        sources = argument.s.value\n":    "list the labels in sources",
			"        source_type = sys.env(\"T\")\n":                                   "source_type cannot be verified",
		} {
			Expect(errorsOf(validate.TenantLint(process(rule), strictFor()))).To(ConsistOf(ContainSubstring(want)), rule)
		}
	})
})

var _ = Describe("strict tenant mode reads prometheus.exporter.blackbox's modules (#421)", func() {
	blackbox := func(attr string) string {
		return "prometheus.exporter.blackbox \"b\" {\n  " + attr + "\n  target {\n    name    = \"t\"\n" +
			"    address = \"https://example.com\"\n    module  = \"http_2xx\"\n  }\n}\n"
	}
	cfg := func(yaml string) string { return blackbox("config = " + quote(yaml)) }

	It("accepts the blackbox wizard's modules", func() {
		Expect(validate.TenantLint(cfg("{ modules: { http_2xx: { prober: http, timeout: 5s } } }"), strictFor())).To(BeEmpty())
		Expect(validate.TenantLint(cfg("{ modules: { tcp_connect: { prober: tcp, timeout: 5s } } }"), strictFor())).To(BeEmpty())
		Expect(validate.TenantLint(blackbox(""), strictFor())).To(BeEmpty())
	})

	It("refuses a module naming X-Scope-OrgID, however YAML spells it", func() {
		for _, y := range []string{
			"modules:\n  h:\n    prober: http\n    http:\n      headers:\n        X-Scope-OrgID: globex\n",
			"modules:\n  h:\n    prober: http\n    http:\n      headers:\n        \"x-scope-org\\u0069d\": acme\n",
			"modules:\n  h:\n    prober: http\n    http:\n      http_client_config:\n        http_headers:\n          x-SCOPE-orgid:\n            values: [globex]\n",
			"modules:\n  h:\n    prober: http\n    http:\n      body: \"X-Scope-OrgID\"\n",
		} {
			Expect(errorsOf(validate.TenantLint(cfg(y), strictFor()))).To(ConsistOf(ContainSubstring("names the X-Scope-OrgID header")), y)
		}
	})

	It("refuses a tcp query_response, config_file, a computed config and one that does not parse", func() {
		qr := "modules:\n  t:\n    prober: tcp\n    tcp:\n      query_response:\n        - send: \"GET / HTTP/1.0\"\n"
		Expect(errorsOf(validate.TenantLint(cfg(qr), strictFor()))).To(ConsistOf(ContainSubstring("query_response")))
		Expect(errorsOf(validate.TenantLint(blackbox("config_file = \"/etc/blackbox.yml\""), strictFor()))).
			To(ConsistOf(ContainSubstring("config_file is read on the collector")))
		Expect(errorsOf(validate.TenantLint(blackbox("config = local.file.c.content"), strictFor()))).
			To(ConsistOf(ContainSubstring("config cannot be verified")))
		Expect(errorsOf(validate.TenantLint(cfg("modules: [unclosed"), strictFor()))).
			To(ConsistOf(ContainSubstring("does not parse as YAML")))
	})
})

// quote is s as an Alloy string literal.
func quote(s string) string {
	b, err := json.Marshal(s)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}
