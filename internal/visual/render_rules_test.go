package visual_test

import (
	"math/rand"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// All specs in this file render against corpusSchema() (render_test.go), the
// schema the product actually serves: the embedded artifact for the pinned
// Alloy version, deep-merged with the same overlay the HTTP handler merges.
//
// Until 2026-08-19 this file declared its own hand-written roleSchema() with
// two fabricated components (test.capsule, test.loop) and reduced, hand-typed
// port models for five real ones. That schema was blind to the shipped
// artifact: setting prometheus.remote_write's receiver export to role
// "produces" in the committed artifact left every spec in this file green
// except the two that already loaded the real schema. Routing every spec
// through the real, merged payload is what makes them evidence.

func node(id, component, label string, props map[string]interface{}) visual.GraphNode {
	return visual.GraphNode{ID: id, Component: component, Label: label, Props: props}
}

func edge(id, from, fromPort, to, toPort string) visual.GraphEdge {
	return visual.GraphEdge{ID: id, From: visual.PortRef{Node: from, Port: fromPort}, To: visual.PortRef{Node: to, Port: toPort}}
}

func doc(nodes []visual.GraphNode, edges []visual.GraphEdge) visual.GraphDocument {
	return visual.GraphDocument{Kind: "alloy-graph/v1", SchemaVersion: "v1.18.1", Nodes: nodes, Edges: edges}
}

func codes(ds []visual.RenderDiagnostic) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return out
}

var _ = Describe("Renderer — D1 direction", func() {
	schema := corpusSchema()

	// D1(a): discovery.kubernetes exports DATA (role produces), so the
	// consumer holds the reference. The canvas edge points source → dest.
	It("emits a data-export reference inside the consumer", func() {
		d := doc(
			[]visual.GraphNode{
				node("n1", "discovery.kubernetes", "k8s", map[string]interface{}{"role": "pod"}),
				node("n2", "prometheus.scrape", "app", nil),
			},
			[]visual.GraphEdge{edge("e1", "n1", "targets", "n2", "targets")},
		)
		r := visual.Render(d, schema)
		Expect(r.Diagnostics).To(BeEmpty())
		// Bare, not `[...]`: a discovery export is already a
		// []discovery.Target, and Alloy v1.18.1 refuses a list of them at
		// evaluation time (refValue in render.go; docs/proofs/sandbox-sim-e2e.md §1).
		Expect(r.Content).To(ContainSubstring("prometheus.scrape \"app\" {\n  targets = discovery.kubernetes.k8s.targets\n}"))
	})

	// D1(b): prometheus.remote_write's receiver is an ACCEPTS export, so the
	// producer holds the reference — the edge still runs scrape → remote_write.
	It("emits a receiver-export reference inside the producer", func() {
		d := doc(
			[]visual.GraphNode{
				node("n2", "prometheus.scrape", "app", nil),
				node("n3", "prometheus.remote_write", "sink", nil),
			},
			[]visual.GraphEdge{edge("e2", "n2", "forward_to", "n3", "receiver")},
		)
		r := visual.Render(d, schema)
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).To(ContainSubstring("prometheus.scrape \"app\" {\n  forward_to = [prometheus.remote_write.sink.receiver]\n}"))
		Expect(r.Content).To(ContainSubstring("prometheus.remote_write \"sink\" {\n}"))
	})

	It("emits a nested-block reference at the port's declared path", func() {
		d := doc(
			[]visual.GraphNode{
				node("n1", "otelcol.receiver.otlp", "ingest", nil),
				node("n2", "otelcol.exporter.otlp", "remote", nil),
			},
			[]visual.GraphEdge{
				edge("e1", "n1", "output.metrics", "n2", "input"),
				edge("e2", "n1", "output.logs", "n2", "input"),
			},
		)
		r := visual.Render(d, schema)
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).To(ContainSubstring("otelcol.receiver.otlp \"ingest\" {\n" +
			"  output {\n" +
			"    metrics = [otelcol.exporter.otlp.remote.input]\n" +
			"    logs = [otelcol.exporter.otlp.remote.input]\n" +
			"  }\n" +
			"}"))
	})

	It("reports an edge that connects two arguments instead of dropping it", func() {
		d := doc(
			[]visual.GraphNode{
				node("n1", "prometheus.scrape", "a", nil),
				node("n2", "prometheus.scrape", "b", nil),
			},
			[]visual.GraphEdge{edge("e1", "n1", "forward_to", "n2", "targets")},
		)
		r := visual.Render(d, schema)
		Expect(codes(r.Diagnostics)).To(ContainElement("edge_unresolved"))
		Expect(r.Content).NotTo(ContainSubstring("forward_to"))
	})

	It("renders both directions of a real metrics pipeline from the shipped artifact", func() {
		d := doc(
			[]visual.GraphNode{
				node("n1", "discovery.kubernetes", "k8s", map[string]interface{}{"role": "pod"}),
				node("n2", "prometheus.scrape", "app", map[string]interface{}{"job_name": "app", "scrape_interval": "30s"}),
				node("n3", "prometheus.remote_write", "sink", map[string]interface{}{
					"endpoint": []interface{}{map[string]interface{}{"url": "https://prom/api/v1/write"}},
				}),
			},
			[]visual.GraphEdge{
				edge("e1", "n1", "targets", "n2", "targets"),
				edge("e2", "n2", "forward_to", "n3", "receiver"),
			},
		)
		r := visual.Render(d, schema)
		Expect(r.Diagnostics).To(BeEmpty())
		// Verified with `alloy RUN` (not just `alloy validate`) in the pinned
		// grafana/alloy:v1.18.1 image. `validate` accepts the bracketed
		// `targets = [discovery....targets]` spelling this line used to assert
		// and `run` refuses it — which is exactly how that defect survived
		// until a real sandbox run hit it (docs/proofs/sandbox-sim-e2e.md §1).
		Expect(r.Content).To(Equal(
			"// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); graph revision 3, schema v1.18.1\n" +
				"\ndiscovery.kubernetes \"k8s\" {\n  role = \"pod\"\n}\n" +
				"\nprometheus.scrape \"app\" {\n" +
				"  targets = discovery.kubernetes.k8s.targets\n" +
				"  forward_to = [prometheus.remote_write.sink.receiver]\n" +
				"  job_name = \"app\"\n" +
				"  scrape_interval = \"30s\"\n}\n" +
				"\nprometheus.remote_write \"sink\" {\n" +
				"  endpoint {\n    url = \"https://prom/api/v1/write\"\n  }\n}\n"))
	})

	It("renders a real OTel chain from the shipped artifact", func() {
		d := doc(
			[]visual.GraphNode{
				node("n1", "otelcol.receiver.otlp", "ingest", nil),
				node("n2", "otelcol.processor.batch", "batcher", nil),
				node("n3", "otelcol.exporter.otlp", "remote", nil),
			},
			[]visual.GraphEdge{
				edge("e1", "n1", "output.metrics", "n2", "input"),
				edge("e2", "n2", "output.metrics", "n3", "input"),
			},
		)
		r := visual.Render(d, schema)
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).To(ContainSubstring("otelcol.receiver.otlp \"ingest\" {\n  output {\n    metrics = [otelcol.processor.batch.batcher.input]\n  }\n}"))
		Expect(r.Content).To(ContainSubstring("otelcol.processor.batch \"batcher\" {\n  output {\n    metrics = [otelcol.exporter.otlp.remote.input]\n  }\n}"))
	})
})

