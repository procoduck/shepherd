package validate_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
	"shepherd/internal/wizard/wizardtest"
)

// tenantDrop is the rule the served form inserts after a labelmap, at two
// spaces of indentation (#410).
const tenantDrop = "  rule {\n    action = \"labeldrop\"\n    regex  = \"__tenant_id__\"\n  }"

func ownsAll() validate.TenantPolicy {
	return validate.TenantPolicy{Strict: true, Owns: func(string) bool { return true }}
}

func relabel(rules ...string) string {
	return "loki.relabel \"r\" {\n  forward_to = []\n" + strings.Join(rules, "\n") + "\n}\n"
}

const (
	labelmapDefault = "  rule {\n    action = \"labelmap\"\n    regex  = \"x_(.+)\"\n  }"
	labelmapRef     = "  rule {\n    action      = \"labelmap\"\n    regex       = \"x_(.+)\"\n    replacement = \"y_$1\"\n  }"
	labelmapLiteral = "  rule {\n    action      = \"labelmap\"\n    regex       = \"x_team\"\n    replacement = \"team\"\n  }"
	setOwned        = "  rule {\n    target_label = \"__tenant_id__\"\n    replacement  = \"acme\"\n  }"
)

func k8smon(name string) string {
	b, err := os.ReadFile("../signals/testdata/k8smon-" + name + ".alloy")
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

// k8smonStrict is the k8s-monitoring fixture name, its logs fixture with a
// loki.process dropping __tenant_id__ between its otelcol.exporter.loki and
// its loki.write. The chart's output sends the exporter straight to the
// writer, which strict tenant mode refuses: the exporter may turn OTLP
// attributes into that label.
func k8smonStrict(name string) string {
	src := k8smon(name)
	if name != "logs" {
		return src
	}
	const exporter = "otelcol.exporter.loki \"loki\" {\n  forward_to = [loki.write.loki.receiver]\n}"
	Expect(src).To(ContainSubstring(exporter))
	return strings.Replace(src, exporter, "otelcol.exporter.loki \"loki\" {\n  forward_to = [loki.process.tenant_drop.receiver]\n}", 1) +
		"\nloki.process \"tenant_drop\" {\n  forward_to = [loki.write.loki.receiver]\n  stage.label_drop {\n    values = [\"__tenant_id__\"]\n  }\n}\n"
}

func insertDrops(text string) string {
	out, err := validate.InsertTenantDrops(text)
	Expect(err).NotTo(HaveOccurred())
	return out
}

var _ = Describe("InsertTenantDrops (strict tenant mode's labelmap drop, #410)", func() {
	It("leaves text without a labelmap that needs one byte-identical", func() {
		for _, src := range []string{
			"",
			writerModule("acme"),
			relabel(setOwned),
			relabel(labelmapLiteral),
			relabel(labelmapDefault, tenantDrop), // already dropped: no second one
			"loki.process \"p\" {\n  forward_to = []\n  stage.truncate {\n    rule {\n      limit = 10\n    }\n  }\n}\n",
		} {
			Expect(insertDrops(src)).To(Equal(src), src)
		}
	})

	It("inserts the drop directly after the labelmap, at its indentation", func() {
		Expect(insertDrops(relabel(labelmapDefault, setOwned))).To(Equal(relabel(labelmapDefault, tenantDrop, setOwned)))
	})

	It("handles a labelmap as the last rule, several in a row, and a reference replacement", func() {
		Expect(insertDrops(relabel(setOwned, labelmapDefault))).To(Equal(relabel(setOwned, labelmapDefault, tenantDrop)))
		Expect(insertDrops(relabel(labelmapDefault, labelmapRef, labelmapDefault))).
			To(Equal(relabel(labelmapDefault, tenantDrop, labelmapRef, tenantDrop, labelmapDefault, tenantDrop)))
	})

	It("does not drop after a labelmap with a literal replacement: it writes that one label", func() {
		Expect(insertDrops(relabel(labelmapLiteral, setOwned))).To(Equal(relabel(labelmapLiteral, setOwned)))
	})

	It("covers every relabel component, a declare body and a foreach template", func() {
		for _, comp := range []string{"discovery.relabel", "prometheus.relabel", "pyroscope.relabel", "prometheus.operator.podmonitors"} {
			src := comp + " \"r\" {\n" + labelmapDefault + "\n}\n"
			Expect(insertDrops(src)).To(Equal(comp+" \"r\" {\n"+labelmapDefault+"\n"+tenantDrop+"\n}\n"), comp)
		}
		db := "database_observability.mysql \"d\" {\n  database_instance {\n    rule {\n      action = \"labelmap\"\n      regex = \"a(.*)\"\n    }\n  }\n}\n"
		Expect(insertDrops(db)).To(ContainSubstring("    }\n    rule {\n      action = \"labeldrop\"\n      regex  = \"__tenant_id__\"\n    }\n  }\n"))

		decl := "declare \"d\" {\n" + indent(relabel(labelmapDefault)) + "}\n"
		Expect(insertDrops(decl)).To(Equal("declare \"d\" {\n" + indent(relabel(labelmapDefault, tenantDrop)) + "}\n"))

		fe := "foreach \"f\" {\n  collection = [1]\n  var = \"x\"\n  template {\n" + indent(indent(relabel(labelmapDefault))) + "  }\n}\n"
		Expect(insertDrops(fe)).To(Equal("foreach \"f\" {\n  collection = [1]\n  var = \"x\"\n  template {\n" +
			indent(indent(relabel(labelmapDefault, tenantDrop))) + "  }\n}\n"))
	})

	It("handles unusual formatting, comments and CRLF line endings", func() {
		oneLine := "loki.relabel \"r\" { forward_to = []\n  rule { action = \"labelmap\" } // keep\n}\n"
		out := insertDrops(oneLine)
		Expect(out).To(Equal("loki.relabel \"r\" { forward_to = []\n  rule { action = \"labelmap\" }\n" + tenantDrop + " // keep\n}\n"))
		Expect(validate.TenantLint(out, ownsAll())).To(BeEmpty())

		commented := "loki.relabel \"r\" {\n  forward_to = []\n  /* a */ rule /* b */ {\n    // c\n    action = /* d */ \"labelmap\"\n  }\n}\n"
		out = insertDrops(commented)
		Expect(out).To(ContainSubstring("  }\n  rule {\n"), "indented as the rule's line is")
		Expect(validate.TenantLint(out, ownsAll())).To(BeEmpty())

		// The indent is the line's leading whitespace, byte for byte: tabs
		// stay tabs, and a multi-byte character before the rule does not
		// shift it.
		tabs := "loki.relabel \"r\" {\n\tforward_to = []\n\trule {\n\t\taction = \"labelmap\"\n\t}\n}\n"
		Expect(insertDrops(tabs)).To(Equal("loki.relabel \"r\" {\n\tforward_to = []\n\trule {\n\t\taction = \"labelmap\"\n\t}\n" +
			"\trule {\n\t  action = \"labeldrop\"\n\t  regex  = \"__tenant_id__\"\n\t}\n}\n"))
		multi := "loki.relabel \"r\" {\n  forward_to = []\n \t/* é ü */ rule { action = \"labelmap\" }\n}\n"
		out = insertDrops(multi)
		Expect(out).To(Equal("loki.relabel \"r\" {\n  forward_to = []\n \t/* é ü */ rule { action = \"labelmap\" }\n" +
			" \trule {\n \t  action = \"labeldrop\"\n \t  regex  = \"__tenant_id__\"\n \t}\n}\n"))
		Expect(validate.TenantLint(out, ownsAll())).To(BeEmpty())

		crlf := strings.ReplaceAll(relabel(labelmapDefault, setOwned), "\n", "\r\n")
		Expect(insertDrops(crlf)).To(Equal(strings.ReplaceAll(relabel(labelmapDefault, tenantDrop, setOwned), "\n", "\r\n")))
	})

	It("inserts inside an import.string module's content, re-quoted, at any depth", func() {
		inner := relabel(labelmapDefault)
		mod := "declare \"m\" {\n" + indent(inner) + "}\n"
		src := "import.string \"m\" {\n  content = " + strconv.Quote(mod) + "\n}\n"
		out := insertDrops(src)
		Expect(out).To(Equal("import.string \"m\" {\n  content = " +
			strconv.Quote("declare \"m\" {\n"+indent(relabel(labelmapDefault, tenantDrop))+"}\n") + "\n}\n"))
		Expect(validate.ServedFormProblem(out)).To(Succeed())

		nested := "import.string \"o\" {\n  content = " + strconv.Quote(src) + "\n}\n"
		Expect(insertDrops(nested)).To(ContainSubstring("labeldrop"))
	})

	It("takes only a block named rule as the next rule", func() {
		// A block with the drop's body but another name is not the drop:
		// the labelmap still gets one, and without it the lint refuses it.
		for _, name := range []string{"stage.rule", "rules", "rule.x"} {
			lookalike := "  " + name + " {\n    action = \"labeldrop\"\n    regex  = \"__tenant_id__\"\n  }"
			src := relabel(labelmapDefault, lookalike)
			Expect(errorsOf(validate.TenantLint(src, ownsAll()))).To(ContainElement(ContainSubstring("labelmap")), name)
			Expect(insertDrops(src)).To(Equal(relabel(labelmapDefault, tenantDrop, lookalike)), name)
		}
		// A block of another name between the labelmap and the drop is
		// skipped over: the drop is still the next rule.
		between := "  other {\n    a = 1\n  }"
		Expect(validate.TenantLint(relabel(labelmapDefault, between, tenantDrop), ownsAll())).To(BeEmpty())
	})

	It("leaves a computed labelmap action alone, so the lint refuses it", func() {
		src := "declare \"d\" {\n  argument \"a\" { }\n  loki.relabel \"r\" {\n    forward_to = []\n    rule {\n      action = argument.a.value\n    }\n  }\n}\n" +
			"d \"x\" {\n  a = \"labelmap\"\n}\n"
		Expect(insertDrops(src)).To(Equal(src))
		Expect(errorsOf(validate.TenantLint(src, ownsAll()))).To(HaveLen(1))
	})
})

func indent(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

var _ = Describe("the tenant lint on a labelmap (#410)", func() {
	It("allows a labelmap that may write __tenant_id__ only when the very next rule drops it", func() {
		for _, lm := range []string{labelmapDefault, labelmapRef} {
			Expect(errorsOf(validate.TenantLint(relabel(lm), ownsAll()))).To(HaveLen(1), lm)
			Expect(validate.TenantLint(relabel(lm, tenantDrop), ownsAll())).To(BeEmpty(), lm)
			Expect(errorsOf(validate.TenantLint(relabel(lm, setOwned, tenantDrop), ownsAll()))).To(HaveLen(1),
				"the drop must be the very next rule")
		}
	})

	It("accepts only the exact drop", func() {
		for _, drop := range []string{
			"  rule {\n    action = \"labeldrop\"\n    regex  = \"__tenant_id__|x\"\n  }",
			"  rule {\n    action = \"labeldrop\"\n    regex  = \"__tenant_.*\"\n  }",
			"  rule {\n    action = \"labelkeep\"\n    regex  = \"__tenant_id__\"\n  }",
			"  rule {\n    action = \"labeldrop\"\n    regex  = \"__tenant_id__\"\n    replacement = \"x\"\n  }",
			"  rule {\n    action = \"labeldrop\"\n    regex  = sys.env(\"R\")\n  }",
		} {
			Expect(errorsOf(validate.TenantLint(relabel(labelmapDefault, drop), ownsAll()))).NotTo(BeEmpty(), drop)
		}
	})

	It("keeps refusing the other rules that write __tenant_id__, drop or not", func() {
		literalTenant := "  rule {\n    action      = \"labelmap\"\n    regex       = \"x\"\n    replacement = \"__tenant_id__\"\n  }"
		replaceFromData := "  rule {\n    source_labels = [\"t\"]\n    target_label  = \"__tenant_id__\"\n  }"
		for _, src := range []string{
			relabel(literalTenant, tenantDrop, setOwned),
			relabel(literalTenant, setOwned),
			relabel(labelmapDefault, tenantDrop, replaceFromData),
		} {
			Expect(errorsOf(validate.TenantLint(insertDrops(src), ownsAll()))).NotTo(BeEmpty(), src)
		}
	})

	It("keeps a later rule that sets an owned tenant working, and refuses an unowned one", func() {
		served := insertDrops(relabel(labelmapDefault, setOwned))
		Expect(validate.TenantLint(served, strictFor())).To(BeEmpty())
		victim := strings.Replace(setOwned, "acme", "victim", 1)
		Expect(errorsOf(validate.TenantLint(insertDrops(relabel(labelmapDefault, victim)), strictFor()))).
			To(ConsistOf(ContainSubstring(`"victim"`)))
	})

	It("refuses the chart's logs fixture verbatim for one finding only: its otelcol.exporter.loki path", func() {
		Expect(errorsOf(validate.TenantLint(insertDrops(k8smon("logs")), ownsAll()))).To(ConsistOf(And(
			ContainSubstring(`otelcol.exporter.loki "loki"`),
			ContainSubstring(`to loki.write "loki" without overwriting or dropping it`),
		)))
		Expect(validate.TenantLint(insertDrops(k8smonStrict("logs")), ownsAll())).To(BeEmpty())
	})

	It("finds the chart's labelmaps in both k8s-monitoring fixtures, and none once the drops are in", func() {
		for name, n := range map[string]int{"logs": 1, "metrics": 2} {
			src := k8smonStrict(name)
			Expect(errorsOf(validate.TenantLint(src, ownsAll()))).To(HaveLen(n), name)
			served := insertDrops(src)
			Expect(strings.Count(served, "action = \"labeldrop\"\n")).To(Equal(n+strings.Count(src, "action = \"labeldrop\"\n")), name)
			Expect(validate.TenantLint(served, ownsAll())).To(BeEmpty(), name)
		}
	})
})

var _ = Describe("ValidatePipeline in a strict org with a labelmap (#410)", func() {
	v := validate.New(&config.ValidateConfig{StabilityLevel: "experimental", Timeout: 10 * time.Second})
	withInline := func(inline func(context.Context, string) (string, error)) validate.TenantPolicy {
		p := ownsAll()
		p.Inline = inline
		return p
	}
	drops := func(_ context.Context, c string) (string, error) { return validate.InsertTenantDrops(c) }

	It("saves the k8s-monitoring fixtures verbatim: the served form carries the drops", func() {
		for _, name := range []string{"logs", "metrics"} {
			r := v.ValidatePipeline(context.Background(), name, k8smonStrict(name), withInline(drops))
			Expect(r.Valid).To(BeTrue(), "%s: %v", name, r.Diagnostics)
		}
	})

	It("fails closed when the served form lacks the drop (insertion stubbed out)", func() {
		identity := func(_ context.Context, c string) (string, error) { return c, nil }
		for _, src := range []string{k8smonStrict("logs"), relabel(labelmapDefault)} {
			r := v.ValidatePipeline(context.Background(), "p", src, withInline(identity))
			Expect(r.Valid).To(BeFalse())
			Expect(errorsOf(r.Diagnostics)).To(ContainElement(And(ContainSubstring("as served"), ContainSubstring("labelmap"))))
			Expect(validate.WithTenantLint(context.Background(), validate.Result{Valid: true}, src, withInline(identity)).Valid).To(BeFalse())
		}
		// And with no served form at all, the stored text is refused as before.
		Expect(v.ValidatePipeline(context.Background(), "p", relabel(labelmapDefault), ownsAll()).Valid).To(BeFalse())
	})

	It("is accepted by the real alloy validate (v1.21.2) for both fixtures", func() {
		bin := wizardtest.AlloyBinary()
		if bin == "" {
			Skip("no alloy binary and no docker")
		}
		real := validate.New(&config.ValidateConfig{AlloyBinary: bin, StabilityLevel: "experimental", Timeout: 2 * time.Minute})
		for _, name := range []string{"logs", "metrics"} {
			served := insertDrops(k8smonStrict(name))
			r := real.Stages12(context.Background(), validate.WrapForValidation(name, served))
			Expect(r.Valid).To(BeTrue(), "%s: %v", name, r.Diagnostics)
			Expect(r.Skipped).To(BeEmpty(), name)
			r = real.ValidatePipeline(context.Background(), name, k8smonStrict(name), withInline(drops))
			Expect(r.Valid).To(BeTrue(), "%s: %v", name, r.Diagnostics)
		}
	})
})

// The relabel rule blocks InsertTenantDrops covers come from the shipped
// schemas (RelabelRuleBlocks). This classifies every schema block that
// looks like a rule, so a new one fails here until someone decides.
var _ = Describe("RelabelRuleBlocks", func() {
	type block struct {
		Name       string `json:"name"`
		Attributes []struct {
			Name string `json:"name"`
		} `json:"attributes"`
		Blocks []block `json:"blocks"`
	}
	// notRelabel are `rule` blocks with an action that are not relabel
	// rules: beyla's payload enrichment rules (include/exclude).
	notRelabel := map[string]bool{"beyla.ebpf ebpf/payload_extraction/http/enrichment/rule": true}
	// otherRelabel are relabel-shaped blocks not named `rule`:
	// prometheus.remote_write's write_relabel_config. Mimir reads the
	// tenant only from the X-Scope-OrgID header, never a label, so a
	// labelmap there cannot pick a tenant; the lint does not check it.
	otherRelabel := map[string]bool{"prometheus.remote_write endpoint/write_relabel_config": true}

	It("covers every relabel rule block of every shipped schema, and only those", func() {
		got, err := validate.RelabelRuleBlocks()
		Expect(err).NotTo(HaveOccurred())
		for _, c := range []string{
			"discovery.relabel", "prometheus.relabel", "loki.relabel", "pyroscope.relabel",
			"prometheus.operator.podmonitors", "prometheus.operator.probes", "prometheus.operator.scrapeconfigs",
			"prometheus.operator.servicemonitors",
		} {
			Expect(got[c]).To(HaveKey("rule"), c)
		}
		Expect(got["database_observability.mysql"]).To(HaveKey("database_instance/rule"))

		files, err := fs.Glob(schema.Embedded, "artifacts/alloy-v*.json")
		Expect(err).NotTo(HaveOccurred())
		Expect(files).NotTo(BeEmpty())
		for _, f := range files {
			data, err := fs.ReadFile(schema.Embedded, f)
			Expect(err).NotTo(HaveOccurred())
			var art struct {
				Components map[string]struct {
					Blocks []block `json:"blocks"`
				} `json:"components"`
			}
			Expect(json.Unmarshal(data, &art)).To(Succeed())
			var walk func(comp, prefix string, bs []block)
			walk = func(comp, prefix string, bs []block) {
				for _, b := range bs {
					p := b.Name
					if prefix != "" {
						p = prefix + "/" + b.Name
					}
					attrs := map[string]bool{}
					for _, a := range b.Attributes {
						attrs[a.Name] = true
					}
					relabelShaped := attrs["action"] && attrs["regex"] && attrs["replacement"] && attrs["source_labels"] && attrs["target_label"]
					key := comp + " " + p
					switch {
					case b.Name == "rule" && relabelShaped:
						Expect(got[comp]).To(HaveKey(p), "%s in %s", key, f)
					case b.Name == "rule" && attrs["action"]:
						Expect(notRelabel).To(HaveKey(key), "a new `rule` block with an action in %s: classify it", f)
					case relabelShaped:
						Expect(otherRelabel).To(HaveKey(key), "a new relabel block not named rule in %s: classify it", f)
					}
					walk(comp, p, b.Blocks)
				}
			}
			for name, c := range art.Components {
				walk(name, "", c.Blocks)
			}
		}
	})
})
