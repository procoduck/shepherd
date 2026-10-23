package visual_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/visual"
)

// #391: Render's output was bounded by nothing. Every edge writes the target
// node's whole label, duplicate edges were never merged, and the topological
// sort re-sorted its queue after every push while every node scanned every
// binding. A request under the 1 MiB graph cap (capGraph) rendered gigabytes.
// These specs are the issue's reproducers; each was red before the fix (the
// edge bomb rendered 6.0 GB, the fan-out took 22.8 s of CPU).

// measure runs fn once and reports its wall time and the bytes the process
// allocated meanwhile. Specs run serially, so TotalAlloc's delta is fn's.
func measure(fn func()) (time.Duration, uint64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	fn()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	return elapsed, after.TotalAlloc - before.TotalAlloc
}

func boundsNode(id, component, label string) visual.GraphNode {
	return visual.GraphNode{ID: id, Component: component, Label: label}
}

func boundsEdge(id, from, fromPort, to, toPort string) visual.GraphEdge {
	return visual.GraphEdge{ID: id, From: visual.PortRef{Node: from, Port: fromPort}, To: visual.PortRef{Node: to, Port: toPort}}
}

func hasCode(r visual.RenderResult, code string) bool {
	for _, d := range r.Diagnostics {
		if d.Code == code && d.Severity == "error" {
			return true
		}
	}
	return false
}

// edgeBomb is the issue's first probe: two nodes, a 400 KB label on the
// target, and 15,000 identical forward_to → receiver edges — a 910 KB request.
func edgeBomb() visual.GraphDocument {
	doc := visual.GraphDocument{Kind: "alloy-graph/v1", Nodes: []visual.GraphNode{
		boundsNode("s", "prometheus.scrape", "s"),
		boundsNode("r", "prometheus.remote_write", strings.Repeat("a", 400_000)),
	}}
	for i := range 15_000 {
		doc.Edges = append(doc.Edges, boundsEdge(fmt.Sprintf("%d", i), "s", "forward_to", "r", "receiver"))
	}
	return doc
}

// amplifier fans every one of k scrapes into every one of m remote_writes:
// k*m distinct, legal edges, each writing a label at the cap. Nothing in it is
// a duplicate or over a per-field limit, yet its text is several MiB.
func amplifier(k, m int) visual.GraphDocument {
	doc := visual.GraphDocument{Kind: "alloy-graph/v1"}
	for j := range m {
		doc.Nodes = append(doc.Nodes, boundsNode(fmt.Sprintf("r%d", j), "prometheus.remote_write",
			fmt.Sprintf("r%d_%s", j, strings.Repeat("x", 240))))
	}
	for i := range k {
		doc.Nodes = append(doc.Nodes, boundsNode(fmt.Sprintf("s%d", i), "prometheus.scrape", fmt.Sprintf("s%d", i)))
		for j := range m {
			doc.Edges = append(doc.Edges, boundsEdge(fmt.Sprintf("%d.%d", i, j), fmt.Sprintf("s%d", i), "forward_to", fmt.Sprintf("r%d", j), "receiver"))
		}
	}
	return doc
}

// fanOut is the issue's second probe: one discovery root feeding n scrapes,
// one binding on each — every child becomes ready during the same pop.
func fanOut(n int) visual.GraphDocument {
	doc := visual.GraphDocument{Kind: "alloy-graph/v1", Nodes: []visual.GraphNode{boundsNode("root", "discovery.kubernetes", "root")}}
	for i := range n {
		id := fmt.Sprintf("c%d", i)
		doc.Nodes = append(doc.Nodes, boundsNode(id, "prometheus.scrape", id))
		doc.Edges = append(doc.Edges, boundsEdge("e"+id, "root", "targets", id, "targets"))
		doc.Bindings = append(doc.Bindings, visual.GraphBinding{Node: id, Prop: "job_name", Ref: visual.BindingRef{Expr: `"j"`}})
	}
	return doc
}