var _ = Describe("Renderer — nested blocks (D2)", func() {
	schema := corpusSchema()

	It("emits repeatable blocks, nested blocks and indentation", func() {
		d := doc([]visual.GraphNode{
			node("n1", "prometheus.remote_write", "sink", map[string]interface{}{
				"endpoint": []interface{}{
					map[string]interface{}{
						"url":            "https://a/write",
						"remote_timeout": "30s",
						"basic_auth": map[string]interface{}{
							"username": "u",
							"password": map[string]interface{}{"$expr": "remote.kubernetes.secret.s.data[\"pw\"]"},
						},
					},
					map[string]interface{}{"url": "https://b/write"},
				},
			}),
		}, nil)
		r := visual.Render(d, schema)
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).To(ContainSubstring(
			"prometheus.remote_write \"sink\" {\n" +
				"  endpoint {\n" +
				"    url = \"https://a/write\"\n" +
				"    remote_timeout = \"30s\"\n" +
				"    basic_auth {\n" +
				"      username = \"u\"\n" +
				"      password = remote.kubernetes.secret.s.data[\"pw\"]\n" +
				"    }\n" +
				"  }\n" +
				"  endpoint {\n" +
				"    url = \"https://b/write\"\n" +
				"  }\n" +
				"}"))
	})

	It("emits an empty block for an empty object", func() {
		d := doc([]visual.GraphNode{
			node("n1", "otelcol.receiver.otlp", "ingest", map[string]interface{}{"grpc": map[string]interface{}{}}),
		}, nil)
		r := visual.Render(d, schema)
		Expect(r.Content).To(ContainSubstring("otelcol.receiver.otlp \"ingest\" {\n  grpc {\n  }\n}"))
	})

	It("reports a repeated non-repeatable block", func() {
		d := doc([]visual.GraphNode{
			node("n1", "otelcol.receiver.otlp", "ingest", map[string]interface{}{
				"grpc": []interface{}{map[string]interface{}{}, map[string]interface{}{}},
			}),
		}, nil)
		r := visual.Render(d, schema)
		Expect(codes(r.Diagnostics)).To(ContainElement("block_not_repeatable"))
	})

	It("reports a block whose value is not an object", func() {
		d := doc([]visual.GraphNode{
			node("n1", "otelcol.receiver.otlp", "ingest", map[string]interface{}{"grpc": "0.0.0.0:4317"}),
		}, nil)
		r := visual.Render(d, schema)
		Expect(codes(r.Diagnostics)).To(ContainElement("prop_type_mismatch"))
	})
})

