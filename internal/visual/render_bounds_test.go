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

	// Review of #393: the diagnostics were as unbounded as the text was. Each
	// edge_unresolved message embedded both components, so one node with a
	// 500 KB unknown component and 25,618 distinct short edges (~1 MiB)
	// produced 12.8 GB of messages.
	It("refuses the diagnostic bomb fast (an over-long component)", func() {
		doc := visual.GraphDocument{Nodes: []visual.GraphNode{
			boundsNode("a", strings.Repeat("c", 500_000), "a"),
			boundsNode("b", "prometheus.scrape", "b"),
		}}
		for i := range 25_618 {
			doc.Edges = append(doc.Edges, boundsEdge(fmt.Sprint(i), "a", fmt.Sprintf("p%d", i), "b", "targets"))
		}
		var r visual.RenderResult
		elapsed, alloc := measure(func() { r = visual.Render(doc, schema) })
		Expect(hasCode(r, "component_too_long")).To(BeTrue(), "%d diagnostics", len(r.Diagnostics))
		Expect(r.Content).To(BeEmpty())
		Expect(elapsed).To(BeNumerically("<", time.Second))
		Expect(alloc).To(BeNumerically("<", 16<<20), "allocated %d bytes", alloc)
	})

	It("caps the diagnostics a legal graph produces, by count and bytes, keeping them blocking", func() {
		long := strings.Repeat("c", visual.MaxComponentBytes)
		doc := visual.GraphDocument{Nodes: []visual.GraphNode{
			boundsNode("a", long, "a"),
			boundsNode("b", "prometheus.scrape", "b"),
		}}
		for i := range 25_000 {
			doc.Edges = append(doc.Edges, boundsEdge(strings.Repeat("e", 200)+fmt.Sprint(i), "a", fmt.Sprintf("p%d", i), "b", "targets"))
		}
		var r visual.RenderResult
		elapsed, alloc := measure(func() { r = visual.Render(doc, schema) })
		Expect(elapsed).To(BeNumerically("<", time.Second))
		Expect(alloc).To(BeNumerically("<", 32<<20), "allocated %d bytes", alloc)
		Expect(len(r.Diagnostics)).To(BeNumerically("<=", visual.MaxDiagnostics+1))
		size := 0
		for _, d := range r.Diagnostics {
			size += len(d.Layer) + len(d.Severity) + len(d.Code) + len(d.NodeID) + len(d.NodeID2) + len(d.Message)
			Expect(len(d.Message)).To(BeNumerically("<", 512), "messages never embed a whole client string")
		}
		Expect(size).To(BeNumerically("<=", visual.MaxDiagnosticBytes+512))
		last := r.Diagnostics[len(r.Diagnostics)-1]
		Expect(last.Code).To(Equal("too_many_diagnostics"))
		Expect(last.Severity).To(Equal("error"), "a dropped error still blocks")
		Expect(last.Message).To(ContainSubstring("more"))
	})

	It("caps diagnostics merged after the render (CapDiagnostics), carrying the dropped count", func() {
		var in []visual.RenderDiagnostic
		for i := range 500 {
			in = append(in, visual.RenderDiagnostic{Layer: "L1", Severity: "warning", Code: "unknown_prop", Message: fmt.Sprint(i)})
		}
		capped := visual.CapDiagnostics(in)
		Expect(capped).To(HaveLen(visual.MaxDiagnostics + 1))
		Expect(capped[visual.MaxDiagnostics].Message).To(HavePrefix("300 more"))
		Expect(capped[visual.MaxDiagnostics].Severity).To(Equal("warning"))
		again := visual.CapDiagnostics(append(capped, visual.RenderDiagnostic{Severity: "error", Code: "experimental_gated"}))
		Expect(again).To(HaveLen(visual.MaxDiagnostics + 1))
		Expect(again[visual.MaxDiagnostics].Message).To(HavePrefix("301 more"))
		Expect(again[visual.MaxDiagnostics].Severity).To(Equal("error"))
		Expect(visual.CapDiagnostics(in[:3])).To(Equal(in[:3]))
	})

	It("refuses a component over MaxComponentBytes and accepts one at it", func() {
		at := strings.Repeat("c", visual.MaxComponentBytes)
		ok := visual.Render(visual.GraphDocument{Nodes: []visual.GraphNode{boundsNode("a", at, "a")}}, schema)
		Expect(hasCode(ok, "component_too_long")).To(BeFalse())
		Expect(hasCode(ok, "unknown_component")).To(BeTrue())
		bad := visual.Render(visual.GraphDocument{Nodes: []visual.GraphNode{boundsNode("a", at+"c", "a")}}, schema)
		Expect(hasCode(bad, "component_too_long")).To(BeTrue())
		Expect(bad.Content).To(BeEmpty())
	})

	// 90,000 × -1.79e308: 9 bytes of JSON each, 310 bytes of Alloy each. The
	// serialisers built the whole 28 MB value (197 MB allocated) before the
	// writer saw it.
	It("stops serialising a value once it passes the cap", func() {
		nums := make([]interface{}, 90_000)
		for i := range nums {
			nums[i] = -1.79e308
		}
		for _, n := range []visual.GraphNode{
			{ID: "u", Component: "not.in.schema", Label: "u", Props: map[string]interface{}{"v": nums}},
			{ID: "s", Component: "prometheus.scrape", Label: "s", Props: map[string]interface{}{"params": map[string]interface{}{"k": nums}}},
		} {
			doc := visual.GraphDocument{Nodes: []visual.GraphNode{n}}
			var r visual.RenderResult
			elapsed, alloc := measure(func() { r = visual.Render(doc, schema) })
			Expect(r.Err()).To(MatchError(visual.ErrOutputTooLarge), n.Component)
			Expect(elapsed).To(BeNumerically("<", time.Second))
			Expect(alloc).To(BeNumerically("<", 16<<20), "%s allocated %d bytes", n.Component, alloc)
		}
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

		It("serialises values exactly as the unbounded serializer did", func() {
			rng := rand.New(rand.NewSource(393))
			var gen func(depth int) interface{}
			gen = func(depth int) interface{} {
				switch k := rng.Intn(9); {
				case k == 0:
					return nil
				case k == 1:
					return []string{"", "a", "q\"\\\n\t\x01é", "x y"}[rng.Intn(4)]
				case k == 2:
					return rng.Intn(2) == 0
				case k == 3:
					return []float64{0, -1.79e308, 1e21, 0.1, -3, 5e-324}[rng.Intn(6)]
				case k <= 5 && depth < 4:
					l := make([]interface{}, rng.Intn(4))
					for i := range l {
						l[i] = gen(depth + 1)
					}
					return l
				case k <= 7 && depth < 4:
					m := map[string]interface{}{}
					for range rng.Intn(4) {
						m[[]string{"a", "b-c", "9", "", "ok_1"}[rng.Intn(5)]] = gen(depth + 1)
					}
					if rng.Intn(5) == 0 {
						return map[string]interface{}{"$expr": " env(\"X\") "}
					}
					return m
				default:
					return int64(rng.Intn(5)) // an unsupported shape: printed and quoted
				}
			}
			for range 20_000 {
				v := gen(0)
				Expect(visual.SerializeNatural(v)).To(Equal(visual.LegacySerializeNatural(v)), "%#v", v)
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

var _ = Describe("Bounded lists beside Render (#393 review)", func() {
	schema := corpusSchema()

	It("CheckPortShapes lists at most MaxDiagnostics problems, quoting references clipped", func() {
		label := strings.Repeat("r", 200)
		var b strings.Builder
		fmt.Fprintf(&b, "prometheus.remote_write %q {\n  endpoint {\n    url = \"http://x\"\n  }\n}\n", label)
		for i := range 300 {
			fmt.Fprintf(&b, "prometheus.scrape \"s%d\" {\n  targets = []\n  forward_to = prometheus.remote_write.%s.receiver\n}\n", i, label)
		}
		diags := visual.CheckPortShapes(b.String(), schema)
		Expect(diags).To(HaveLen(visual.MaxDiagnostics + 1))
		Expect(diags[0].Message).NotTo(ContainSubstring(label))
		Expect(diags[visual.MaxDiagnostics].Message).To(Equal("100 more port-shape problems are not listed"))
	})

	// The graph view's module expansion numbered each repeated edge by
	// scanning every edge for every candidate suffix: cubic in the repeats.
	It("ParseAlloy draws repeated module edges in linear time, ids unchanged", func() {
		refs := make([]string, 1500)
		for i := range refs {
			refs[i] = "argument.destinations.value"
		}
		content := "declare \"scraper\" {\n  argument \"destinations\" { }\n\n  prometheus.scrape \"inner\" {\n    targets    = []\n    forward_to = [" +
			strings.Join(refs, ", ") + "]\n  }\n}\n\nscraper \"main\" {\n  destinations = []\n}\n"
		var res visual.ParseResult
		elapsed, _ := measure(func() {
			res = visual.ParseAlloyWithOptions(content, "v1.20.1", visual.ParseOptions{ExpandModules: true}, schema)
		})
		Expect(elapsed).To(BeNumerically("<", time.Second))
		ids := map[string]bool{}
		for _, e := range res.Doc.Edges {
			Expect(ids[e.ID]).To(BeFalse(), "edge id %s is unique", e.ID)
			ids[e.ID] = true
		}
		Expect(len(ids)).To(BeNumerically(">=", 1500))
		Expect(ids).To(HaveKey(MatchRegexp(`_1500$`)), "the numbering is the same: base, base_2 … base_1500")
	})

	It("UpgradeCheck lists at most MaxDiagnostics items, labels clipped", func() {
		doc := visual.GraphDocument{}
		for i := range 300 {
			doc.Nodes = append(doc.Nodes, visual.GraphNode{ID: fmt.Sprintf("n%d", i), Component: "gone", Label: strings.Repeat("l", 5000)})
		}
		old := visual.UpgradeSchemaPayload{Components: map[string]visual.UpgradeComponentSchema{"gone": {}}}
		res := visual.UpgradeCheck(doc, old, visual.UpgradeSchemaPayload{}, "v1", "v2")
		Expect(res.Items).To(HaveLen(visual.MaxDiagnostics + 1))
		Expect(len(res.Items[0].NodeLabel)).To(BeNumerically("<=", visual.MaxLabelBytes))
		Expect(res.Items[visual.MaxDiagnostics].Detail).To(Equal("100 more changes are not listed"))
		Expect(res.NeedsUpgrade).To(BeTrue())
	})
})
