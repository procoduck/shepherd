package visual_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// intPtr is a tiny helper for GraphEdge.Order (a *int).
func intPtr(i int) *int { return &i }

var _ = Describe("DiffGraphs", func() {
	It("reports an added, a removed and an unchanged node", func() {
		from := visual.GraphDocument{Nodes: []visual.GraphNode{
			{ID: "a", Component: "prometheus.scrape", Label: "web"},
			{ID: "b", Component: "loki.write", Label: "local"},
		}}
		to := visual.GraphDocument{Nodes: []visual.GraphNode{
			{ID: "a", Component: "prometheus.scrape", Label: "web"},
			{ID: "c", Component: "loki.write", Label: "central"},
		}}

		diff := visual.DiffGraphs(from, to)

		Expect(diff.NodeChanges).To(HaveLen(2))
		// Sorted by component then label: "central" (added) before "local" (removed).
		Expect(diff.NodeChanges[0].ID).To(Equal("c"))
		Expect(diff.NodeChanges[0].Kind).To(Equal(visual.ChangeAdded))
		Expect(diff.NodeChanges[1].ID).To(Equal("b"))
		Expect(diff.NodeChanges[1].Kind).To(Equal(visual.ChangeRemoved))
		Expect(diff.EdgeChanges).To(BeEmpty())
		Expect(diff.BindingChanges).To(BeEmpty())
		Expect(diff.IsEmpty()).To(BeFalse())
	})

	It("reports a changed node with a per-field before/after", func() {
		from := visual.GraphDocument{Nodes: []visual.GraphNode{{
			ID: "a", Component: "prometheus.scrape", Label: "web", Disabled: false,
			Props: map[string]interface{}{"scrape_interval": "15s", "job_name": "web"},
		}}}
		to := visual.GraphDocument{Nodes: []visual.GraphNode{{
			ID: "a", Component: "prometheus.scrape", Label: "web-2", Disabled: true,
			Props: map[string]interface{}{"scrape_interval": "30s", "job_name": "web"},
		}}}

		diff := visual.DiffGraphs(from, to)

		Expect(diff.NodeChanges).To(HaveLen(1))
		nc := diff.NodeChanges[0]
		Expect(nc.Kind).To(Equal(visual.ChangeChanged))
		Expect(nc.ID).To(Equal("a"))
		// Sorted by field name: disabled, label, prop:scrape_interval.
		Expect(nc.FieldChanges).To(Equal([]visual.FieldChange{
			{Field: "disabled", OldValue: "false", NewValue: "true"},
			{Field: "label", OldValue: "web", NewValue: "web-2"},
			{Field: "prop:scrape_interval", OldValue: "15s", NewValue: "30s"},
		}))
	})

	It("reports added and removed props on a changed node", func() {
		from := visual.GraphDocument{Nodes: []visual.GraphNode{{
			ID: "a", Component: "x", Props: map[string]interface{}{"gone": "1"},
		}}}
		to := visual.GraphDocument{Nodes: []visual.GraphNode{{
			ID: "a", Component: "x", Props: map[string]interface{}{"fresh": "2"},
		}}}

		diff := visual.DiffGraphs(from, to)

		Expect(diff.NodeChanges).To(HaveLen(1))
		Expect(diff.NodeChanges[0].FieldChanges).To(Equal([]visual.FieldChange{
			{Field: "prop:fresh", OldValue: "", NewValue: "2"},
			{Field: "prop:gone", OldValue: "1", NewValue: ""},
		}))
	})

	It("ignores position-only moves (pure layout is not a change)", func() {
		from := visual.GraphDocument{Nodes: []visual.GraphNode{{
			ID: "a", Component: "x", Position: visual.Position{X: 0, Y: 0},
		}}}
		to := visual.GraphDocument{Nodes: []visual.GraphNode{{
			ID: "a", Component: "x", Position: visual.Position{X: 400, Y: 250},
		}}}

		Expect(visual.DiffGraphs(from, to).IsEmpty()).To(BeTrue())
	})

	It("treats a non-string prop by canonical value, not Go type", func() {
		// JSON numbers decode to float64; an int and a float64 of the same value
		// must not read as a change.
		from := visual.GraphDocument{Nodes: []visual.GraphNode{{
			ID: "a", Component: "x", Props: map[string]interface{}{"n": float64(5), "on": true},
		}}}
		to := visual.GraphDocument{Nodes: []visual.GraphNode{{
			ID: "a", Component: "x", Props: map[string]interface{}{"n": 5, "on": true},
		}}}

		Expect(visual.DiffGraphs(from, to).IsEmpty()).To(BeTrue())
	})

	It("diffs edges by id, including an endpoint change", func() {
		from := visual.GraphDocument{Edges: []visual.GraphEdge{
			{ID: "e1", From: visual.PortRef{Node: "a", Port: "out"}, To: visual.PortRef{Node: "b", Port: "in"}},
			{ID: "e2", From: visual.PortRef{Node: "a", Port: "out"}, To: visual.PortRef{Node: "z", Port: "in"}},
		}}
		to := visual.GraphDocument{Edges: []visual.GraphEdge{
			// e1 re-pointed to c, e2 removed, e3 added.
			{ID: "e1", From: visual.PortRef{Node: "a", Port: "out"}, To: visual.PortRef{Node: "c", Port: "in"}},
			{ID: "e3", From: visual.PortRef{Node: "c", Port: "out"}, To: visual.PortRef{Node: "d", Port: "in"}},
		}}

		diff := visual.DiffGraphs(from, to)

		Expect(diff.EdgeChanges).To(HaveLen(3))
		Expect(diff.EdgeChanges[0].ID).To(Equal("e1"))
		Expect(diff.EdgeChanges[0].Kind).To(Equal(visual.ChangeChanged))
		Expect(diff.EdgeChanges[0].To.Node).To(Equal("c"))
		Expect(diff.EdgeChanges[1].ID).To(Equal("e2"))
		Expect(diff.EdgeChanges[1].Kind).To(Equal(visual.ChangeRemoved))
		Expect(diff.EdgeChanges[2].ID).To(Equal("e3"))
		Expect(diff.EdgeChanges[2].Kind).To(Equal(visual.ChangeAdded))
	})

	It("treats an edge order change as a change", func() {
		from := visual.GraphDocument{Edges: []visual.GraphEdge{
			{ID: "e1", From: visual.PortRef{Node: "a"}, To: visual.PortRef{Node: "b"}, Order: intPtr(1)},
		}}
		to := visual.GraphDocument{Edges: []visual.GraphEdge{
			{ID: "e1", From: visual.PortRef{Node: "a"}, To: visual.PortRef{Node: "b"}, Order: intPtr(2)},
		}}

		diff := visual.DiffGraphs(from, to)
		Expect(diff.EdgeChanges).To(HaveLen(1))
		Expect(diff.EdgeChanges[0].Kind).To(Equal(visual.ChangeChanged))
	})

	It("diffs bindings by (node, prop)", func() {
		from := visual.GraphDocument{Bindings: []visual.GraphBinding{
			{Node: "a", Prop: "forward_to", Ref: visual.BindingRef{Node: "b", Export: "receiver"}},
			{Node: "a", Prop: "targets", Ref: visual.BindingRef{Node: "x", Export: "targets"}},
		}}
		to := visual.GraphDocument{Bindings: []visual.GraphBinding{
			// forward_to re-pointed, targets removed, extra added.
			{Node: "a", Prop: "forward_to", Ref: visual.BindingRef{Node: "c", Export: "receiver"}},
			{Node: "a", Prop: "extra", Ref: visual.BindingRef{Node: "y", Export: "out"}},
		}}

		diff := visual.DiffGraphs(from, to)

		Expect(diff.BindingChanges).To(HaveLen(3))
		// Sorted by node then prop: extra(added), forward_to(changed), targets(removed).
		Expect(diff.BindingChanges[0].Prop).To(Equal("extra"))
		Expect(diff.BindingChanges[0].Kind).To(Equal(visual.ChangeAdded))
		Expect(diff.BindingChanges[1].Prop).To(Equal("forward_to"))
		Expect(diff.BindingChanges[1].Kind).To(Equal(visual.ChangeChanged))
		Expect(diff.BindingChanges[1].OldRef.Node).To(Equal("b"))
		Expect(diff.BindingChanges[1].NewRef.Node).To(Equal("c"))
		Expect(diff.BindingChanges[2].Prop).To(Equal("targets"))
		Expect(diff.BindingChanges[2].Kind).To(Equal(visual.ChangeRemoved))
	})

	It("is empty for two identical graphs", func() {
		g := visual.GraphDocument{
			Nodes:    []visual.GraphNode{{ID: "a", Component: "x", Label: "n", Props: map[string]interface{}{"k": "v"}}},
			Edges:    []visual.GraphEdge{{ID: "e", From: visual.PortRef{Node: "a"}, To: visual.PortRef{Node: "a"}}},
			Bindings: []visual.GraphBinding{{Node: "a", Prop: "p", Ref: visual.BindingRef{Node: "a"}}},
		}
		Expect(visual.DiffGraphs(g, g).IsEmpty()).To(BeTrue())
	})

	// #203: a revision's saved graph and a re-render of the same pipeline name
	// their nodes differently (n1 vs n_discovery_kubernetes_pods). The nodes are
	// the same Alloy blocks, so the diff must match them by component + label and
	// map wires/bindings through that identity rather than the raw ids.
	Context("matching by stable identity (#203)", func() {
		// The demo-visual shape: discovery -> scrape -> remote_write, with the
		// builder's n1/n2/n3 ids on one side and a re-parse's ids on the other.
		demoGraph := func(ids [3]string, edgeIDs [2]string) visual.GraphDocument {
			return visual.GraphDocument{
				Nodes: []visual.GraphNode{
					{ID: ids[0], Component: "discovery.kubernetes", Label: "pods", Props: map[string]interface{}{"role": "pod"}},
					{ID: ids[1], Component: "prometheus.scrape", Label: "pods", Props: map[string]interface{}{"scrape_interval": "30s"}},
					{ID: ids[2], Component: "prometheus.remote_write", Label: "default"},
				},
				Edges: []visual.GraphEdge{
					{ID: edgeIDs[0], From: visual.PortRef{Node: ids[0], Port: "targets"}, To: visual.PortRef{Node: ids[1], Port: "targets"}},
					{ID: edgeIDs[1], From: visual.PortRef{Node: ids[1], Port: "forward_to"}, To: visual.PortRef{Node: ids[2], Port: "receiver"}},
				},
				Bindings: []visual.GraphBinding{
					{Node: ids[1], Prop: "targets", Ref: visual.BindingRef{Node: ids[0], Export: "targets"}},
				},
			}
		}
		builderIDs := [3]string{"n1", "n2", "n3"}
		parsedIDs := [3]string{"n_discovery_kubernetes_pods", "n_prometheus_scrape_pods", "n_prometheus_remote_write_default"}

		It("reports no change for the same graph under renamed ids", func() {
			from := demoGraph(builderIDs, [2]string{"e1", "e2"})
			to := demoGraph(parsedIDs, [2]string{"e_a", "e_b"})

			diff := visual.DiffGraphs(from, to)
			Expect(diff.NodeChanges).To(BeEmpty())
			Expect(diff.EdgeChanges).To(BeEmpty())
			Expect(diff.BindingChanges).To(BeEmpty())
			Expect(diff.IsEmpty()).To(BeTrue())
		})

		It("reports only a genuinely added node, and its wire by human name", func() {
			from := demoGraph(builderIDs, [2]string{"e1", "e2"})
			to := demoGraph(parsedIDs, [2]string{"e_a", "e_b"})
			to.Nodes = append(to.Nodes, visual.GraphNode{ID: "n_loki_write_central", Component: "loki.write", Label: "central"})
			to.Edges = append(to.Edges, visual.GraphEdge{
				ID:   "e_c",
				From: visual.PortRef{Node: "n_prometheus_scrape_pods", Port: "forward_to"},
				To:   visual.PortRef{Node: "n_loki_write_central", Port: "receiver"},
			})

			diff := visual.DiffGraphs(from, to)
			Expect(diff.NodeChanges).To(HaveLen(1))
			Expect(diff.NodeChanges[0].Kind).To(Equal(visual.ChangeAdded))
			Expect(diff.NodeChanges[0].Component).To(Equal("loki.write"))
			Expect(diff.NodeChanges[0].Label).To(Equal("central"))
			Expect(diff.EdgeChanges).To(HaveLen(1))
			ec := diff.EdgeChanges[0]
			Expect(ec.Kind).To(Equal(visual.ChangeAdded))
			Expect(ec.From).To(Equal(visual.PortRef{Node: `prometheus.scrape "pods"`, Port: "forward_to"}))
			Expect(ec.To).To(Equal(visual.PortRef{Node: `loki.write "central"`, Port: "receiver"}))
			Expect(diff.BindingChanges).To(BeEmpty())
		})

		It("reports a changed attribute on an identity-matched node as changed", func() {
			from := demoGraph(builderIDs, [2]string{"e1", "e2"})
			to := demoGraph(parsedIDs, [2]string{"e_a", "e_b"})
			to.Nodes[1].Props = map[string]interface{}{"scrape_interval": "60s"}

			diff := visual.DiffGraphs(from, to)
			Expect(diff.NodeChanges).To(HaveLen(1))
			nc := diff.NodeChanges[0]
			Expect(nc.Kind).To(Equal(visual.ChangeChanged))
			Expect(nc.Component).To(Equal("prometheus.scrape"))
			Expect(nc.FieldChanges).To(Equal([]visual.FieldChange{
				{Field: "prop:scrape_interval", OldValue: "30s", NewValue: "60s"},
			}))
			Expect(diff.EdgeChanges).To(BeEmpty())
			Expect(diff.BindingChanges).To(BeEmpty())
		})

		It("names binding nodes and refs by component and label", func() {
			from := demoGraph(builderIDs, [2]string{"e1", "e2"})
			to := demoGraph(parsedIDs, [2]string{"e_a", "e_b"})
			to.Bindings[0].Ref.Export = "output"

			diff := visual.DiffGraphs(from, to)
			Expect(diff.BindingChanges).To(HaveLen(1))
			bc := diff.BindingChanges[0]
			Expect(bc.Kind).To(Equal(visual.ChangeChanged))
			Expect(bc.Node).To(Equal(`prometheus.scrape "pods"`))
			Expect(bc.OldRef).To(Equal(visual.BindingRef{Node: `discovery.kubernetes "pods"`, Export: "targets"}))
			Expect(bc.NewRef).To(Equal(visual.BindingRef{Node: `discovery.kubernetes "pods"`, Export: "output"}))
		})

		It("treats a block as an object and as a one-element list alike", func() {
			// A re-parse yields the object form plus a block_order; the builder
			// saves a list of one and no stated order. Both render the same Alloy.
			from := visual.GraphDocument{Nodes: []visual.GraphNode{{
				ID: "n_rw", Component: "prometheus.remote_write", Label: "demo", BlockOrder: []string{"endpoint"},
				Props: map[string]interface{}{"endpoint": map[string]interface{}{"url": "https://p/api/v1/write"}},
			}}}
			to := visual.GraphDocument{Nodes: []visual.GraphNode{{
				ID: "n3", Component: "prometheus.remote_write", Label: "demo",
				Props: map[string]interface{}{"endpoint": []interface{}{map[string]interface{}{"url": "https://p/api/v1/write"}}},
			}}}
			Expect(visual.DiffGraphs(from, to).IsEmpty()).To(BeTrue())

			// A real change inside the block still shows.
			to.Nodes[0].Props = map[string]interface{}{"endpoint": []interface{}{map[string]interface{}{"url": "https://q/api/v1/write"}}}
			diff := visual.DiffGraphs(from, to)
			Expect(diff.NodeChanges).To(HaveLen(1))
			Expect(diff.NodeChanges[0].FieldChanges).To(HaveLen(1))
			Expect(diff.NodeChanges[0].FieldChanges[0].Field).To(Equal("prop:endpoint"))
		})

		It("falls back to the id when component + label is ambiguous", func() {
			// Two unlabelled blocks of the same component: identity can't tell
			// them apart, so their ids pair them.
			from := visual.GraphDocument{Nodes: []visual.GraphNode{
				{ID: "a", Component: "x", Props: map[string]interface{}{"v": "1"}},
				{ID: "b", Component: "x", Props: map[string]interface{}{"v": "2"}},
			}}
			to := visual.GraphDocument{Nodes: []visual.GraphNode{
				{ID: "a", Component: "x", Props: map[string]interface{}{"v": "1"}},
				{ID: "b", Component: "x", Props: map[string]interface{}{"v": "3"}},
			}}

			diff := visual.DiffGraphs(from, to)
			Expect(diff.NodeChanges).To(HaveLen(1))
			Expect(diff.NodeChanges[0].ID).To(Equal("b"))
			Expect(diff.NodeChanges[0].Kind).To(Equal(visual.ChangeChanged))
		})
	})
})