var _ = Describe("Renderer — typed serialization", func() {
	schema := corpusSchema()

	render := func(props map[string]interface{}) visual.RenderResult {
		return visual.Render(doc([]visual.GraphNode{node("n1", "prometheus.scrape", "app", props)}, nil), schema)
	}

	It("renders each declared type in its Alloy form", func() {
		r := render(map[string]interface{}{
			"job_name":         "app",
			"scrape_interval":  "30s",
			"sample_limit":     float64(1000),
			"honor_labels":     true,
			"params":           map[string]interface{}{"b": "2", "a-x": "1"},
			"scrape_protocols": []interface{}{"PrometheusText1.0.0", float64(2)},
		})
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).To(ContainSubstring("  job_name = \"app\"\n"))
		Expect(r.Content).To(ContainSubstring("  scrape_interval = \"30s\"\n"))
		Expect(r.Content).To(ContainSubstring("  sample_limit = 1000\n"))
		Expect(r.Content).To(ContainSubstring("  honor_labels = true\n"))
		Expect(r.Content).To(ContainSubstring("  params = {\"a-x\" = \"1\", b = \"2\"}\n"))
		Expect(r.Content).To(ContainSubstring("  scrape_protocols = [\"PrometheusText1.0.0\", 2]\n"))
	})

	It("sorts map keys by byte order, matching the TS renderer", func() {
		r := render(map[string]interface{}{"params": map[string]interface{}{"ab": "2", "a_b": "1", "B": "3"}})
		Expect(r.Content).To(ContainSubstring("params = {B = \"3\", a_b = \"1\", ab = \"2\"}"))
	})

	It("never inlines a secret", func() {
		r := render(map[string]interface{}{"bearer_token": "hunter2"})
		Expect(codes(r.Diagnostics)).To(ContainElement("secret_by_value"))
		Expect(r.Content).NotTo(ContainSubstring("hunter2"))
	})

	It("accepts a secret supplied as an expression", func() {
		r := render(map[string]interface{}{"bearer_token": map[string]interface{}{"$expr": "sys.env(\"TOKEN\")"}})
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).To(ContainSubstring("  bearer_token = sys.env(\"TOKEN\")\n"))
	})

	It("refuses to quote a string into a list-typed attribute", func() {
		r := render(map[string]interface{}{"scrape_protocols": "PrometheusText1.0.0"})
		Expect(codes(r.Diagnostics)).To(ContainElement("prop_type_mismatch"))
		Expect(r.Content).NotTo(ContainSubstring("scrape_protocols"))
	})

	It("reports a duration without a unit", func() {
		r := render(map[string]interface{}{"scrape_interval": float64(30)})
		Expect(codes(r.Diagnostics)).To(ContainElement("invalid_duration"))
	})

	It("coerces a numeric string typed as number", func() {
		r := render(map[string]interface{}{"sample_limit": "1000"})
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).To(ContainSubstring("  sample_limit = 1000\n"))
	})

	It("omits a null prop rather than stringifying it", func() {
		r := render(map[string]interface{}{"job_name": nil})
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).NotTo(ContainSubstring("job_name"))
	})

	// relabel_rules is capsule-typed: it can only be satisfied by a reference
	// expression (e.g. another component's export), never a literal.
	It("emits a capsule value as an expression and warns about unknown props", func() {
		r := visual.Render(doc([]visual.GraphNode{node("n1", "loki.source.docker", "c", map[string]interface{}{
			"host":          "unix:///var/run/docker.sock",
			"relabel_rules": "discovery.relabel.x.rules",
			"nope":          "x",
		})}, nil), schema)
		Expect(r.Content).To(ContainSubstring("  host = \"unix:///var/run/docker.sock\"\n"))
		Expect(r.Content).To(ContainSubstring("  relabel_rules = discovery.relabel.x.rules\n"))
		Expect(codes(r.Diagnostics)).To(ContainElement("unknown_prop"))
		Expect(r.Content).NotTo(ContainSubstring("nope"))
	})

	It("formats large numbers without an exponent, matching the TS renderer", func() {
		r := render(map[string]interface{}{"sample_limit": 1e21})
		Expect(r.Content).To(ContainSubstring("sample_limit = 1" + strings.Repeat("0", 21) + "\n"))
	})
})

