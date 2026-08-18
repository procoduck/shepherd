package visual_test

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

func TestVisual(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Visual Suite")
}

// corpusSchema is the shared test schema matching web/tests/fixtures/schema-fixture.ts.
// It defines the exact set of components used by all corpus entries.
func corpusSchema() visual.SchemaPayload {
	return visual.SchemaPayload{
		Meta: visual.SchemaMeta{AlloyVersion: "alloy-v1.18.1"},
		Components: map[string]visual.ComponentSchema{
			"discovery.kubernetes": {
				Category:   "sources",
				Attributes: []visual.AttributeSchema{{Name: "role", Type: "string"}},
				Outputs:    []visual.PortSchema{{Export: "targets", Type: "targets"}},
			},
			"discovery.relabel": {
				Category: "transform",
				Inputs:   []visual.PortSchema{{Prop: "targets", Type: "targets", Cardinality: "list"}},
				Outputs:  []visual.PortSchema{{Export: "output", Type: "targets"}},
			},
			"prometheus.scrape": {
				Category: "transform",
				Attributes: []visual.AttributeSchema{
					{Name: "job_name", Type: "string"},
					{Name: "scrape_interval", Type: "duration"},
					{Name: "password", Type: "secret"},
					{Name: "action", Type: "string"},
				},
				Inputs:  []visual.PortSchema{{Prop: "targets", Type: "targets", Cardinality: "list"}},
				Outputs: []visual.PortSchema{{Export: "metrics", Type: "prom.metrics"}},
			},
			"prometheus.remote_write": {
				Category:   "destinations",
				Attributes: []visual.AttributeSchema{{Name: "password", Type: "secret"}},
				Inputs:     []visual.PortSchema{{Prop: "receiver", Type: "prom.metrics", Cardinality: "list"}},
			},
			"prometheus.relabel": {
				Category: "transform",
				Inputs:   []visual.PortSchema{{Prop: "forward_to", Type: "prom.metrics", Cardinality: "list"}},
				Outputs:  []visual.PortSchema{{Export: "receiver", Type: "prom.metrics"}},
			},
			"loki.source.file": {
				Category: "sources",
				Attributes: []visual.AttributeSchema{
					{Name: "stage_type", Type: "string"},
				},
				Outputs: []visual.PortSchema{{Export: "logs", Type: "loki.logs"}},
			},
			"loki.process": {
				Category: "transform",
				Attributes: []visual.AttributeSchema{
					{Name: "stage_type", Type: "string"},
				},
				Inputs:  []visual.PortSchema{{Prop: "forward_to", Type: "loki.logs", Cardinality: "list"}},
				Outputs: []visual.PortSchema{{Export: "receiver", Type: "loki.logs"}},
			},
			"loki.write": {
				Category: "destinations",
				Inputs:   []visual.PortSchema{{Prop: "receiver", Type: "loki.logs", Cardinality: "list"}},
			},
			"remote.kubernetes.secret": {
				Category: "config",
			},
			"otelcol.receiver.otlp": {
				Category: "sources",
				Outputs: []visual.PortSchema{
					{Export: "output.metrics", Type: "otel.metrics"},
					{Export: "output.logs", Type: "otel.logs"},
					{Export: "output.traces", Type: "otel.traces"},
				},
			},
			"otelcol.processor.batch": {
				Category: "transform",
				Inputs: []visual.PortSchema{
					{Prop: "input.metrics", Type: "otel.metrics", Cardinality: "list"},
					{Prop: "input.logs", Type: "otel.logs", Cardinality: "list"},
					{Prop: "input.traces", Type: "otel.traces", Cardinality: "list"},
				},
				Outputs: []visual.PortSchema{
					{Export: "input.metrics", Type: "otel.metrics"},
					{Export: "input.logs", Type: "otel.logs"},
					{Export: "input.traces", Type: "otel.traces"},
				},
			},
			"otelcol.exporter.otlp": {
				Category: "destinations",
				Inputs: []visual.PortSchema{
					{Prop: "input.metrics", Type: "otel.metrics", Cardinality: "list"},
					{Prop: "input.logs", Type: "otel.logs", Cardinality: "list"},
					{Prop: "input.traces", Type: "otel.traces", Cardinality: "list"},
				},
			},
		},
	}
}

func readCorpus(name string) ([]byte, []byte) {
	GinkgoHelper()
	root := filepath.Join("testdata", "corpus")
	graph, err := os.ReadFile(filepath.Join(root, name+".graph.json"))
	Expect(err).NotTo(HaveOccurred(), "reading %s.graph.json", name)
	golden, err := os.ReadFile(filepath.Join(root, name+".golden.alloy"))
	Expect(err).NotTo(HaveOccurred(), "reading %s.golden.alloy", name)
	return graph, golden
}

func unmarshalDoc(data []byte) visual.GraphDocument {
	GinkgoHelper()
	var doc visual.GraphDocument
	Expect(json.Unmarshal(data, &doc)).To(Succeed())
	return doc
}

