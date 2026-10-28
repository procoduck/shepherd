package visual_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// Nested-block bindings (#426): a doc.bindings[] entry whose prop is a path
// into a nested block — `endpoint[0].url`, `client.tls.key_pem`,
// `stage.tenant[0].value` — renders inside that block, and the read-only
// parse recovers it at the same path. A prop with no `.` and no `[` is the
// pre-existing top-level binding and keeps its exact rendering (the
// bindings-secret corpus golden pins it).

func bind(nodeID, prop, refNode, export, expr string) visual.GraphBinding {
	return visual.GraphBinding{Node: nodeID, Prop: prop, Ref: visual.BindingRef{Node: refNode, Export: export, Expr: expr}}
}

func withBindings(d visual.GraphDocument, b ...visual.GraphBinding) visual.GraphDocument {
	d.Bindings = b
	return d
}

var _ = Describe("binding paths", func() {
	schema := corpusSchema()
	secret := node("s1", "remote.kubernetes.secret", "creds", map[string]interface{}{"namespace": "mon", "name": "creds"})
	file := node("f1", "local.file", "ep", map[string]interface{}{"filename": "/etc/ep"})
	const pw = `remote.kubernetes.secret.creds.data["password"]`

	Describe("ParseBindingPath", func() {
		It("splits names and indices", func() {
			steps, err := visual.ParseBindingPath("endpoint[1].basic_auth.password")
			Expect(err).NotTo(HaveOccurred())
			Expect(steps).To(Equal([]visual.BindingPathToken{
				{Name: "endpoint", Index: 1, Indexed: true},
				{Name: "basic_auth"},
				{Name: "password"},
			}))
		})

		It("reports a top-level prop as not nested", func() {
			Expect(visual.IsNestedBindingProp("bearer_token")).To(BeFalse())
			Expect(visual.IsNestedBindingProp("endpoint[0].url")).To(BeTrue())
			Expect(visual.IsNestedBindingProp("client.endpoint")).To(BeTrue())
		})

		DescribeTable("refuses malformed paths",
			func(prop string) {
				_, err := visual.ParseBindingPath(prop)
				Expect(err).To(HaveOccurred())
			},
			Entry("empty", ""),
			Entry("leading dot", ".url"),
			Entry("trailing dot", "endpoint."),
			Entry("double dot", "endpoint..url"),
			Entry("open bracket", "endpoint[.url"),
			Entry("non-numeric index", "endpoint[x].url"),
			Entry("negative index", "endpoint[-1].url"),
			Entry("index on leaf", "endpoint[0].url[0]"),
			Entry("bad identifier", "end-point.url"),
			Entry("leading zero index", "endpoint[01].url"),
		)
	})

	Describe("ResolveBindingPath", func() {
		It("resolves a dotted block name by the schema", func() {
			steps, leaf, err := visual.ResolveBindingPath(schema.Components["loki.process"], "stage.tenant[0].value")
			Expect(err).NotTo(HaveOccurred())
			Expect(steps).To(Equal([]visual.BindingBlockStep{{Name: "stage.tenant", Index: 0, Repeatable: true}}))
			Expect(leaf.Name).To(Equal("value"))
			Expect(visual.FormatBindingPath(steps, leaf.Name)).To(Equal("stage.tenant[0].value"))
		})

		It("reads an omitted index on a repeatable block as instance 0 and formats it canonically", func() {
			steps, leaf, err := visual.ResolveBindingPath(schema.Components["prometheus.remote_write"], "endpoint.basic_auth.password")
			Expect(err).NotTo(HaveOccurred())
			Expect(visual.FormatBindingPath(steps, leaf.Name)).To(Equal("endpoint[0].basic_auth.password"))
		})

		DescribeTable("refuses paths the schema does not hold",
			func(component, prop string) {
				_, _, err := visual.ResolveBindingPath(schema.Components[component], prop)
				Expect(err).To(HaveOccurred())
			},
			Entry("unknown block", "prometheus.remote_write", "nowhere[0].url"),
			Entry("unknown leaf", "prometheus.remote_write", "endpoint[0].nope"),
			Entry("leaf is a block", "prometheus.remote_write", "endpoint[0].basic_auth"),
			Entry("index on a non-repeatable block", "otelcol.exporter.otlp", "client[1].endpoint"),
			Entry("index inside a dotted block name", "loki.process", "stage[0].tenant.value"),
		)
	})

	Describe("rendering", func() {
		It("emits a binding inside a single nested block", func() {
			d := withBindings(doc([]visual.GraphNode{
				file,
				node("o1", "otelcol.exporter.otlp", "out", map[string]interface{}{"client": map[string]interface{}{"compression": "gzip"}}),
			}, nil), bind("o1", "client.endpoint", "f1", "content", "local.file.ep.content"))
			r := visual.Render(d, schema)
			Expect(r.Diagnostics).To(BeEmpty())
			Expect(r.Content).To(ContainSubstring("  client {\n    endpoint = local.file.ep.content\n    compression = \"gzip\"\n  }\n"))
			Expect(r.Content).NotTo(ContainSubstring("client.endpoint ="))
		})

		It("emits into the indexed instance of a repeated block, deeper nesting included", func() {
			d := withBindings(doc([]visual.GraphNode{
				secret,
				node("w1", "prometheus.remote_write", "sink", map[string]interface{}{"endpoint": []interface{}{
					map[string]interface{}{"url": "https://a/api/v1/write"},
					map[string]interface{}{"url": "https://b/api/v1/write"},
				}}),
			}, nil),
				bind("w1", "endpoint[1].bearer_token", "s1", "data", `remote.kubernetes.secret.creds.data["token"]`),
				bind("w1", "endpoint[0].basic_auth.password", "s1", "data", pw),
			)
			r := visual.Render(d, schema)
			Expect(r.Diagnostics).To(BeEmpty())
			Expect(r.Content).To(ContainSubstring(`prometheus.remote_write "sink" {
  endpoint {
    url = "https://a/api/v1/write"
    basic_auth {
      password = remote.kubernetes.secret.creds.data["password"]
    }
  }
  endpoint {
    url = "https://b/api/v1/write"
    bearer_token = remote.kubernetes.secret.creds.data["token"]
  }
}
`))
		})

		It("creates the block when the props have none and the index is 0", func() {
			d := withBindings(doc([]visual.GraphNode{
				file,
				node("o1", "otelcol.exporter.otlp", "out", nil),
			}, nil), bind("o1", "client.tls.ca_pem", "f1", "content", "local.file.ep.content"))
			r := visual.Render(d, schema)
			Expect(r.Diagnostics).To(BeEmpty())
			Expect(r.Content).To(ContainSubstring("  client {\n    tls {\n      ca_pem = local.file.ep.content\n    }\n  }\n"))
		})

		It("resolves a dotted block name", func() {
			d := withBindings(doc([]visual.GraphNode{
				file,
				node("p1", "loki.process", "tenant", map[string]interface{}{
					"forward_to":   map[string]interface{}{"$expr": "[]"},
					"stage.tenant": []interface{}{map[string]interface{}{}},
				}),
			}, nil), bind("p1", "stage.tenant[0].value", "f1", "content", "local.file.ep.content"))
			r := visual.Render(d, schema)
			Expect(codes(r.Diagnostics)).NotTo(ContainElement("binding_path_invalid"))
			Expect(r.Content).To(ContainSubstring("  stage.tenant {\n    value = local.file.ep.content\n  }\n"))
		})

		It("does not mutate the document's props", func() {
			props := map[string]interface{}{"endpoint": []interface{}{map[string]interface{}{"url": "https://a"}}}
			d := withBindings(doc([]visual.GraphNode{secret, node("w1", "prometheus.remote_write", "sink", props)}, nil),
				bind("w1", "endpoint[0].basic_auth.password", "s1", "data", pw))
			before, err := json.Marshal(props)
			Expect(err).NotTo(HaveOccurred())
			visual.Render(d, schema)
			after, err := json.Marshal(props)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(after)).To(Equal(string(before)))
		})

		It("lets the binding win over a literal at the same path, with a warning", func() {
			d := withBindings(doc([]visual.GraphNode{
				file,
				node("o1", "otelcol.exporter.otlp", "out", map[string]interface{}{"client": map[string]interface{}{"endpoint": "collector:4317"}}),
			}, nil), bind("o1", "client.endpoint", "f1", "content", "local.file.ep.content"))
			r := visual.Render(d, schema)
			Expect(codes(r.Diagnostics)).To(Equal([]string{"binding_overrides_prop"}))
			Expect(r.Content).To(ContainSubstring("    endpoint = local.file.ep.content\n"))
			Expect(r.Content).NotTo(ContainSubstring("collector:4317"))
		})

		DescribeTable("refuses a path that does not resolve, emitting nothing for it",
			func(component string, props map[string]interface{}, prop string) {
				d := withBindings(doc([]visual.GraphNode{file, node("x1", component, "x", props)}, nil),
					bind("x1", prop, "f1", "content", "local.file.ep.content"))
				r := visual.Render(d, schema)
				Expect(codes(r.Diagnostics)).To(ContainElement("binding_path_invalid"))
				Expect(r.Content).NotTo(ContainSubstring("local.file.ep.content"))
			},
			Entry("malformed", "otelcol.exporter.otlp", nil, "client..endpoint"),
			Entry("unknown block", "otelcol.exporter.otlp", nil, "nowhere.endpoint"),
			Entry("index past the instances", "prometheus.remote_write",
				map[string]interface{}{"endpoint": []interface{}{map[string]interface{}{"url": "https://a"}}}, "endpoint[1].url"),
			Entry("a block value that is not a block", "otelcol.exporter.otlp",
				map[string]interface{}{"client": "nope"}, "client.endpoint"),
			Entry("a component the schema does not describe", "custom.thing", nil, "client.endpoint"),
		)
	})

	Describe("the read-only parse", func() {
		It("recovers nested bindings at their canonical path, and the text round-trips", func() {
			d := withBindings(doc([]visual.GraphNode{
				secret, file,
				node("w1", "prometheus.remote_write", "sink", map[string]interface{}{"endpoint": []interface{}{
					map[string]interface{}{"url": "https://a/api/v1/write"},
					map[string]interface{}{"url": "https://b/api/v1/write"},
				}}),
				node("o1", "otelcol.exporter.otlp", "out", nil),
			}, nil),
				bind("w1", "endpoint[0].basic_auth.password", "s1", "data", pw),
				bind("w1", "endpoint[1].bearer_token", "s1", "data", `remote.kubernetes.secret.creds.data["token"]`),
				bind("o1", "client.endpoint", "f1", "content", "local.file.ep.content"),
			)
			text := visual.Render(d, schema)
			Expect(text.Diagnostics).To(BeEmpty())

			parsed := visual.ParseAlloy(text.Content, "alloy-v1.21.2", schema)
			Expect(parsed.Opaque).To(BeFalse(), parsed.Warning)
			byProp := map[string]visual.GraphBinding{}
			for _, b := range parsed.Bindings {
				byProp[b.Node+" "+b.Prop] = b
			}
			idOf := map[string]string{}
			for _, n := range parsed.Doc.Nodes {
				idOf[n.Label] = n.ID
			}
			for _, want := range d.Bindings {
				var label string
				for _, n := range d.Nodes {
					if n.ID == want.Node {
						label = n.Label
					}
				}
				got, ok := byProp[idOf[label]+" "+want.Prop]
				Expect(ok).To(BeTrue(), "no recovered binding for %s %s in %v", label, want.Prop, parsed.Bindings)
				Expect(got.Ref.Export).To(Equal(want.Ref.Export))
				Expect(got.Ref.Expr).To(Equal(want.Ref.Expr))
			}

			// The builder's draft drops the projection; the props hold the
			// expressions, so the text renders back byte for byte.
			draft := parsed.Doc
			draft.Bindings = nil
			Expect(visual.Render(draft, schema).Content).To(Equal(text.Content))
		})
	})
})
