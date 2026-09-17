package visual_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

var _ = Describe("ExperimentalNodes", func() {
	payload := visual.SchemaPayload{
		Components: map[string]visual.ComponentSchema{
			"otelcol.connector.spanmetrics": {Stability: "experimental"},
			"prometheus.scrape":             {Stability: "ga"},
			"loki.write":                    {}, // empty stability == not experimental
		},
	}

	It("returns the enabled experimental nodes in document order", func() {
		doc := visual.GraphDocument{Nodes: []visual.GraphNode{
			{ID: "a", Component: "prometheus.scrape", Label: "scrape"},
			{ID: "b", Component: "otelcol.connector.spanmetrics", Label: "spanmetrics"},
			{ID: "c", Component: "loki.write", Label: "logs"},
		}}

		got := visual.ExperimentalNodes(doc, payload)

		Expect(got).To(Equal([]visual.ExperimentalNode{
			{NodeID: "b", Component: "otelcol.connector.spanmetrics", Label: "spanmetrics"},
		}))
	})

	It("skips a disabled experimental node (it does not render)", func() {
		doc := visual.GraphDocument{Nodes: []visual.GraphNode{
			{ID: "b", Component: "otelcol.connector.spanmetrics", Label: "spanmetrics", Disabled: true},
		}}
		Expect(visual.ExperimentalNodes(doc, payload)).To(BeEmpty())
	})

	It("treats an empty or unknown stability as non-experimental", func() {
		doc := visual.GraphDocument{Nodes: []visual.GraphNode{
			{ID: "c", Component: "loki.write"},
			{ID: "d", Component: "component.not.in.schema"},
		}}
		Expect(visual.ExperimentalNodes(doc, payload)).To(BeEmpty())
	})

	It("is empty for a graph with no nodes", func() {
		Expect(visual.ExperimentalNodes(visual.GraphDocument{}, payload)).To(BeEmpty())
	})
})
