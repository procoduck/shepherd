package visual_test

import (
	"encoding/json"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// The builder's own round trip through the read-only parse: a visual
// pipeline whose wizard_state is missing or unreadable is reloaded from
// VisualService.GraphView (VisualBuilderPage's fallback), and "Recreate as
// visual pipeline" copies the same response into a new draft. Both strip the
// bindings GraphView merges in (web/src/visual/graphViewFlow.ts,
// builderDraftFromGraphView) — they are a display-only projection of
// expressions the props already hold. Rendering that draft must give back the
// text it was parsed from, byte for byte.

// roundTripShapes are builder-authored props that hold references inside a
// larger expression — the shapes that must stay an expression (no wire, no
// opaque flag) so they neither conflict with a wire nor lose their literal.
func roundTripShapes() map[string]visual.GraphDocument {
	node := func(id, component, label string, props map[string]interface{}) visual.GraphNode {
		return visual.GraphNode{ID: id, Component: component, Label: label, Props: props}
	}
	doc := func(nodes ...visual.GraphNode) visual.GraphDocument {
		return visual.GraphDocument{Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.20.1", Nodes: nodes}
	}
	return map[string]visual.GraphDocument{
		// A port reference concatenated with a literal target list.
		"buried-port-ref": doc(
			node("n1", "discovery.kubernetes", "pods", map[string]interface{}{"role": "pod"}),
			node("n2", "discovery.relabel", "keep", map[string]interface{}{
				"targets": map[string]interface{}{
					"$expr": `array.concat(discovery.kubernetes.pods.targets, [{"__address__" = "localhost:9090"}])`,
				},
			}),
		),
		// A list mixing a receiver with a non-port export.
		"mixed-list": doc(
			node("n1", "local.file", "x", map[string]interface{}{"filename": "/etc/x"}),
			node("n2", "prometheus.scrape", "app", map[string]interface{}{
				"targets": []interface{}{},
				"forward_to": map[string]interface{}{
					"$expr": `[prometheus.remote_write.sink.receiver, local.file.x.content]`,
				},
			}),
			node("n3", "prometheus.remote_write", "sink", map[string]interface{}{
				"endpoint": []interface{}{map[string]interface{}{"url": "https://prom/api/v1/write"}},
			}),
		),
	}
}

// roundTripCorpus is every corpus entry whose text re-parses to the same
// graph. Excluded, with the reason: disabled-node and kitchen-sink each hold a
// disabled node, which is rendered as a comment only, so its block is not in
// the text to parse back (and the header's node count drops by one).
var roundTripCorpus = []string{
	"minimal-scrape", "fanin-fanout", "nested-blocks", "bindings-secret",
	"logs-chain", "label-edgecases", "otel-three-signals", "diagnostics-mixed",
}

type roundTripCase struct {
	Name   string               `json:"name"`
	Text   string               `json:"text"`
	Graph  visual.GraphDocument `json:"graph"`
	Opaque bool                 `json:"opaque"`
}

// graphViewOf is what VisualService.GraphView returns for text: the parsed
// document with the parser's reference projection merged into its bindings.
func graphViewOf(text string) (visual.GraphDocument, visual.ParseResult) {
	parsed := visual.ParseAlloy(text, "alloy-v1.20.1", corpusSchema())
	view := parsed.Doc
	view.Bindings = append(append([]visual.GraphBinding{}, view.Bindings...), parsed.Bindings...)
	// As the wire carries them: an empty repeated field is [], never null.
	view.Edges = append([]visual.GraphEdge{}, view.Edges...)
	return view, parsed
}

var _ = Describe("builder text through the graph-view parse", func() {
	schema := corpusSchema()

	roundTrip := func(text string) {
		GinkgoHelper()
		view, parsed := graphViewOf(text)
		Expect(parsed.Opaque).To(BeFalse(), parsed.Warning)
		draft := view
		draft.Bindings = nil // builderDraftFromGraphView
		rendered := visual.Render(draft, schema)
		for _, d := range rendered.Diagnostics {
			Expect(d.Code).NotTo(Equal("prop_wire_conflict"), d.Message)
		}
		Expect(rendered.Content).To(Equal(text))
	}

	for _, name := range roundTripCorpus {
		It("round-trips the "+name+" corpus golden", func() {
			graph, golden := readCorpus(name)
			Expect(string(golden)).To(Equal(visual.Render(unmarshalDoc(graph), schema).Content))
			roundTrip(string(golden))
		})
	}

	for name, doc := range roundTripShapes() {
		It("keeps the "+name+" shape an expression: no wire, not opaque, literal intact", func() {
			text := visual.Render(doc, schema)
			Expect(text.Diagnostics).To(BeEmpty())
			view, _ := graphViewOf(text.Content)
			Expect(view.Edges).To(BeEmpty(), "a reference inside a larger expression is not a wire")
			Expect(view.Bindings).NotTo(BeEmpty(), "the view still lists the references")
			roundTrip(text.Content)
		})
	}

	// web/src/visual/graphViewRoundTrip.test.ts renders each case's graph
	// through builderDraftFromGraphView and renderTS and expects the same text,
	// which makes this the Go/TS parity proof for these shapes. Regenerate
	// with GEN_GOLDENS=1 (`make generate-corpus` does).
	It("matches the web round-trip fixture", func() {
		cases := []roundTripCase{}
		for _, name := range roundTripCorpus {
			_, golden := readCorpus(name)
			view, parsed := graphViewOf(string(golden))
			cases = append(cases, roundTripCase{name, string(golden), view, parsed.Opaque})
		}
		shapes := roundTripShapes()
		for _, name := range []string{"buried-port-ref", "mixed-list"} {
			text := visual.Render(shapes[name], schema).Content
			view, parsed := graphViewOf(text)
			cases = append(cases, roundTripCase{name, text, view, parsed.Opaque})
		}
		got, err := json.MarshalIndent(cases, "", "  ")
		Expect(err).NotTo(HaveOccurred())
		const fixture = "../../web/tests/fixtures/graphview-roundtrip.json"
		if os.Getenv("GEN_GOLDENS") != "" {
			Expect(os.WriteFile(fixture, append(got, '\n'), 0o644)).To(Succeed())
		}
		raw, err := os.ReadFile(fixture)
		Expect(err).NotTo(HaveOccurred())
		var want, have any
		Expect(json.Unmarshal(raw, &want)).To(Succeed())
		Expect(json.Unmarshal(got, &have)).To(Succeed())
		Expect(have).To(Equal(want), "the web fixture is stale: run make generate-corpus")
	})
})
