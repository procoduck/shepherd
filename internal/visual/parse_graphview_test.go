package visual_test

import (
	"encoding/json"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// The read-only graph view of a pipeline that was NOT built in the visual
// builder: the Alloy that grafana/k8s-monitoring renders (a `declare` block,
// a custom-component instance, secret references nested inside function
// calls). See the fixture's header for its provenance.
var _ = Describe("ParseAlloy on a chart-generated pipeline (graph view)", func() {
	var result visual.ParseResult
	byID := map[string]visual.GraphNode{}

	BeforeEach(func() {
		content, err := os.ReadFile("testdata/k8s-monitoring/singleton.alloy")
		Expect(err).NotTo(HaveOccurred())
		result = visual.ParseAlloy(string(content), "v1.20.1", corpusSchema())
		byID = map[string]visual.GraphNode{}
		for _, n := range result.Doc.Nodes {
			byID[n.ID] = n
		}
	})

	It("keeps every top-level block as a node, the unmapped ones as opaque", func() {
		Expect(result.Doc.Nodes).To(HaveLen(12))
		var opaque []string
		for _, n := range result.Doc.Nodes {
			if strings.HasPrefix(n.Component, "opaque.") {
				opaque = append(opaque, n.Component)
			}
		}
		Expect(opaque).To(ConsistOf("opaque.declare", "opaque.cluster_events"))
		Expect(result.Opaque).To(BeTrue())
		Expect(result.Warning).To(ContainSubstring(`"cluster_events" is not a component`))
	})

	It("wires the opaque instance to its destination, and only real ports become edges", func() {
		Expect(result.Doc.Edges).To(HaveLen(7))
		var fromOpaque []visual.GraphEdge
		for _, e := range result.Doc.Edges {
			Expect(e.To.Port).NotTo(Equal("data"), "a secret reference is not a wire")
			if strings.HasPrefix(byID[e.From.Node].Component, "opaque.") {
				fromOpaque = append(fromOpaque, e)
			}
		}
		Expect(fromOpaque).To(HaveLen(1))
		Expect(fromOpaque[0].From.Port).To(Equal("logs_destinations"))
		Expect(byID[fromOpaque[0].To.Node].Component).To(Equal("loki.write"))
		Expect(fromOpaque[0].To.Port).To(Equal("receiver"))
	})

	It("carries each opaque block's printed source in props under $raw", func() {
		for _, n := range result.Doc.Nodes {
			raw, has := n.Props["$raw"]
			if !strings.HasPrefix(n.Component, "opaque.") {
				Expect(has).To(BeFalse(), "%s is mapped and must not carry $raw", n.Component)
				continue
			}
			Expect(raw).To(BeAssignableToTypeOf(""))
		}
		var declare visual.GraphNode
		for _, n := range result.Doc.Nodes {
			if n.Component == "opaque.declare" {
				declare = n
			}
		}
		raw, isString := declare.Props["$raw"].(string)
		Expect(isString).To(BeTrue())
		Expect(raw).To(HavePrefix(`declare "cluster_events" {`))
		Expect(raw).To(ContainSubstring(`loki.source.kubernetes_events "cluster_events"`))
		Expect(raw).To(ContainSubstring(`forward_to = argument.logs_destinations.value`))
	})

	It("reports secret references inside calls and index expressions as bindings, not edges", func() {
		Expect(result.Bindings).To(HaveLen(4))
		type b struct{ consumer, prop, source, export, expr string }
		got := []b{}
		for _, bd := range result.Bindings {
			got = append(got, b{
				byID[bd.Node].Component + "." + byID[bd.Node].Label, bd.Prop,
				byID[bd.Ref.Node].Component + "." + byID[bd.Ref.Node].Label, bd.Ref.Export, bd.Ref.Expr,
			})
		}
		Expect(got).To(ConsistOf(
			b{
				"loki.write.loki", "endpoint.basic_auth.username", "remote.kubernetes.secret.loki", "data",
				`convert.nonsensitive(remote.kubernetes.secret.loki.data["username"])`,
			},
			b{
				"loki.write.loki", "endpoint.basic_auth.password", "remote.kubernetes.secret.loki", "data",
				`remote.kubernetes.secret.loki.data["password"]`,
			},
			b{
				"prometheus.remote_write.prometheus", "endpoint.basic_auth.username", "remote.kubernetes.secret.prometheus", "data",
				`convert.nonsensitive(remote.kubernetes.secret.prometheus.data["username"])`,
			},
			b{
				"prometheus.remote_write.prometheus", "endpoint.basic_auth.password", "remote.kubernetes.secret.prometheus", "data",
				`remote.kubernetes.secret.prometheus.data["password"]`,
			},
		))
		// The bindings are a projection: the renderable document is unchanged,
		// the expression still lives in props, and Doc.Bindings stays empty.
		Expect(result.Doc.Bindings).To(BeEmpty())
		for _, n := range result.Doc.Nodes {
			if n.Component != "loki.write" {
				continue
			}
			endpoint, isMap := n.Props["endpoint"].(map[string]interface{})
			Expect(isMap).To(BeTrue())
			auth, isMap := endpoint["basic_auth"].(map[string]interface{})
			Expect(isMap).To(BeTrue())
			Expect(auth["password"]).To(Equal(map[string]interface{}{"$expr": `remote.kubernetes.secret.loki.data["password"]`}))
		}
	})

	// The mocked Playwright suite (web/tests/specs/visual-graph-view.spec.ts)
	// replays this parse as its GraphView response, so the UI is proved
	// against what the server really returns for this config. After a parser
	// change regenerate it with `GEN_GOLDENS=1 ginkgo --focus fixture
	// ./internal/visual`, then `pnpm exec biome format --write` the file in
	// web/ (the comparison below is by value, so Biome's layout is fine).
	It("matches the web suite's GraphView fixture", func() {
		view := result.Doc
		view.Bindings = append(append([]visual.GraphBinding{}, view.Bindings...), result.Bindings...)
		got, err := json.MarshalIndent(map[string]any{
			"graph": view, "opaque": result.Opaque, "warning": result.Warning,
		}, "", "  ")
		Expect(err).NotTo(HaveOccurred())
		const fixture = "../../web/tests/fixtures/k8s-monitoring-graphview.json"
		if os.Getenv("GEN_GOLDENS") != "" {
			Expect(os.WriteFile(fixture, append(got, '\n'), 0o644)).To(Succeed())
		}
		raw, err := os.ReadFile(fixture)
		Expect(err).NotTo(HaveOccurred())
		var want, have any
		Expect(json.Unmarshal(raw, &want)).To(Succeed())
		Expect(json.Unmarshal(got, &have)).To(Succeed())
		Expect(have).To(Equal(want), "the web fixture is stale: regenerate it (see above)")
	})

	It("never renders $raw as an attribute", func() {
		rendered := visual.Render(result.Doc, corpusSchema())
		Expect(rendered.Content).NotTo(ContainSubstring("$raw"))
		for _, d := range rendered.Diagnostics {
			Expect(d.Message).NotTo(ContainSubstring("$raw"))
		}
	})
})

var _ = Describe("ParseAlloy references inside expressions", func() {
	It("turns array.concat of port references into edges and renders the fan-in back", func() {
		original := `discovery.kubernetes "pods" {
  role = "pod"
}

discovery.kubernetes "nodes" {
  role = "node"
}

prometheus.scrape "app" {
  targets = array.concat(discovery.kubernetes.pods.targets, discovery.kubernetes.nodes.targets)
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://prom/api/v1/write"
  }
}
`
		schema := corpusSchema()
		parsed := visual.ParseAlloy(original, "v1.20.1", schema)
		Expect(parsed.Opaque).To(BeFalse(), parsed.Warning)
		Expect(parsed.Doc.Edges).To(HaveLen(3))
		for _, n := range parsed.Doc.Nodes {
			Expect(n.Props).NotTo(HaveKey("targets"))
		}
		rendered := visual.Render(parsed.Doc, schema)
		Expect(rendered.Diagnostics).To(BeEmpty())
		Expect(rendered.Content).To(ContainSubstring(
			"targets = array.concat(discovery.kubernetes.pods.targets, discovery.kubernetes.nodes.targets)"))
	})

	It("draws a port reference buried in another expression, keeps the expression and says so", func() {
		parsed := visual.ParseAlloy(`discovery.kubernetes "pods" {
  role = "pod"
}

discovery.relabel "keep" {
  targets = array.concat(discovery.kubernetes.pods.targets, [{"__address__" = "localhost:9090"}])
}
`, "v1.20.1", corpusSchema())
		Expect(parsed.Doc.Edges).To(HaveLen(1))
		Expect(parsed.Doc.Edges[0].To.Port).To(Equal("targets"))
		var relabel visual.GraphNode
		for _, n := range parsed.Doc.Nodes {
			if n.Component == "discovery.relabel" {
				relabel = n
			}
		}
		Expect(relabel.Props["targets"]).To(HaveKey("$expr"))
		Expect(parsed.Opaque).To(BeTrue())
		Expect(parsed.Warning).To(ContainSubstring(`"targets"`))
		Expect(parsed.Bindings).To(BeEmpty())
	})

	It("reports a top-level non-port reference as a binding and keeps its prop", func() {
		parsed := visual.ParseAlloy(`remote.kubernetes.secret "creds" {
  namespace = "monitoring"
  name = "creds"
}

prometheus.scrape "app" {
  targets = []
  bearer_token = remote.kubernetes.secret.creds.data["token"]
}
`, "v1.20.1", corpusSchema())
		Expect(parsed.Doc.Edges).To(BeEmpty())
		Expect(parsed.Bindings).To(HaveLen(1))
		Expect(parsed.Bindings[0].Prop).To(Equal("bearer_token"))
		Expect(parsed.Bindings[0].Ref.Export).To(Equal("data"))
		Expect(parsed.Bindings[0].Ref.Expr).To(Equal(`remote.kubernetes.secret.creds.data["token"]`))
		for _, n := range parsed.Doc.Nodes {
			if n.Component == "prometheus.scrape" {
				Expect(n.Props["bearer_token"]).To(HaveKey("$expr"))
			}
		}
	})

	It("does not render a $raw prop on a known component either", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "v1.20.1",
			Nodes: []visual.GraphNode{{
				ID: "n1", Component: "discovery.kubernetes", Label: "k",
				Props: map[string]interface{}{"role": "pod", "$raw": `discovery.kubernetes "k" {}`},
			}},
		}
		rendered := visual.Render(doc, corpusSchema())
		Expect(rendered.Content).NotTo(ContainSubstring("$raw"))
		for _, d := range rendered.Diagnostics {
			Expect(d.Message).NotTo(ContainSubstring("$raw"))
		}
	})
})
