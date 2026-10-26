package validate_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/validate"
)

// syslogWith is a loki.source.syslog whose relabel_rules is rules, beside a
// loki.relabel "r" (its rule blocks given) and a loki.write to an owned
// tenant.
func syslogWith(rules, relabelRules string) string {
	return "loki.source.syslog \"s\" {\n  listener {\n    address = \"0.0.0.0:1514\"\n  }\n" +
		"  relabel_rules = " + rules + "\n  forward_to    = [loki.write.w.receiver]\n}\n" +
		"loki.relabel \"r\" {\n  forward_to = []\n" + relabelRules + "\n}\n" +
		"loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n}\n"
}

var _ = Describe("strict tenant mode also checks relabel rule attributes", func() {
	DescribeTable("refuses rules written in place of a loki.relabel reference",
		func(rules string) {
			for _, comp := range []string{"loki.source.syslog", "loki.source.journal", "loki.source.kafka"} {
				src := strings.Replace(syslogWith(rules, setOwned), "loki.source.syslog", comp, 1)
				Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(And(
					ContainSubstring(comp+` "s" relabel_rules`),
					ContainSubstring("move the rules into a loki.relabel component"),
				)), comp)
			}
		},
		Entry("a list of labelmap objects", `[{action = "labelmap", regex = "x_(.*)", replacement = "$1"}]`),
		Entry("a list setting the tenant from a label",
			`[{action = "replace", source_labels = ["foo"], target_label = "__tenant_id__"}]`),
		Entry("a single object", `{action = "labelmap", regex = "x_(.*)"}`),
		Entry("a function call", `array.concat(loki.relabel.r.rules, [])`),
		Entry("a concatenation", `loki.relabel.r.rules + []`),
		Entry("a declare argument", `argument.rules.value`),
		Entry("a component's other export", `loki.relabel.r.receiver`),
		Entry("prometheus.relabel's rules (not a loki rules export)", `prometheus.relabel.r.rules`),
		Entry("a module export", `mod.relabel.r.rules`),
	)

	It("accepts a reference to a loki.relabel or discovery.relabel in the same scope, and lints its rules", func() {
		Expect(validate.TenantLint(syslogWith("loki.relabel.r.rules", setOwned), strictFor())).To(BeEmpty())
		disc := strings.Replace(syslogWith("discovery.relabel.r.rules", setOwned), `loki.relabel "r" {`+"\n  forward_to = []",
			`discovery.relabel "r" {`+"\n  targets = []", 1)
		Expect(validate.TenantLint(disc, strictFor())).To(BeEmpty())
		// The referenced component's rules are the ones linted.
		unowned := strings.Replace(setOwned, `"acme"`, `"globex"`, 1)
		Expect(errorsOf(validate.TenantLint(syslogWith("loki.relabel.r.rules", unowned), strictFor()))).
			To(ConsistOf(ContainSubstring(`tenant "globex" is not one this organisation owns`)))
		Expect(errorsOf(validate.TenantLint(syslogWith("loki.relabel.r.rules", labelmapDefault), strictFor()))).
			To(ConsistOf(ContainSubstring("labelmap")))
		// And the served form drops the tenant after its labelmap.
		Expect(validate.TenantLint(insertDrops(syslogWith("loki.relabel.r.rules", labelmapDefault)), strictFor())).To(BeEmpty())
	})

	It("refuses a reference to a component it cannot find in the same scope", func() {
		missing := syslogWith("loki.relabel.other.rules", setOwned)
		Expect(errorsOf(validate.TenantLint(missing, strictFor()))).
			To(ConsistOf(ContainSubstring("not in the same pipeline or declare")))
		// A loki.relabel outside the declare the source is in.
		nested := "declare \"d\" {\n" + strings.Replace(syslogWith("loki.relabel.r.rules", setOwned),
			"loki.relabel \"r\" {\n  forward_to = []\n"+setOwned+"\n}\n", "", 1) + "}\n" +
			"loki.relabel \"r\" {\n  forward_to = []\n" + setOwned + "\n}\n"
		Expect(errorsOf(validate.TenantLint(nested, strictFor()))).
			To(ConsistOf(ContainSubstring("not in the same pipeline or declare")))
	})

	It("checks it inside a foreach template and an import.string module too", func() {
		lit := `[{action = "labelmap", regex = "x_(.*)"}]`
		foreach := "foreach \"f\" {\n  collection = [1]\n  var = \"x\"\n  template {\n" + syslogWith(lit, setOwned) + "  }\n}\n"
		Expect(errorsOf(validate.TenantLint(foreach, strictFor()))).To(ContainElement(ContainSubstring("relabel_rules")))
		mod := "import.string \"m\" {\n  content = " + strconv.Quote(syslogWith(lit, setOwned)) + "\n}\n"
		Expect(errorsOf(validate.TenantLint(mod, strictFor()))).To(ContainElement(ContainSubstring("relabel_rules")))
	})

	It("is not checked outside strict mode", func() {
		Expect(validate.TenantLint(syslogWith(`[{action = "labelmap", regex = "x_(.*)"}]`, setOwned), validate.TenantPolicy{})).
			To(BeEmpty())
	})

	It("refuses it through ValidatePipeline with a served form, too", func() {
		p := strictFor()
		p.Inline = func(_ context.Context, c string) (string, error) { return validate.InsertTenantDrops(c) }
		r := noBinary().ValidatePipeline(context.Background(), "p", syslogWith(`[{action = "labelmap", regex = "x_(.*)"}]`, setOwned), p)
		Expect(r.Valid).To(BeFalse())
		Expect(errorsOf(r.Diagnostics)).To(ContainElement(ContainSubstring("relabel_rules")))
	})
})