var _ = Describe("Render bounds (#391)", func() {
	schema := corpusSchema()

	It("caps output at the pipeline contents limit", func() {
		Expect(visual.MaxRenderBytes).To(Equal(config.MaxPipelineContentsBytes))
	})

	It("refuses the edge bomb fast instead of rendering gigabytes", func() {
		// 910 KB as protobuf, the size capGraph measures (its JSON is larger).
		doc := edgeBomb()
		var r visual.RenderResult
		elapsed, alloc := measure(func() { r = visual.Render(doc, schema) })
		Expect(hasCode(r, "label_too_long")).To(BeTrue(), "%v", r.Diagnostics)
		Expect(r.Content).To(BeEmpty())
		Expect(elapsed).To(BeNumerically("<", 2*time.Second))
		Expect(alloc).To(BeNumerically("<", 16<<20), "allocated %d bytes", alloc)
	})

	It("refuses a legal graph whose text would pass the cap, while writing", func() {
		doc := amplifier(100, 100)
		raw, err := json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(raw)).To(BeNumerically("<", config.MaxPipelineContentsBytes), "the probe must fit under capGraph")
		Expect(len(visual.LegacyRender(doc, schema).Content)).To(BeNumerically(">", 2*visual.MaxRenderBytes),
			"unbounded, this graph renders well past the cap")

		var r visual.RenderResult
		elapsed, alloc := measure(func() { r = visual.Render(doc, schema) })
		Expect(hasCode(r, "output_too_large")).To(BeTrue(), "%v", r.Diagnostics)
		Expect(errors.Is(r.Err(), visual.ErrOutputTooLarge)).To(BeTrue())
		Expect(r.Content).To(BeEmpty())
		Expect(r.NodeMap).To(BeEmpty())
		Expect(elapsed).To(BeNumerically("<", 2*time.Second))
		// Refused while writing: what it holds is bounded by the cap and the
		// input (the edge index), never by the text the graph expands to —
		// unbounded, this render allocated 28 MB.
		Expect(alloc).To(BeNumerically("<", 4*visual.MaxRenderBytes), "allocated %d bytes", alloc)
	})

	It("renders a graph just under the cap in full", func() {
		r := visual.Render(amplifier(30, 100), schema)
		Expect(r.Err()).NotTo(HaveOccurred())
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(len(r.Content)).To(BeNumerically("<=", visual.MaxRenderBytes))
		Expect(len(r.Content)).To(BeNumerically(">", visual.MaxRenderBytes/2))
	})

	It("refuses a label over MaxLabelBytes, disabled or not, and accepts one at it", func() {
		at := strings.Repeat("l", visual.MaxLabelBytes)
		ok := visual.Render(visual.GraphDocument{Nodes: []visual.GraphNode{boundsNode("a", "prometheus.scrape", at)}}, schema)
		Expect(hasCode(ok, "label_too_long")).To(BeFalse())
		Expect(ok.Content).To(ContainSubstring(at))

		for _, disabled := range []bool{false, true} {
			n := boundsNode("a", "prometheus.scrape", at+"l")
			n.Disabled = disabled
			r := visual.Render(visual.GraphDocument{Nodes: []visual.GraphNode{n}}, schema)
			Expect(hasCode(r, "label_too_long")).To(BeTrue(), "disabled=%v", disabled)
			Expect(r.Content).To(BeEmpty())
		}
	})

	It("refuses duplicate edges with one diagnostic per pair and writes the reference once", func() {
		doc := visual.GraphDocument{Nodes: []visual.GraphNode{
			boundsNode("s", "prometheus.scrape", "s"),
			boundsNode("r", "prometheus.remote_write", "r"),
		}}
		for i := range 50 {
			doc.Edges = append(doc.Edges, boundsEdge(fmt.Sprintf("e%d", i), "s", "forward_to", "r", "receiver"))
		}
		r := visual.Render(doc, schema)
		dups := 0
		for _, d := range r.Diagnostics {
			if d.Code == "duplicate_edge" {
				dups++
				Expect(d.Severity).To(Equal("error"))
			}
		}
		Expect(dups).To(Equal(1))
		Expect(strings.Count(r.Content, "prometheus.remote_write.r.receiver")).To(Equal(1))
	})

	It("refuses the 18,000-child fan-out fast", func() {
		// Its text is about 1.6 MiB, so it is refused; the sort runs in full
		// first, and the refusal comes at the cap rather than after the text.
		var r visual.RenderResult
		elapsed, _ := measure(func() { r = visual.Render(fanOut(18_000), schema) })
		Expect(r.Err()).To(MatchError(visual.ErrOutputTooLarge))
		Expect(elapsed).To(BeNumerically("<", 2*time.Second))
	})

	It("renders a 9,000-child fan-out without quadratic work", func() {
		var r visual.RenderResult
		elapsed, _ := measure(func() { r = visual.Render(fanOut(9_000), schema) })
		Expect(r.Diagnostics).To(BeEmpty())
		Expect(r.Content).To(ContainSubstring("prometheus.scrape \"c8999\""))
		Expect(elapsed).To(BeNumerically("<", 2*time.Second))
	})

	Describe("output is byte-identical to the pre-#391 renderer", func() {
		same := func(doc visual.GraphDocument) {
			GinkgoHelper()
			got, want := visual.Render(doc, schema), visual.LegacyRender(doc, schema)
			Expect(got.Content).To(Equal(want.Content))
			Expect(got.NodeMap).To(Equal(want.NodeMap))
			Expect(got.Diagnostics).To(Equal(want.Diagnostics))
		}

		It("on every corpus graph", func() {
			for _, name := range append(append([]string{}, corpusNames...), "diagnostics-mixed") {
				graphJSON, _ := readCorpus(name)
				same(unmarshalDoc(graphJSON))
			}
		})

		It("on the fan-out", func() {
			same(fanOut(500))
		})

		It("on 3,000 random small graphs", func() {
			rng := rand.New(rand.NewSource(391))
			codes := map[string]int{}
			wired, rendered := 0, 0
			for i := range 3000 {
				doc := randomGraph(rng, schema)
				By(fmt.Sprintf("graph %d", i), func() { same(doc) })
				r := visual.Render(doc, schema)
				for _, d := range r.Diagnostics {
					codes[d.Code]++
				}
				if r.Content != "" {
					rendered++
				}
				if strings.Contains(r.Content, ".receiver") || strings.Contains(r.Content, ".targets") || strings.Contains(r.Content, ".input") {
					wired++
				}
			}
			// The generator reaches every path whose order the fix touches.
			Expect(rendered).To(BeNumerically(">", 1000))
			Expect(wired).To(BeNumerically(">", 300))
			for _, code := range []string{"cycle", "label_collision", "edge_unresolved", "unknown_component", "empty_binding_expr", "empty_binding_prop"} {
				Expect(codes[code]).To(BeNumerically(">", 0), "no random graph produced %s", code)
			}
		})
	})
})

