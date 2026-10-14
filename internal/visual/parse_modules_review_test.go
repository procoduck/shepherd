package visual_test

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

// Review findings on #342: each spec is a probe that failed before its fix.
var _ = Describe("ParseAlloy module expansion: ids, edge ids, order and ports", func() {
	parse := func(src string) visual.ParseResult {
		return visual.ParseAlloyWithOptions(src, "v1.20.1", expandModules, corpusSchema())
	}
	byKey := func(r visual.ParseResult) map[string]visual.GraphNode {
		out := map[string]visual.GraphNode{}
		for _, n := range r.Doc.Nodes {
			out[n.Component+"/"+n.Label] = n
		}
		return out
	}
	labelOf := func(r visual.ParseResult, id string) string {
		for _, n := range r.Doc.Nodes {
			if n.ID == id {
				return n.Label
			}
		}
		return ""
	}
	expectUniqueIDs := func(r visual.ParseResult) {
		GinkgoHelper()
		nodes := map[string]bool{}
		for _, n := range r.Doc.Nodes {
			Expect(nodes).NotTo(HaveKey(n.ID), "duplicate node id")
			nodes[n.ID] = true
		}
		edges := map[string]bool{}
		for _, e := range r.Doc.Edges {
			Expect(edges).NotTo(HaveKey(e.ID), "duplicate edge id")
			edges[e.ID] = true
		}
	}

	It("keeps node ids unique when module paths sanitize to the same prefix", func() {
		scrape := "prometheus.scrape \"s\" {\n  targets = []\n  forward_to = []\n}\n"
		r := parse(`declare "a" {
  declare "b" {
    ` + scrape + `  }
}
declare "a__b" {
  ` + scrape + `}
declare "A__B" {
  ` + scrape + `}
`)
		expectUniqueIDs(r)
		var owners []string
		for _, n := range r.Doc.Nodes {
			if n.Component == "prometheus.scrape" {
				owners = append(owners, labelOf(r, r.Parents[n.ID]))
			}
		}
		Expect(owners).To(ConsistOf("b", "a__b", "A__B"), "each scrape's parent is its own module")
	})

	It("gives two argument edges into one prop distinct ids", func() {
		r := parse(`declare "m" {
  argument "a" { }
  argument "b" { }
  discovery.relabel "r" {
    targets = array.concat(argument.a.value, argument.b.value)
  }
}
`)
		Expect(r.Doc.Edges).To(HaveLen(2))
		expectUniqueIDs(r)
	})

	It("keeps every edge id unique on the k8s-monitoring fixtures", func() {
		for _, name := range []string{"metrics.alloy", "receiver.alloy", "singleton.alloy"} {
			expectUniqueIDs(parseFixture(name, expandModules))
		}
	})

	sibling := map[string]string{
		"b": `declare "b" {
  argument "dest" { }
  discovery.kubernetes "pods" {
    role = "pod"
  }
  prometheus.scrape "inner" {
    targets    = discovery.kubernetes.pods.targets
    forward_to = argument.dest.value
  }
  export "t" {
    value = discovery.kubernetes.pods.targets
  }
}
`,
		"a": `declare "a" {
  argument "dest" { }
  b "inner" {
    dest = argument.dest.value
  }
  prometheus.scrape "s" {
    targets    = b.inner.t
    forward_to = argument.dest.value
  }
}
`,
	}
	for _, order := range [][]string{{"a", "b"}, {"b", "a"}} {
		It(fmt.Sprintf("wires a sibling module's instance inside a module whatever the order (%s first)", order[0]), func() {
			r := parse(sibling[order[0]] + sibling[order[1]])
			nodes := byKey(r)
			pods, outer := nodes["discovery.kubernetes/pods"], nodes["prometheus.scrape/s"]
			Expect(reaches(r.Doc, pods.ID, outer.ID)).To(BeTrue(), "b's export reaches a's scrape")
			for _, b := range r.Bindings {
				Expect(b.Ref.Export).NotTo(Equal("t"), "the export reference is a wire, not a binding")
			}
			// b's scrape hands its data out through b's port, a's instance of b,
			// and a's own argument port.
			Expect(reaches(r.Doc, nodes["prometheus.scrape/inner"].ID, nodes["declare/a"].ID)).To(BeTrue())
		})
	}

	It("gives an argument and an export of the same name separate ports", func() {
		r := parse(`declare "m" {
  argument "x" { }
  discovery.kubernetes "p" {
    role = "pod"
  }
  prometheus.scrape "s" {
    targets    = discovery.kubernetes.p.targets
    forward_to = argument.x.value
  }
  export "x" {
    value = discovery.kubernetes.p.targets
  }
}
`)
		group := byKey(r)["declare/m"]
		ports := map[string]bool{}
		for _, e := range r.Doc.Edges {
			Expect(e.From.Node == group.ID && e.To.Node == group.ID).To(BeFalse(), "no edge from the group to itself")
			if e.From.Node == group.ID {
				ports[e.From.Port] = true
			}
			if e.To.Node == group.ID {
				ports[e.To.Port] = true
			}
		}
		Expect(ports).To(Equal(map[string]bool{"argument.x": true, "export.x": true}))
	})

	It("draws an export that passes an argument through as group in → group out", func() {
		r := parse(`declare "m" {
  argument "in" { }
  discovery.relabel "r" {
    targets = argument.in.value
  }
  export "out" {
    value = argument.in.value
  }
}
`)
		group := byKey(r)["declare/m"]
		var through []string
		for _, e := range r.Doc.Edges {
			if e.From.Node == group.ID && e.To.Node == group.ID {
				through = append(through, e.From.Port+"->"+e.To.Port)
			}
		}
		Expect(through).To(Equal([]string{"argument.in->export.out"}))
		for _, b := range r.Bindings {
			Expect(strings.HasPrefix(b.Prop, "export.")).To(BeFalse())
		}
	})
})

// The coverage the CHANGELOG quotes, from the four committed configs: Alloy
// wires between components (instance → destination included), argument
// references wired to a module's port, and the synthetic group ↔ instance
// links, counted separately.
var _ = It("covers every component of grafana/k8s-monitoring 4.4.0's four configs", func() {
	type tally struct{ components, wires, viaPort, links int }
	count := func(opts visual.ParseOptions) tally {
		var t tally
		for _, name := range []string{"metrics.alloy", "logs.alloy", "singleton.alloy", "receiver.alloy"} {
			r := parseFixture(name, opts)
			kind := map[string]string{}
			for _, n := range r.Doc.Nodes {
				kind[n.ID] = n.Component
				if n.Component != "declare" && !strings.HasPrefix(n.Component, "opaque.") {
					t.components++
				}
			}
			for _, e := range r.Doc.Edges {
				from, to := kind[e.From.Node], kind[e.To.Node]
				switch {
				case from != "declare" && to != "declare":
					t.wires++
				case strings.HasPrefix(from, "opaque.") || strings.HasPrefix(to, "opaque."):
					t.links++
				default:
					t.viaPort++
				}
			}
		}
		return t
	}
	Expect(count(visual.ParseOptions{})).To(Equal(tally{components: 23, wires: 30}))
	Expect(count(expandModules)).To(Equal(tally{components: 69, wires: 74, viaPort: 16, links: 10}))
})