// RelabelRuleAttributes is read from the shipped schemas. This classifies
// every capsule-typed attribute and every attribute named like rules in
// every shipped schema, so a new one fails here until someone decides.
var _ = Describe("RelabelRuleAttributes", func() {
	// notRules are capsule attributes that carry no relabel rules.
	notRules := map[string]bool{
		"auth": true, "decision_cache": true, "default_value": true, "extension": true, "storage": true, "value": true,
	}
	// otherRules are attributes named like rules that are not relabel
	// rules: otelcol.processor.span's regexes extracting span attributes.
	otherRules := map[string]bool{"otelcol.processor.span name/to_attributes rules": true}

	type attr struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	type block struct {
		Name       string  `json:"name"`
		Attributes []attr  `json:"attributes"`
		Blocks     []block `json:"blocks"`
	}

	It("covers every relabel rules attribute of every shipped schema, and only those", func() {
		got, err := validate.RelabelRuleAttributes()
		Expect(err).NotTo(HaveOccurred())
		for _, c := range []string{
			"loki.source.api", "loki.source.awsfirehose", "loki.source.azure_event_hubs", "loki.source.docker",
			"loki.source.gcplog", "loki.source.gelf", "loki.source.heroku", "loki.source.journal",
			"loki.source.kafka", "loki.source.syslog",
		} {
			Expect(got[c]).To(HaveKey("relabel_rules"), c)
		}

		files, err := fs.Glob(schema.Embedded, "artifacts/alloy-v*.json")
		Expect(err).NotTo(HaveOccurred())
		Expect(files).NotTo(BeEmpty())
		seen := map[string]bool{}
		for _, f := range files {
			data, err := fs.ReadFile(schema.Embedded, f)
			Expect(err).NotTo(HaveOccurred())
			var art struct {
				Components map[string]struct {
					Attributes []attr  `json:"attributes"`
					Blocks     []block `json:"blocks"`
				} `json:"components"`
			}
			Expect(json.Unmarshal(data, &art)).To(Succeed())
			var walk func(comp, prefix string, as []attr, bs []block)
			walk = func(comp, prefix string, as []attr, bs []block) {
				for _, a := range as {
					key := strings.TrimSpace(comp + " " + prefix + " " + a.Name)
					if prefix == "" {
						key = comp + " " + a.Name
					}
					rulesNamed := strings.HasSuffix(a.Name, "rules")
					switch {
					case a.Type == "capsule" && rulesNamed:
						Expect(prefix).To(BeEmpty(), "%s in %s: a nested relabel rules attribute; the lint checks top-level ones", key, f)
						Expect(got[comp]).To(HaveKey(a.Name), "%s in %s", key, f)
						seen[comp] = true
					case a.Type == "capsule":
						Expect(notRules).To(HaveKey(a.Name), "a new capsule attribute %s in %s: classify it", key, f)
					case rulesNamed:
						Expect(otherRules).To(HaveKey(key), "a new attribute named like rules %s in %s: classify it", key, f)
					}
				}
				for _, b := range bs {
					p := b.Name
					if prefix != "" {
						p = prefix + "/" + b.Name
					}
					walk(comp, p, b.Attributes, b.Blocks)
				}
			}
			for name, c := range art.Components {
				walk(name, "", c.Attributes, c.Blocks)
			}
		}
		Expect(got).To(HaveLen(len(seen)), "RelabelRuleAttributes lists only what the schemas hold")
	})
})