var _ = Describe("Renderer — diagnostics", func() {
	schema := corpusSchema()

	It("accumulates diagnostics instead of replacing them", func() {
		d := doc([]visual.GraphNode{
			node("n1", "nonexistent.component", "x", nil),
			node("n2", "prometheus.scrape", "app", map[string]interface{}{"bearer_token": "hunter2"}),
		}, nil)
		r := visual.Render(d, schema)
		Expect(codes(r.Diagnostics)).To(ContainElements("unknown_component", "secret_by_value"))
	})

	It("reports every colliding label pair", func() {
		d := doc([]visual.GraphNode{
			node("a", "prometheus.remote_write", "my-sink", nil),
			node("b", "prometheus.remote_write", "my_sink", nil),
			node("c", "prometheus.scrape", "one two", nil),
			node("e", "prometheus.scrape", "one-two", nil),
		}, nil)
		r := visual.Render(d, schema)
		Expect(r.Content).To(BeEmpty())
		Expect(codes(r.Diagnostics)).To(Equal([]string{"label_collision", "label_collision"}))
	})

	It("keeps unknown-component props instead of dropping them", func() {
		d := doc([]visual.GraphNode{node("n1", "nonexistent.component", "x", map[string]interface{}{"b": "2", "a": float64(1)})}, nil)
		r := visual.Render(d, schema)
		Expect(codes(r.Diagnostics)).To(ContainElement("unknown_component"))
		Expect(r.Content).To(ContainSubstring("nonexistent.component \"x\" {\n  a = 1\n  b = \"2\"\n}"))
	})

	It("reports a prop that is also satisfied by a wire", func() {
		d := doc([]visual.GraphNode{
			node("n1", "discovery.kubernetes", "k8s", nil),
			node("n2", "prometheus.scrape", "app", map[string]interface{}{"targets": []interface{}{}}),
		}, []visual.GraphEdge{edge("e1", "n1", "targets", "n2", "targets")})
		r := visual.Render(d, schema)
		Expect(codes(r.Diagnostics)).To(ContainElement("prop_wire_conflict"))
		Expect(r.Content).To(ContainSubstring("targets = discovery.kubernetes.k8s.targets"))
	})

	It("reports an empty binding on both fields and skips the line", func() {
		d := visual.GraphDocument{
			SchemaVersion: "v1.18.1",
			Nodes:         []visual.GraphNode{node("n1", "prometheus.scrape", "app", nil)},
			Bindings: []visual.GraphBinding{
				{Node: "n1", Prop: "job_name", Ref: visual.BindingRef{Expr: "  "}},
				{Node: "n1", Prop: "", Ref: visual.BindingRef{Expr: "x"}},
			},
		}
		r := visual.Render(d, schema)
		Expect(codes(r.Diagnostics)).To(Equal([]string{"empty_binding_expr", "empty_binding_prop"}))
		Expect(r.Content).To(ContainSubstring("prometheus.scrape \"app\" {\n}"))
	})

	// Two prometheus.relabel nodes wired into each other's receiver, standing
	// in for a two-node cycle: n1.forward_to → n2.receiver, n2.forward_to →
	// n1.receiver — the D1(b) producer-holds-the-reference shape, looped.
	It("reports a cycle and still orders it deterministically", func() {
		nodes := []visual.GraphNode{
			node("n1", "prometheus.relabel", "zzz", nil),
			node("n2", "prometheus.relabel", "aaa", nil),
		}
		edges := []visual.GraphEdge{
			edge("e1", "n1", "forward_to", "n2", "receiver"),
			edge("e2", "n2", "forward_to", "n1", "receiver"),
		}
		first := visual.Render(doc(nodes, edges), schema)
		Expect(codes(first.Diagnostics)).To(ContainElement("cycle"))

		rng := rand.New(rand.NewSource(7))
		for range 5 {
			shuffled := make([]visual.GraphNode, len(nodes))
			copy(shuffled, nodes)
			rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			Expect(visual.Render(doc(shuffled, edges), schema).Content).To(Equal(first.Content))
		}
		Expect(strings.Index(first.Content, "\"aaa\"")).To(BeNumerically("<", strings.Index(first.Content, "\"zzz\"")))
	})

	It("sanitizes labels by code point, matching the TS renderer", func() {
		d := doc([]visual.GraphNode{node("n1", "prometheus.scrape", "a😀b", nil)}, nil)
		Expect(visual.Render(d, schema).Content).To(ContainSubstring("prometheus.scrape \"a_b\""))
	})
})