var corpusNames = []string{
	"minimal-scrape",
	"fanin-fanout",
	"nested-blocks",
	"bindings-secret",
	"logs-chain",
	"disabled-node",
	"label-edgecases",
	"otel-three-signals",
	"kitchen-sink",
}

var _ = Describe("Renderer", func() {
	schema := corpusSchema()

	// 7.2.1 — corpus renders byte-exact
	// RED-RUN PROOF: changing attribute emission order in render.go (e.g. emitting in props-map
	// order instead of schema order) causes all corpus entries with props to fail with
	// "Expected '...' to equal '...'" — the golden files become mismatched.
	DescribeTable("7.2.1 corpus renders byte-exact",
		func(name string) {
			graphJSON, golden := readCorpus(name)
			doc := unmarshalDoc(graphJSON)
			result := visual.Render(doc, schema)
			Expect(result.Diagnostics).To(BeEmpty(), "unexpected diagnostics for %s", name)
			Expect(result.Content).To(Equal(string(golden)), "output mismatch for %s", name)
		},
		Entry("minimal-scrape", "minimal-scrape"),
		Entry("fanin-fanout", "fanin-fanout"),
		Entry("nested-blocks", "nested-blocks"),
		Entry("bindings-secret", "bindings-secret"),
		Entry("logs-chain", "logs-chain"),
		Entry("disabled-node", "disabled-node"),
		Entry("label-edgecases", "label-edgecases"),
		Entry("otel-three-signals", "otel-three-signals"),
		Entry("kitchen-sink", "kitchen-sink"),
	)

	// 7.2.2 — render is permutation-invariant
	// RED-RUN PROOF: removing the topological sort tie-break (the category/component/label
	// secondary sort in the Kahn algorithm) causes kitchen-sink to produce different output
	// on different node-array orderings — the DescribeTable entry "kitchen-sink" fails.
	DescribeTable("7.2.2 render is permutation-invariant",
		func(name string) {
			graphJSON, golden := readCorpus(name)
			doc := unmarshalDoc(graphJSON)
			rng := rand.New(rand.NewSource(42))
			for i := range 5 {
				// Shuffle nodes and edges
				shuffled := doc
				nodes := make([]visual.GraphNode, len(doc.Nodes))
				copy(nodes, doc.Nodes)
				edges := make([]visual.GraphEdge, len(doc.Edges))
				copy(edges, doc.Edges)
				rng.Shuffle(len(nodes), func(a, b int) { nodes[a], nodes[b] = nodes[b], nodes[a] })
				rng.Shuffle(len(edges), func(a, b int) { edges[a], edges[b] = edges[b], edges[a] })
				shuffled.Nodes = nodes
				shuffled.Edges = edges
				result := visual.Render(shuffled, schema)
				Expect(result.Content).To(Equal(string(golden)),
					"permutation %d of %s produced different output", i, name)
			}
		},
		Entry("minimal-scrape", "minimal-scrape"),
		Entry("fanin-fanout", "fanin-fanout"),
		Entry("kitchen-sink", "kitchen-sink"),
	)

	// 7.2.3 — node_map is complete and correct
	It("7.2.3 node_map is complete and correct for all corpus entries", func() {
		for _, name := range corpusNames {
			graphJSON, _ := readCorpus(name)
			doc := unmarshalDoc(graphJSON)
			result := visual.Render(doc, schema)
			if len(result.Diagnostics) > 0 {
				continue // skip entries that intentionally produce diagnostics
			}

			lines := splitLines(result.Content)

			// Count expected non-disabled nodes
			activeCount := 0
			for _, n := range doc.Nodes {
				if !n.Disabled {
					activeCount++
				}
			}
			Expect(result.NodeMap).To(HaveLen(activeCount),
				"node_map should have exactly one entry per non-disabled node in %s", name)

			// Every entry has valid, non-overlapping ranges covering the component block
			seen := map[[2]int]string{}
			for id, r := range result.NodeMap {
				Expect(r.StartLine).To(BeNumerically("<=", r.EndLine),
					"node %s in %s: start > end", id, name)
				Expect(r.StartLine).To(BeNumerically(">=", 1),
					"node %s in %s: start < 1", id, name)
				Expect(r.EndLine).To(BeNumerically("<=", len(lines)),
					"node %s in %s: end > line count", id, name)

				key := [2]int{r.StartLine, r.EndLine}
				Expect(seen).NotTo(HaveKey(key),
					"node_map ranges overlap: %s and %s both map to lines %d-%d in %s",
					id, seen[key], r.StartLine, r.EndLine, name)
				seen[key] = id

				// The start line must contain the component name
				if r.StartLine <= len(lines) {
					Expect(lines[r.StartLine-1]).To(ContainSubstring(nodeComponent(doc, id)),
						"line %d of %s does not contain component name for node %s",
						r.StartLine, name, id)
				}
			}
		}
	})

	// 7.2.4 — fan-in preserves edge order
	It("7.2.4 fan-in preserves edge order", func() {
		graphJSON, _ := readCorpus("fanin-fanout")
		doc := unmarshalDoc(graphJSON)

		// Original order: first(0), second(1) → targets = [first.output, second.output]
		result := visual.Render(doc, schema)
		Expect(result.Content).To(ContainSubstring("discovery.relabel.first.output, discovery.relabel.second.output"))

		// Swap orders on the edges going into prometheus.scrape targets
		swapped := doc
		edges := make([]visual.GraphEdge, len(doc.Edges))
		copy(edges, doc.Edges)
		for i := range edges {
			if edges[i].To.Port == "targets" {
				if edges[i].Order != nil && *edges[i].Order == 0 {
					o := 1
					edges[i].Order = &o
				} else if edges[i].Order != nil && *edges[i].Order == 1 {
					o := 0
					edges[i].Order = &o
				}
			}
		}
		swapped.Edges = edges
		swappedResult := visual.Render(swapped, schema)
		Expect(swappedResult.Content).To(ContainSubstring("discovery.relabel.second.output, discovery.relabel.first.output"))
	})

	// 7.2.5 — sanitize collision is an error, never auto-suffixed
	// RED-RUN PROOF: adding auto-suffix logic (e.g. appending "_2" to the second node's label)
	// causes this test to fail — render succeeds instead of returning label_collision.
	It("7.2.5 sanitize collision is error, never auto-suffixed", func() {
		doc := visual.GraphDocument{
			SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{ID: "a", Component: "prometheus.remote_write", Label: "my-sink", Disabled: false},
				{ID: "b", Component: "prometheus.remote_write", Label: "my_sink", Disabled: false},
			},
		}
		result := visual.Render(doc, schema)
		Expect(result.Content).To(BeEmpty(), "content must be empty on collision")
		Expect(result.Diagnostics).To(HaveLen(1))
		Expect(result.Diagnostics[0].Code).To(Equal("label_collision"))
		// Both node IDs must be named
		Expect([]string{result.Diagnostics[0].NodeID, result.Diagnostics[0].NodeID2}).To(
			ConsistOf("a", "b"))
	})

	// 7.2.6 — disabled node emission
	It("7.2.6 disabled node is skipped in output but leaves a comment", func() {
		graphJSON, _ := readCorpus("disabled-node")
		doc := unmarshalDoc(graphJSON)
		result := visual.Render(doc, schema)
		Expect(result.Diagnostics).To(BeEmpty())
		// Disabled node appears as a comment
		Expect(result.Content).To(ContainSubstring(`// node "app" disabled`))
		// Disabled node block is NOT emitted
		Expect(result.Content).NotTo(ContainSubstring(`prometheus.scrape "app" {`))
		// Other nodes ARE emitted
		Expect(result.Content).To(ContainSubstring(`prometheus.remote_write "sink" {`))
		// render is total — succeeds even though remote_write has no incoming wires
		Expect(result.Content).NotTo(BeEmpty())
	})

	// 7.2.7 — secret-typed prop with a literal value is refused
	// RED-RUN PROOF: removing the secret-by-value check from render.go causes this test to
	// fail — render succeeds and emits the plaintext secret instead of an error.
	It("7.2.7 secret-typed literal prop returns secret_by_value error", func() {
		doc := visual.GraphDocument{
			SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{
					ID:        "s1",
					Component: "prometheus.scrape",
					Label:     "app",
					Disabled:  false,
					Props:     map[string]interface{}{"password": "plaintext-secret"},
				},
			},
		}
		result := visual.Render(doc, schema)
		Expect(result.Diagnostics).NotTo(BeEmpty(), "expected secret_by_value diagnostic")
		Expect(result.Diagnostics[0].Code).To(Equal("secret_by_value"))
		Expect(result.Diagnostics[0].NodeID).To(Equal("s1"))
	})

	It("7.2.9 unknown component produces diagnostic but still renders", func() {
		doc := visual.GraphDocument{SchemaVersion: "alloy-v1.18.1", Nodes: []visual.GraphNode{{ID: "u1", Component: "nonexistent.component", Label: "test"}}}
		result := visual.Render(doc, corpusSchema())
		Expect(result.Diagnostics).To(ContainElement(HaveField("Code", "unknown_component")))
		Expect(result.Content).NotTo(BeEmpty())
	})
})

// splitLines splits content into 1-based lines.
func splitLines(content string) []string {
	lines := []string{}
	cur := ""
	for _, ch := range content {
		if ch == '\n' {
			lines = append(lines, cur)
			cur = ""
		} else {
			cur += string(ch)
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// nodeComponent returns the component of a node by ID.
func nodeComponent(doc visual.GraphDocument, id string) string {
	for _, n := range doc.Nodes {
		if n.ID == id {
			return n.Component
		}
	}
	return ""
}