// otlpToLoki is otelcol.receiver.otlp -> otelcol.exporter.loki -> between ->
// loki.write{tenant_id = "acme"}, beside loki.process/loki.relabel steps
// that do and do not clear __tenant_id__.
func otlpToLoki(between string) string {
	return "otelcol.receiver.otlp \"in\" {\n  http { }\n  output {\n    logs = [otelcol.exporter.loki.x.input]\n  }\n}\n" +
		"otelcol.exporter.loki \"x\" {\n  forward_to = [" + between + "]\n}\n" +
		"loki.process \"set\" {\n  forward_to = [loki.write.w.receiver]\n  stage.static_labels {\n    values = { \"__tenant_id__\" = \"acme\" }\n  }\n}\n" +
		"loki.process \"drop\" {\n  forward_to = [loki.write.w.receiver]\n  stage.label_drop {\n    values = [\"__tenant_id__\"]\n  }\n}\n" +
		"loki.relabel \"drop\" {\n  forward_to = [loki.write.w.receiver]\n" + tenantDrop + "\n}\n" +
		"loki.process \"noop\" {\n  forward_to = [loki.write.w.receiver]\n  stage.static_labels {\n    values = { team = \"a\" }\n  }\n}\n" +
		"loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n}\n"
}

var _ = Describe("strict tenant mode also checks the labels otelcol.exporter.loki makes", func() {
	DescribeTable("refuses a path to the writer that keeps a __tenant_id__ made from OTLP attributes",
		func(between string) {
			Expect(errorsOf(validate.TenantLint(otlpToLoki(between), strictFor()))).To(ConsistOf(And(
				ContainSubstring(`otelcol.exporter.loki "x"`),
				ContainSubstring("loki.resource.labels"),
				ContainSubstring("on every path to the writer"),
			)))
		},
		Entry("straight to the writer", "loki.write.w.receiver"),
		Entry("through a step that keeps it", "loki.process.noop.receiver"),
		Entry("one safe path and one unsafe", "loki.process.drop.receiver, loki.write.w.receiver"),
	)
	DescribeTable("accepts every path setting it to an owned tenant or dropping it first",
		func(between string) {
			Expect(validate.TenantLint(otlpToLoki(between), strictFor())).To(BeEmpty())
		},
		Entry("stage.static_labels to an owned tenant", "loki.process.set.receiver"),
		Entry("stage.label_drop", "loki.process.drop.receiver"),
		Entry("a relabel labeldrop", "loki.relabel.drop.receiver"),
	)
	It("refuses setting it to a tenant the org does not own", func() {
		other := otlpToLoki("loki.process.setother.receiver") + "loki.process \"setother\" {\n  forward_to = [loki.write.w.receiver]\n" +
			"  stage.static_labels {\n    values = { \"__tenant_id__\" = \"globex\" }\n  }\n}\n"
		Expect(errorsOf(validate.TenantLint(other, strictFor()))).
			To(ContainElement(ContainSubstring(`tenant "globex" is not one this organisation owns`)))
	})
})

// exporterDrop is what the strict served form puts after an
// otelcol.exporter.loki "x" whose forward_to was fwd, at the top level.
func exporterDrop(fwd string) string {
	return "\n\nloki.process \"shepherd_tenant_drop_x\" {\n  forward_to = " + fwd + "\n\n  stage.label_drop {\n" +
		"    values = [\"__tenant_id__\"]\n  }\n}"
}

const (
	exporterDirect  = "otelcol.exporter.loki \"x\" {\n  forward_to = [loki.write.w.receiver]\n}"
	exporterViaDrop = "otelcol.exporter.loki \"x\" {\n  forward_to = [loki.process.shepherd_tenant_drop_x.receiver]\n}"
)

