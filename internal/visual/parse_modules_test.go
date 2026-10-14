package visual_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// expandModules is the graph view's parse: the builder's ParseAlloy callers
// never pass it (see ParseOptions).
var expandModules = visual.ParseOptions{ExpandModules: true}

func parseFixture(name string, opts visual.ParseOptions) visual.ParseResult {
	content, err := os.ReadFile("testdata/k8s-monitoring/" + name)
	Expect(err).NotTo(HaveOccurred())
	return visual.ParseAlloyWithOptions(string(content), "v1.20.1", opts, corpusSchema())
}

// childrenOf groups the parsed nodes by their parent group's label.
func childrenOf(result visual.ParseResult) map[string][]visual.GraphNode {
	byID := map[string]visual.GraphNode{}
	for _, n := range result.Doc.Nodes {
		byID[n.ID] = n
	}
	out := map[string][]visual.GraphNode{}
	for _, n := range result.Doc.Nodes {
		parent := result.Parents[n.ID]
		if parent == "" {
			continue
		}
		out[byID[parent].Label] = append(out[byID[parent].Label], n)
	}
	return out
}

// reaches reports whether the edges lead from node `from` to node `to`.
func reaches(doc visual.GraphDocument, from, to string) bool {
	next := map[string][]string{}
	for _, e := range doc.Edges {
		next[e.From.Node] = append(next[e.From.Node], e.To.Node)
	}
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == to {
			return true
		}
		for _, n := range next[cur] {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return false
}

var _ = Describe("ParseAlloy module expansion (graph view only)", func() {
	Describe("the k8s-monitoring metrics config", func() {
		var result visual.ParseResult
		byID := map[string]visual.GraphNode{}

		BeforeEach(func() {
			result = parseFixture("metrics.alloy", expandModules)
			byID = map[string]visual.GraphNode{}
			for _, n := range result.Doc.Nodes {
				byID[n.ID] = n
			}
		})

		It("draws every declare as a group of its components, never an empty opaque box", func() {
			var groups []string
			for _, n := range result.Doc.Nodes {
				Expect(n.Component).NotTo(Equal("opaque.declare"))
				if n.Component == "declare" {
					groups = append(groups, n.Label)
					Expect(result.Parents[n.ID]).To(BeEmpty())
				}
			}
			Expect(groups).To(ConsistOf(
				"annotation_autodiscovery", "cluster_metrics", "linux_host_metrics", "prometheus_operator_objects"))
			kids := childrenOf(result)
			Expect(kids["annotation_autodiscovery"]).To(HaveLen(8))
			Expect(kids["cluster_metrics"]).To(HaveLen(15))
			Expect(kids["linux_host_metrics"]).To(HaveLen(4))
			Expect(kids["prometheus_operator_objects"]).To(HaveLen(3))
			for group, children := range kids {
				for _, c := range children {
					Expect(c.Component).NotTo(HavePrefix("opaque."), "%s in %s", c.Component, group)
					Expect(c.ID).To(HavePrefix("n_"+group+"__"), "inner ids are prefixed with their module")
				}
			}
		})

		It("keeps every component of the config: 30 inside modules, 3 at the top level", func() {
			components, instances := 0, 0
			ids := map[string]bool{}
			for _, n := range result.Doc.Nodes {
				Expect(ids).NotTo(HaveKey(n.ID), "node ids are unique across modules")
				ids[n.ID] = true
				switch {
				case n.Component == "declare":
				case strings.HasPrefix(n.Component, "opaque."):
					instances++
				default:
					components++
				}
			}
			Expect(components).To(Equal(33))
			Expect(instances).To(Equal(4), "the four module instances stay opaque nodes")
		})

		It("wires every argument reference to its group's port, and each group port to its instance", func() {
			var inner, link int
			for _, e := range result.Doc.Edges {
				from, to := byID[e.From.Node], byID[e.To.Node]
				switch {
				case to.Component == "declare":
					Expect(result.Parents[from.ID]).To(Equal(to.ID), "an argument edge stays inside its module")
					Expect(e.From.Port).To(Equal("forward_to"))
					Expect(e.To.Port).To(Equal("metrics_destinations"))
					inner++
				case from.Component == "declare":
					Expect(to.Component).To(Equal("opaque." + from.Label))
					Expect(e.From.Port).To(Equal("metrics_destinations"))
					Expect(e.To.Port).To(Equal("metrics_destinations"))
					link++
				}
			}
			Expect(inner).To(Equal(10), "the config has ten `argument.metrics_destinations.value` references")
			Expect(link).To(Equal(4))
		})

		It("lets every scrape inside a module be traced to the remote_write", func() {
			var sink string
			for _, n := range result.Doc.Nodes {
				if n.Component == "prometheus.remote_write" {
					sink = n.ID
				}
			}
			Expect(sink).NotTo(BeEmpty())
			for _, n := range result.Doc.Nodes {
				if result.Parents[n.ID] != "" && strings.HasPrefix(n.Component, "prometheus.scrape") {
					Expect(reaches(result.Doc, n.ID, sink)).To(BeTrue(), "%s has no path to the destination", n.ID)
				}
			}
		})

		It("resolves references between components of the same module", func() {
			var relabel visual.GraphNode
			for _, n := range result.Doc.Nodes {
				if n.Component == "discovery.relabel" && n.Label == "annotation_autodiscovery_http" {
					relabel = n
				}
			}
			var from []string
			for _, e := range result.Doc.Edges {
				if e.To.Node == relabel.ID && e.To.Port == "targets" {
					from = append(from, byID[e.From.Node].Label)
					Expect(result.Parents[e.From.Node]).To(Equal(result.Parents[relabel.ID]))
				}
			}
			Expect(from).To(ConsistOf("annotation_autodiscovery_pods", "annotation_autodiscovery_services"))
		})

		It("still says the pipeline is not fully mappable, so the builder will not import it", func() {
			Expect(result.Opaque).To(BeTrue())
			Expect(result.Warning).To(ContainSubstring(`module "cluster_metrics" is shown as a group`))
		})

		// The mocked Playwright suite (visual-graph-view-modules.spec.ts)
		// replays this parse as its GraphView response. Regenerate it with
		// `make generate-corpus`.
		It("matches the web suite's metrics GraphView fixture", func() {
			expectFixture(result, "../../web/tests/fixtures/k8s-monitoring-metrics-graphview.json")
		})
	})

	It("follows arguments consumed inside an output block (the receiver config)", func() {
		result := parseFixture("receiver.alloy", expandModules)
		byID := map[string]visual.GraphNode{}
		for _, n := range result.Doc.Nodes {
			byID[n.ID] = n
		}
		var ports []string
		for _, e := range result.Doc.Edges {
			if byID[e.To.Node].Component == "declare" {
				ports = append(ports, e.From.Port+"->"+e.To.Port)
			}
		}
		Expect(ports).To(ConsistOf(
			"output.metrics->metrics_destinations", "output.logs->logs_destinations", "output.traces->traces_destinations"))
		var batch, attributes string
		for _, n := range result.Doc.Nodes {
			switch {
			case n.Component == "otelcol.processor.batch" && result.Parents[n.ID] != "":
				batch = n.ID
			case n.Component == "otelcol.processor.attributes":
				attributes = n.ID
			}
		}
		Expect(reaches(result.Doc, batch, attributes)).To(BeTrue())
	})

	It("expands the singleton config's module, keeps its bindings, and matches the web suite's GraphView fixture", func() {
		result := parseFixture("singleton.alloy", expandModules)
		kids := childrenOf(result)
		Expect(kids["cluster_events"]).To(HaveLen(2))
		Expect(result.Bindings).To(HaveLen(4))
		expectFixture(result, "../../web/tests/fixtures/k8s-monitoring-graphview.json")
	})

	It("leaves the builder's parse untouched: no group, no parent, the declare opaque", func() {
		result := parseFixture("metrics.alloy", visual.ParseOptions{})
		for _, n := range result.Doc.Nodes {
			Expect(n.Component).NotTo(Equal("declare"))
		}
		Expect(result.Parents).To(BeNil())
		Expect(result.Doc.Nodes).To(ContainElement(HaveField("Component", "opaque.declare")))
	})

	It("is the builder's parse exactly for a config without modules", func() {
		cases := map[string]string{}
		goldens, err := filepath.Glob("testdata/corpus/*.golden.alloy")
		Expect(err).NotTo(HaveOccurred())
		for _, path := range goldens {
			text, readErr := os.ReadFile(path)
			Expect(readErr).NotTo(HaveOccurred())
			cases[filepath.Base(path)] = string(text)
		}
		for name, doc := range roundTripShapes() {
			cases[name] = visual.Render(doc, corpusSchema()).Content
		}
		Expect(len(cases)).To(BeNumerically(">=", 10))
		for name, content := range cases {
			plain := visual.ParseAlloy(content, "v1.20.1", corpusSchema())
			expanded := visual.ParseAlloyWithOptions(content, "v1.20.1", expandModules, corpusSchema())
			Expect(expanded).To(Equal(plain), name)
		}
	})

	Describe("hand-written modules", func() {
		parse := func(src string) (visual.ParseResult, map[string]visual.GraphNode) {
			result := visual.ParseAlloyWithOptions(src, "v1.20.1", expandModules, corpusSchema())
			byKey := map[string]visual.GraphNode{}
			for _, n := range result.Doc.Nodes {
				byKey[n.Component+"/"+n.Label] = n
			}
			return result, byKey
		}

		It("wires an export out of its group and on to the instance's consumers", func() {
			result, byKey := parse(`declare "disc" {
  discovery.kubernetes "pods" {
    role = "pod"
  }
  export "targets" {
    value = discovery.kubernetes.pods.targets
  }
}

disc "main" { }

prometheus.scrape "app" {
  targets    = disc.main.targets
  forward_to = []
}
`)
			group, pods := byKey["declare/disc"], byKey["discovery.kubernetes/pods"]
			instance, scrape := byKey["opaque.disc/main"], byKey["prometheus.scrape/app"]
			Expect(result.Parents[pods.ID]).To(Equal(group.ID))
			type end struct{ from, fromPort, to, toPort string }
			var got []end
			for _, e := range result.Doc.Edges {
				got = append(got, end{e.From.Node, e.From.Port, e.To.Node, e.To.Port})
			}
			Expect(got).To(ConsistOf(
				end{pods.ID, "targets", group.ID, "targets"},
				end{group.ID, "targets", instance.ID, "targets"},
				end{instance.ID, "targets", scrape.ID, "targets"},
			))
			Expect(reaches(result.Doc, pods.ID, scrape.ID)).To(BeTrue())
		})

		It("feeds a data argument into the module from the instance", func() {
			result, byKey := parse(`discovery.kubernetes "pods" {
  role = "pod"
}

declare "scraper" {
  argument "targets" { }
  argument "destinations" { }

  prometheus.scrape "inner" {
    targets    = argument.targets.value
    forward_to = argument.destinations.value
  }
}

scraper "main" {
  targets      = discovery.kubernetes.pods.targets
  destinations = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://prom/api/v1/write"
  }
}
`)
			source, sink := byKey["discovery.kubernetes/pods"], byKey["prometheus.remote_write/sink"]
			Expect(reaches(result.Doc, source.ID, byKey["prometheus.scrape/inner"].ID)).To(BeTrue())
			Expect(reaches(result.Doc, byKey["prometheus.scrape/inner"].ID, sink.ID)).To(BeTrue())
		})

		It("keeps a non-port argument reference as a display-only binding on the group", func() {
			result, byKey := parse(`declare "logs" {
  argument "namespace" { }

  discovery.kubernetes "pods" {
    role = "pod"
    namespaces {
      names = [argument.namespace.value]
    }
  }
}
`)
			group := byKey["declare/logs"]
			Expect(result.Doc.Edges).To(BeEmpty())
			Expect(result.Bindings).To(HaveLen(1))
			Expect(result.Bindings[0].Ref.Node).To(Equal(group.ID))
			Expect(result.Bindings[0].Ref.Export).To(Equal("namespace"))
			Expect(result.Bindings[0].Prop).To(Equal("namespaces.names"))
		})

		It("recurses into a nested declare, prefixing ids with the whole module path", func() {
			result, byKey := parse(`declare "outer" {
  argument "out" { }

  declare "inner" {
    argument "out" { }
    prometheus.scrape "s" {
      targets    = []
      forward_to = argument.out.value
    }
  }

  inner "x" {
    out = argument.out.value
  }
}
`)
			outer, inner := byKey["declare/outer"], byKey["declare/inner"]
			scrape, instance := byKey["prometheus.scrape/s"], byKey["opaque.inner/x"]
			Expect(result.Parents[inner.ID]).To(Equal(outer.ID))
			Expect(result.Parents[scrape.ID]).To(Equal(inner.ID))
			Expect(result.Parents[instance.ID]).To(Equal(outer.ID))
			Expect(scrape.ID).To(Equal("n_outer__inner__prometheus_scrape_s"))
			Expect(reaches(result.Doc, scrape.ID, outer.ID)).To(BeTrue(),
				"the inner scrape reaches the outer module's argument port through the inner instance")
		})

		It("stops expanding past the depth bound and says so", func() {
			var b strings.Builder
			depth := visual.MaxModuleDepth + 2
			for i := range depth {
				fmt.Fprintf(&b, "declare \"d%d\" {\n", i)
			}
			b.WriteString("discovery.kubernetes \"deep\" {\n  role = \"pod\"\n}\n")
			for range depth {
				b.WriteString("}\n")
			}
			result, byKey := parse(b.String())
			var groups []string
			for _, n := range result.Doc.Nodes {
				if n.Component == "declare" {
					groups = append(groups, n.Label)
				}
			}
			sort.Strings(groups)
			Expect(groups).To(HaveLen(visual.MaxModuleDepth))
			Expect(byKey).NotTo(HaveKey("discovery.kubernetes/deep"))
			Expect(result.Warning).To(ContainSubstring("nested more than"))
		})
	})
})

// expectFixture compares a GraphView-shaped serialization of result with a
// web fixture by value, rewriting it first under GEN_GOLDENS.
func expectFixture(result visual.ParseResult, fixture string) {
	view := result.Doc
	view.Bindings = append(append([]visual.GraphBinding{}, view.Bindings...), result.Bindings...)
	// As GraphView sends it: each node inside a module carries its parent.
	raw, err := json.Marshal(view)
	Expect(err).NotTo(HaveOccurred())
	var graph map[string]any
	Expect(json.Unmarshal(raw, &graph)).To(Succeed())
	nodes, isList := graph["nodes"].([]any)
	Expect(isList).To(BeTrue())
	for _, n := range nodes {
		node, isMap := n.(map[string]any)
		Expect(isMap).To(BeTrue())
		id, isString := node["id"].(string)
		Expect(isString).To(BeTrue())
		if parent := result.Parents[id]; parent != "" {
			node["parent"] = parent
		}
	}
	got, err := json.MarshalIndent(map[string]any{
		"graph": graph, "opaque": result.Opaque, "warning": result.Warning,
	}, "", "  ")
	Expect(err).NotTo(HaveOccurred())
	if os.Getenv("GEN_GOLDENS") != "" {
		Expect(os.WriteFile(fixture, append(got, '\n'), 0o644)).To(Succeed())
	}
	stored, err := os.ReadFile(fixture)
	Expect(err).NotTo(HaveOccurred())
	var want, have any
	Expect(json.Unmarshal(stored, &want)).To(Succeed())
	Expect(json.Unmarshal(got, &have)).To(Succeed())
	Expect(have).To(Equal(want), "the web fixture is stale: regenerate it with `make generate-corpus`")
}