// randomGraph builds a small graph over real schema components: valid and
// invalid wires, cycles, disabled and unknown nodes, label collisions, block
// orders and bindings (empty ones included) — every distinct (from, port, to,
// port) at most once, since a duplicate is now refused.
func randomGraph(rng *rand.Rand, schema visual.SchemaPayload) visual.GraphDocument {
	comps := []string{
		"discovery.kubernetes", "discovery.relabel", "prometheus.scrape", "prometheus.relabel",
		"prometheus.remote_write", "loki.source.file", "loki.process", "loki.write",
		"otelcol.receiver.otlp", "otelcol.processor.batch", "otelcol.exporter.otlp", "not.a.component",
	}
	labels := []string{"a", "b", "c", "main", "Main", "9x", "x-y", "", "sink"}
	n := 1 + rng.Intn(9)
	doc := visual.GraphDocument{Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1"}
	for i := range n {
		node := visual.GraphNode{
			ID:        fmt.Sprintf("n%d", i),
			Component: comps[rng.Intn(len(comps))],
			Label:     labels[rng.Intn(len(labels))] + fmt.Sprint(rng.Intn(3)),
			Disabled:  rng.Intn(8) == 0,
		}
		if rng.Intn(4) == 0 {
			node.Props = map[string]interface{}{"job_name": "j", "extra": 1.0}
		}
		if rng.Intn(6) == 0 {
			node.BlockOrder = []string{"rule", "stage.json", "stage.drop"}
		}
		doc.Nodes = append(doc.Nodes, node)
	}
	ports := func(c string) []string {
		cs := schema.Components[c]
		out := []string{"bogus"}
		for _, p := range cs.Inputs {
			out = append(out, p.Prop)
		}
		for _, p := range cs.Outputs {
			out = append(out, p.Export)
		}
		sort.Strings(out)
		return out
	}
	seen := map[string]bool{}
	for e := range rng.Intn(3 * n) {
		from, to := doc.Nodes[rng.Intn(n)], doc.Nodes[rng.Intn(n)]
		fp, tp := ports(from.Component), ports(to.Component)
		edge := boundsEdge(fmt.Sprintf("e%d", e), from.ID, fp[rng.Intn(len(fp))], to.ID, tp[rng.Intn(len(tp))])
		if rng.Intn(3) == 0 {
			o := rng.Intn(3)
			edge.Order = &o
		}
		key := edge.From.Node + "\x00" + edge.From.Port + "\x00" + edge.To.Node + "\x00" + edge.To.Port
		if seen[key] {
			continue
		}
		seen[key] = true
		doc.Edges = append(doc.Edges, edge)
	}
	for b := range rng.Intn(n + 1) {
		expr := []string{`"v"`, "", "  ", "env(\"X\")"}[rng.Intn(4)]
		prop := []string{"job_name", "", "scrape_interval"}[rng.Intn(3)]
		doc.Bindings = append(doc.Bindings, visual.GraphBinding{
			Node: doc.Nodes[rng.Intn(n)].ID, Prop: prop, Ref: visual.BindingRef{Expr: expr + strings.Repeat(" ", b%2)},
		})
	}
	return doc
}

// BenchmarkRenderFanOut scales the #391 fan-out probe. Before the fix the work
// grew quadratically (the queue re-sort and the per-node binding scan); now
// each step is n log n.
func BenchmarkRenderFanOut(b *testing.B) {
	schema := corpusSchema()
	for _, n := range []int{1_000, 4_000, 16_000} {
		doc := fanOut(n)
		b.Run(fmt.Sprintf("children=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				visual.Render(doc, schema)
			}
		})
	}
}

// BenchmarkRenderAmplifier is the over-cap refusal: its cost is bounded by
// MaxRenderBytes, not by what the graph would expand to.
func BenchmarkRenderAmplifier(b *testing.B) {
	schema := corpusSchema()
	for _, k := range []int{100, 400} {
		doc := amplifier(k, 100)
		b.Run(fmt.Sprintf("edges=%d", k*100), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				visual.Render(doc, schema)
			}
		})
	}
}