var _ = Describe("the strict served form routes otelcol.exporter.loki through a __tenant_id__ drop", func() {
	v := noBinary()
	strictWith := func(inline func(context.Context, string) (string, error)) validate.TenantPolicy {
		p := strictFor()
		p.Inline = inline
		return p
	}
	drops := func(_ context.Context, c string) (string, error) { return validate.InsertTenantDrops(c) }
	identity := func(_ context.Context, c string) (string, error) { return c, nil }

	It("forwards the exporter to an inserted loki.process that drops __tenant_id__ and forwards as before", func() {
		src := otlpToLoki("loki.write.w.receiver")
		out := insertDrops(src)
		Expect(out).To(Equal(strings.Replace(src, exporterDirect, exporterViaDrop+exporterDrop("[loki.write.w.receiver]"), 1)))
		Expect(validate.TenantLint(out, strictFor())).To(BeEmpty())

		// Several targets and a multi-line list are kept verbatim.
		multi := strings.Replace(src, "[loki.write.w.receiver]\n}\nloki.process \"set\"",
			"[\n    loki.write.w.receiver,\n    loki.process.noop.receiver,\n  ]\n}\nloki.process \"set\"", 1)
		Expect(insertDrops(multi)).To(ContainSubstring(exporterViaDrop +
			exporterDrop("[\n    loki.write.w.receiver,\n    loki.process.noop.receiver,\n  ]")))
	})

	It("is accepted when saved, and refused when the served form lacks the drop (rewrite stubbed out)", func() {
		src := otlpToLoki("loki.write.w.receiver")
		Expect(v.ValidatePipeline(context.Background(), "p", src, strictWith(drops)).Valid).To(BeTrue())
		r := v.ValidatePipeline(context.Background(), "p", src, strictWith(identity))
		Expect(r.Valid).To(BeFalse())
		Expect(errorsOf(r.Diagnostics)).To(ConsistOf(And(ContainSubstring("as served"), ContainSubstring(`otelcol.exporter.loki "x"`))))
		// With no served form at all the stored text is refused as before.
		Expect(v.ValidatePipeline(context.Background(), "p", src, strictFor()).Valid).To(BeFalse())
	})

	It("applies in a declare body and in an import.string module", func() {
		decl := "declare \"d\" {\n" + indent(otlpToLoki("loki.write.w.receiver")) + "}\n"
		out := insertDrops(decl)
		Expect(out).To(ContainSubstring("  " + strings.ReplaceAll(exporterViaDrop, "\n", "\n  ")))
		Expect(out).To(ContainSubstring("\n  loki.process \"shepherd_tenant_drop_x\" {\n    forward_to = [loki.write.w.receiver]\n"))
		Expect(validate.TenantLint(out, strictFor())).To(BeEmpty())
		Expect(v.ValidatePipeline(context.Background(), "p", decl, strictWith(drops)).Valid).To(BeTrue())

		mod := "import.string \"m\" {\n  content = " + strconv.Quote(decl) + "\n}\n"
		out = insertDrops(mod)
		Expect(out).To(ContainSubstring("shepherd_tenant_drop_x"))
		Expect(validate.TenantLint(out, strictFor())).To(BeEmpty())
		Expect(v.ValidatePipeline(context.Background(), "p", mod, strictWith(drops)).Valid).To(BeTrue())
	})

	DescribeTable("refuses a forward_to that is not a plain list of receivers, and leaves it unrewritten",
		func(fwd string) {
			src := "declare \"d\" {\n  argument \"out\" { }\n  otelcol.exporter.loki \"x\" {\n    forward_to = " + fwd + "\n  }\n}\n"
			Expect(insertDrops(src)).To(Equal(src))
			r := v.ValidatePipeline(context.Background(), "p", src, strictWith(drops))
			Expect(r.Valid).To(BeFalse())
			Expect(errorsOf(r.Diagnostics)).To(ConsistOf(ContainSubstring("forward_to is not a list of component receivers")))
		},
		Entry("a declare argument", "argument.out.value"),
		Entry("a function call", "array.concat([], argument.out.value)"),
		Entry("a list holding an argument", "[argument.out.value]"),
	)

	It("refuses the save when the inserted component's name is already taken in that scope", func() {
		src := otlpToLoki("loki.write.w.receiver") + "loki.process \"shepherd_tenant_drop_x\" {\n  forward_to = []\n}\n"
		_, err := validate.InsertTenantDrops(src)
		Expect(err).To(MatchError(ContainSubstring(`loki.process "shepherd_tenant_drop_x" already exists`)))
		r := v.ValidatePipeline(context.Background(), "p", src, strictWith(drops))
		Expect(r.Valid).To(BeFalse())
		Expect(errorsOf(r.Diagnostics)).To(ContainElement(ContainSubstring("already exists")))
	})

	It("leaves a pipeline without otelcol.exporter.loki byte-identical", func() {
		for _, src := range []string{
			syslogWith("loki.relabel.r.rules", setOwned),
			"otelcol.exporter.prometheus \"p\" {\n  forward_to = []\n}\n",
			"otelcol.exporter.loki \"x\" { }\n",
			"otelcol.exporter.loki \"x\" {\n  forward_to = []\n}\n",
		} {
			Expect(insertDrops(src)).To(Equal(src), src)
		}
	})
})
