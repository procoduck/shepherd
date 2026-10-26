package validate_test

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/validate"
)

// lokiWrite is a loki.write to the owned tenant "acme" with external_labels
// set to labels.
func lokiWrite(labels string) string {
	return "loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n" +
		"  external_labels = " + labels + "\n}\n"
}

// staticLabels is a loki.process whose stage.static_labels values are values.
func staticLabels(values string) string {
	return "loki.process \"p\" {\n  forward_to = []\n  stage.static_labels {\n    values = " + values + "\n  }\n}\n"
}

var _ = Describe("strict tenant mode checks label maps computed at runtime", func() {
	It("classifies every map-typed and targets attribute of every shipped schema", func() {
		attrs, err := validate.SchemaMapAttributes(schema.Embedded)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).NotTo(BeEmpty())
		var unclassified []string
		kinds := map[validate.MapAttrKind]int{}
		for _, a := range attrs {
			kind, reason := validate.ClassifyMapAttribute(a.Component, a.Block(), a.Attr, a.Type)
			kinds[kind]++
			switch kind {
			case validate.MapAttrUnclassified:
				unclassified = append(unclassified, fmt.Sprintf("%s %s %s (%s)", a.Component, a.Path, a.Attr, a.Type))
			case validate.MapAttrExcluded, validate.MapAttrTargets:
				Expect(reason).NotTo(BeEmpty(), "%s %s %s needs a documented reason", a.Component, a.Path, a.Attr)
			case validate.MapAttrLabels, validate.MapAttrHeaders:
			}
		}
		Expect(unclassified).To(BeEmpty(), "a schema brings map attributes strict tenant mode does not classify: "+
			"add each to the label maps, the exclusions (with a reason) or the target consumers in labelmaps.go")
		Expect(kinds[validate.MapAttrLabels]).To(BeNumerically(">", 10))
		Expect(kinds[validate.MapAttrHeaders]).To(BeNumerically(">", 10))
	})

	DescribeTable("classifies the label maps a tenant can be set through",
		func(component, block, attr string, want validate.MapAttrKind) {
			kind, _ := validate.ClassifyMapAttribute(component, block, attr, "map")
			Expect(kind).To(Equal(want))
		},
		Entry(nil, "loki.write", "", "external_labels", validate.MapAttrLabels),
		Entry(nil, "loki.process", "stage.static_labels", "values", validate.MapAttrLabels),
		Entry(nil, "loki.process", "stage.labels", "values", validate.MapAttrLabels),
		Entry(nil, "loki.source.api", "", "labels", validate.MapAttrLabels),
		Entry(nil, "loki.source.syslog", "listener", "labels", validate.MapAttrLabels),
		Entry(nil, "faro.receiver", "", "extra_log_labels", validate.MapAttrLabels),
		Entry(nil, "prometheus.remote_write", "", "external_labels", validate.MapAttrLabels),
		Entry(nil, "pyroscope.write", "", "external_labels", validate.MapAttrLabels),
		Entry(nil, "loki.write", "endpoint", "headers", validate.MapAttrHeaders),
		Entry(nil, "otelcol.exporter.otlphttp", "client", "headers", validate.MapAttrHeaders),
	)

	DescribeTable("refuses a label map it cannot read",
		func(src, where string) {
			Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(And(
				ContainSubstring(where), ContainSubstring("not a literal map"))))
		},
		Entry("external_labels from a file", "local.file \"ext\" {\n  filename = \"/etc/ext.json\"\n}\n"+
			lokiWrite("encoding.from_json(local.file.ext.content)"), "loki.write external_labels"),
		Entry("external_labels from a JSON literal", lokiWrite(`encoding.from_json("{\"__tenant_id__\":\"x\"}")`),
			"loki.write external_labels"),
		Entry("stage.static_labels from JSON", staticLabels(`encoding.from_json("{\"__tenant_id__\":\"x\"}")`),
			"loki.process stage.static_labels values"),
		Entry("a source's labels from an export", "loki.source.api \"a\" {\n  http {\n    listen_port = 3100\n  }\n"+
			"  labels     = remote.http.x.content\n  forward_to = []\n}\n", "loki.source.api labels"),
		Entry("a listener's labels from a call", "loki.source.syslog \"s\" {\n  listener {\n    address = \":1514\"\n"+
			"    labels  = encoding.from_yaml(sys.env(\"L\"))\n  }\n  forward_to = []\n}\n", "loki.source.syslog listener labels"),
		Entry("faro's extra_log_labels from a call", "faro.receiver \"f\" {\n  extra_log_labels = encoding.from_json(\"{}\")\n"+
			"  output {\n  }\n}\n", "faro.receiver extra_log_labels"),
		Entry("prometheus external_labels from a call", "prometheus.remote_write \"m\" {\n  endpoint {\n"+
			"    url = \"https://m.example/push\"\n    headers = { \"X-Scope-OrgID\" = \"acme\" }\n  }\n"+
			"  external_labels = encoding.from_json(\"{}\")\n}\n", "prometheus.remote_write external_labels"),
		Entry("a declare argument given a call", "declare \"d\" {\n  argument \"l\" { }\n"+
			indent(lokiWrite("argument.l.value"))+"}\nd \"x\" {\n  l = encoding.from_json(\"{}\")\n}\n",
			"loki.write external_labels"),
		Entry("a declare argument with no value", "declare \"d\" {\n  argument \"l\" { optional = true }\n"+
			indent(lokiWrite("argument.l.value"))+"}\n", "loki.write external_labels"),
	)

	It("refuses headers it cannot read, a declare argument given a call included", func() {
		src := "declare \"d\" {\n  argument \"h\" { }\n  prometheus.remote_write \"m\" {\n    endpoint {\n" +
			"      url = \"https://m.example/push\"\n      headers = argument.h.value\n    }\n  }\n}\n" +
			"d \"x\" {\n  h = encoding.from_json(\"{}\")\n}\n"
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring("headers set from an expression")))
	})

	It("accepts literal label maps, and computed values under other keys", func() {
		for _, src := range []string{
			lokiWrite(`{ cluster = "prod", "__tenant_id__" = "acme" }`),
			lokiWrite(`{ cluster = sys.env("CLUSTER") }`),
			staticLabels(`{ "__tenant_id__" = "acme-prod" }`),
			lokiWrite(`({})`),
		} {
			Expect(validate.TenantLint(src, strictFor())).To(BeEmpty(), src)
		}
	})

	It("folds a declare argument to the literal maps it is given, and checks their tenants", func() {
		decl := func(given string) string {
			return "declare \"d\" {\n  argument \"l\" {\n    default = { team = \"a\" }\n  }\n" +
				indent(lokiWrite("argument.l.value")) + "}\nd \"x\" {\n  l = " + given + "\n}\n"
		}
		Expect(validate.TenantLint(decl(`{ "__tenant_id__" = "acme" }`), strictFor())).To(BeEmpty())
		Expect(errorsOf(validate.TenantLint(decl(`{ "__tenant_id__" = "globex" }`), strictFor()))).
			To(ConsistOf(ContainSubstring(`tenant "globex" is not one this organisation owns`)))
		// Passed down two declare levels.
		nested := "declare \"outer\" {\n  argument \"l\" { }\n" + indent(decl(`argument.l.value`)) +
			"}\nouter \"o\" {\n  l = { \"__tenant_id__\" = \"globex\" }\n}\n"
		Expect(errorsOf(validate.TenantLint(nested, strictFor()))).
			To(ContainElement(ContainSubstring(`tenant "globex" is not one this organisation owns`)))
	})

	It("refuses __tenant_id__ in stage.labels whether written in place or given through an argument", func() {
		inPlace := "loki.process \"p\" {\n  forward_to = []\n  stage.labels {\n    values = { \"__tenant_id__\" = \"acme\" }\n  }\n}\n"
		Expect(errorsOf(validate.TenantLint(inPlace, strictFor()))).To(ConsistOf(ContainSubstring("from the log data")))
		viaArg := "declare \"d\" {\n  argument \"v\" { }\n  loki.process \"p\" {\n    forward_to = []\n    stage.labels {\n" +
			"      values = argument.v.value\n    }\n  }\n}\nd \"x\" {\n  v = { \"__tenant_id__\" = \"acme\" }\n}\n"
		Expect(errorsOf(validate.TenantLint(viaArg, strictFor()))).To(ConsistOf(ContainSubstring("from the log data")))
	})

	It("folds a headers argument and checks X-Scope-OrgID in what it is given", func() {
		src := func(tenant string) string {
			return "declare \"d\" {\n  argument \"h\" { }\n  prometheus.remote_write \"m\" {\n    endpoint {\n" +
				"      url = \"https://m.example/push\"\n      headers = argument.h.value\n    }\n  }\n}\n" +
				"d \"x\" {\n  h = { \"X-Scope-OrgID\" = \"" + tenant + "\" }\n}\n"
		}
		Expect(validate.TenantLint(src("acme"), strictFor())).To(BeEmpty())
		Expect(errorsOf(validate.TenantLint(src("globex"), strictFor()))).
			To(ConsistOf(ContainSubstring(`tenant "globex" is not one this organisation owns`)))
	})

	It("takes discovery targets into the Loki sources that drop reserved labels, and metrics and profiles", func() {
		for _, src := range []string{
			"loki.source.file \"f\" {\n  targets    = local.file_match.x.targets\n  forward_to = []\n}\n",
			"loki.source.docker \"d\" {\n  host       = \"unix:///var/run/docker.sock\"\n  targets    = discovery.docker.x.targets\n  forward_to = []\n}\n",
			"loki.source.kubernetes \"k\" {\n  targets    = discovery.kubernetes.pods.targets\n  forward_to = []\n}\n",
			"prometheus.scrape \"s\" {\n  targets    = array.concat(discovery.kubernetes.pods.targets, [])\n  forward_to = []\n}\n",
			"pyroscope.scrape \"s\" {\n  targets    = discovery.kubernetes.pods.targets\n  forward_to = []\n}\n",
		} {
			Expect(validate.TenantLint(src, strictFor())).To(BeEmpty(), src)
		}
		// A literal target still has its tenant label checked.
		lit := "loki.source.file \"f\" {\n  targets    = [{ \"__path__\" = \"/x\", \"__tenant_id__\" = \"globex\" }]\n  forward_to = []\n}\n"
		Expect(errorsOf(validate.TenantLint(lit, strictFor()))).To(ConsistOf(ContainSubstring(`tenant "globex"`)))
	})

	It("requires loki.enrich, which copies target labels onto entries, to copy a literal list without __tenant_id__", func() {
		enrich := func(copy string) string {
			return "loki.enrich \"e\" {\n  targets            = discovery.kubernetes.pods.targets\n" +
				"  target_match_label = \"host\"\n" + copy + "  forward_to         = []\n}\n"
		}
		Expect(validate.TenantLint(enrich("  labels_to_copy     = [\"team\", \"env\"]\n"), strictFor())).To(BeEmpty())
		for _, bad := range []string{
			"",
			"  labels_to_copy     = [\"team\", \"__tenant_id__\"]\n",
			"  labels_to_copy     = array.concat([\"team\"], [])\n",
		} {
			Expect(errorsOf(validate.TenantLint(enrich(bad), strictFor()))).
				To(ConsistOf(ContainSubstring("labels_to_copy")), bad)
		}
	})

	It("checks components inside a foreach template like those at the top level", func() {
		foreach := func(inner string) string {
			return "foreach \"f\" {\n  collection = [1]\n  var = \"x\"\n  template {\n" + indent(indent(inner)) + "  }\n}\n"
		}
		unownedWrite := "loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"globex\"\n  }\n}\n"
		Expect(errorsOf(validate.TenantLint(foreach(unownedWrite), strictFor()))).
			To(ConsistOf(ContainSubstring(`tenant "globex" is not one this organisation owns`)))
		Expect(errorsOf(validate.TenantLint(foreach(lokiWrite("encoding.from_json(\"{}\")")), strictFor()))).
			To(ConsistOf(ContainSubstring("not a literal map")))
		// A push source in the template is followed to its writer.
		push := "loki.source.api \"a\" {\n  http {\n    listen_port = 3100\n  }\n  forward_to = [loki.write.w.receiver]\n}\n" +
			strings.Replace(unownedWrite, "globex", "acme", 1)
		Expect(errorsOf(validate.TenantLint(foreach(push), strictFor()))).
			To(ConsistOf(ContainSubstring("forwards the tenant a client sends")))
	})

	It("does not change a non-strict org's verdict", func() {
		Expect(validate.TenantLint(lokiWrite(`encoding.from_json("{}")`), validate.TenantPolicy{})).To(BeEmpty())
	})

	It("accepts the k8s-monitoring chart's output in strict mode", func() {
		// (labelmapdrop_test.go runs the real alloy validate on the same
		// served form.)
		for _, name := range []string{"logs", "metrics"} {
			Expect(errorsOf(validate.TenantLint(insertDrops(k8smon(name)), ownsAll()))).To(BeEmpty(), name)
			p := ownsAll()
			p.Inline = func(_ context.Context, c string) (string, error) { return validate.InsertTenantDrops(c) }
			r := validate.WithTenantLint(context.Background(), validate.Result{Valid: true}, k8smon(name), p)
			Expect(r.Valid).To(BeTrue(), "%s: %v", name, r.Diagnostics)
		}
	})
})
