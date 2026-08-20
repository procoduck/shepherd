package simulate_test

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/token"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/netshape"
	"shepherd/internal/simsvc"
	"shepherd/internal/simulate"
	"shepherd/internal/visual"
)

// What the transform does to authored ADDRESSES — which is a narrower claim
// than §6.4 once made, and deliberately so. §6.4 said "a malicious graph cannot
// reach anything except the capture harness" and attributed that to the
// transform. It is the network that delivers it (e2e/sandbox_egress_test.go);
// what the specs below measure is that the transform does not hand an authored
// endpoint to the sandbox, which is a credential-and-fidelity property.
//
// CRITICAL-2 measured how false the original claim was. A prometheus.scrape node with a
// literal __address__ naming an arbitrary host survived Transform untouched,
// rendered with zero diagnostics, passed real `alloy validate` with exit 0 and
// was ALLOWED by the simulator's endpoint allowlist. Docker's internal network
// was the only thing left, and nothing tested its effect.
//
// HIGH-3 measured the second half: rewriteDestinations only rewrote the paths
// the overlay's endpoint_paths named, so every ADDRESS-BEARING SIBLING on the
// same mapped destination survived — otelcol.exporter.otlphttp's per-signal
// traces_endpoint/logs_endpoint/metrics_endpoint (which override the endpoint
// that WAS rewritten), proxy_url on five different exporters, oauth2.token_url
// on three. The finding's own note was that no transform test covered any of
// them: `grep -c "proxy_url|logs_endpoint|token_url" internal/simulate/*_test.go`
// returned 0.
//
// The sweep below is generated from the artifact rather than from the finding's
// list, because the finding's list is not the property. "The three siblings
// somebody enumerated do not survive" is a much weaker claim than "no attribute
// the artifact declares with an address-shaped name survives", and only the
// second one keeps holding after an Alloy bump.
//
// WHAT THESE SPECS DO NOT CLAIM. They say nothing about what the sandbox can
// REACH. The transform bounds which authored VALUES leave the graph; the
// network the sandbox runs on is what denies it a route (e2e/sandbox_egress_test.go).
// The sweep's own residue makes the distinction unavoidable: five
// relabel-destination paths keep their authored value by design, and one of the
// values a user may write there is the literal "__address__", which retargets
// the scrape at runtime. See "Transform: a runtime retarget is NOT contained by
// the transform" at the bottom of this file.

// addressLeafRe has the shape of internal/schema's own addressNameRe, which
// guards the keep lists at build time. It is deliberately restated rather than
// exported and shared: this sweep wants to be WIDER than the guard if the two
// ever drift, and a sweep that imported the guard's own regex could only ever
// probe what the guard already covers.
var addressLeafRe = regexp.MustCompile(`(?i)(url|uri|endpoint|address|addr|host|server|broker|proxy|dsn|target|port|domain|site|region|zone)`)

// outsideHost is the host every probe in this file plants. It is under
// example.net, so a probe that somehow escaped into a real dial would go
// nowhere; and it is one string, so a leak anywhere in the sweep names the same
// value the assertion looks for.
const outsideHost = "internal-vault.example.net"

// relabelDestinationPaths are the ONLY address-named attribute paths in the
// shipped artifact whose authored value survives the transform, enumerated
// rather than tolerated by a wildcard so a sixth (or, since the availability
// fix, an eleventh) one cannot appear silently. Two different reasons put a
// path on this list, and they are not the same claim:
//
//   - Five are a relabel rule's `target_label`: the name of the label the rule
//     WRITES. The overlay allowlists them because relabel is the single
//     mechanism S3 exists to let people simulate, and a relabel rule with no
//     destination is not a relabel rule. What the overlay's own
//     acknowledgement gets wrong — "a relabel destination LABEL name, never a
//     network address" — is that __address__ is itself a label name. A user
//     who writes it here steers the scrape, and the value that lands there is
//     computed at runtime from `replacement` and the source labels, so no
//     gate over the rendered text can bound it. The sweep records the
//     residue; the network denies the reach.
//   - Five more are the availability fix (VB-1 §6.4): a required attribute
//     that is address-SHAPED by name but not address-shaped by SEMANTICS —
//     loki.enrich/pyroscope.enrich's target_match_label is a label name to
//     match against, not a network target, and otelcol.receiver.cloudflare's
//     endpoint / fluentforward's endpoint / tcplog's listen_address are the
//     local bind address the sandbox itself listens on for a push-style
//     receiver, never a destination it dials. Each is required, so dropping
//     it would leave a config the sandbox cannot load — the same "missing
//     required attribute" failure this fix closes everywhere else, just with
//     the opposite remedy: keep the value instead of refusing the component.
//     Their sim_keep_acknowledged entries carry the same reasoning.
var relabelDestinationPaths = []string{
	"discovery.relabel rule.*.target_label",
	"loki.enrich target_match_label",
	"loki.relabel rule.*.target_label",
	"otelcol.receiver.cloudflare endpoint",
	"otelcol.receiver.fluentforward endpoint",
	"otelcol.receiver.tcplog listen_address",
	"prometheus.relabel rule.*.target_label",
	"prometheus.remote_write endpoint.*.write_relabel_config.*.target_label",
	"pyroscope.enrich target_match_label",
	"pyroscope.relabel rule.*.target_label",
}

var _ = Describe("Transform: no authored address reaches the sandbox", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	// The sweep. Every attribute path in the shipped artifact whose leaf name
	// is address-shaped gets the outside host planted at it; the transformed
	// render must carry it at the enumerated relabel-destination paths and
	// nowhere else. A refused graph ships nothing, so the assertion is on the
	// render, not on the error.
	It("plants an outside host at every address-named attribute path in the artifact and finds it only at the relabel destinations", func() {
		var probed, rendered int
		var leaked []string

		for i, p := range canaryPaths(payload) {
			if !addressLeafRe.MatchString(p.canon[len(p.canon)-1]) {
				continue
			}
			probed++
			class, _ := policy.Components[p.component].Keep.ClassOf(p.canon)
			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: p.component, Label: fmt.Sprintf("probe%d", i),
					Props: canaryProps(p.canon, canaryValue(p.typ, class, outsideHost)),
				}},
			}
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			if err != nil {
				continue
			}
			rendered++
			if strings.Contains(visual.Render(result.Graph, payload).Content, outsideHost) {
				leaked = append(leaked, p.String())
			}
		}

		// ConsistOf, not BeEmpty and not a subset: a sixth surviving path is a
		// real change to what the transform lets through and has to be looked
		// at, and a path that STOPS surviving means the overlay quietly lost a
		// relabel destination and every relabel simulation with it.
		Expect(leaked).To(ConsistOf(relabelDestinationPaths),
			"the set of address-named paths that keep their authored value changed; "+
				"if that is intended, update relabelDestinationPaths and docs/proofs/simulator-containment.md together")
		// Stated so the sweep cannot pass by probing nothing: a filter bug or a
		// schema payload that failed to load would otherwise look like success.
		Expect(probed).To(Equal(679), "address-named attribute paths probed")
		// And most of them must actually REACH a render. A sweep whose probes
		// all failed closed for some unrelated reason would report the same
		// leak list while testing nothing about rule K.
		// 539, down from 575: the round-2 HIGH accounted for four (blackbox
		// and snmp becoming sim_unsupported — their probe destination sits in
		// an ordinary `address` key inside the target list, which forcing
		// __address__ never touched). The rest is the availability fix (VB-1
		// §6.4): 19 more components moved to sim_unsupported because an
		// unconditionally required attribute or block had no keep-list entry,
		// so every address-named probe on any of those 19 now fails the run
		// closed instead of rendering, the same as blackbox and snmp already
		// did.
		Expect(rendered).To(Equal(539), "probes that produced a sandbox config rather than failing the run closed")
	})

	// HIGH-3's own list, named one per entry. The sweep above already covers
	// every one of them, but "N paths leaked" is not a useful regression
	// message when the path that came back is the one a finding was written
	// about.
	DescribeTable("refuses the specific sibling endpoints that survived alongside a rewritten destination",
		func(component string, path ...string) {
			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: component, Label: "exfil",
					Props: canaryProps(path, "https://"+outsideHost+"/x"),
				}},
			}
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred())

			content := visual.Render(result.Graph, payload).Content
			Expect(content).NotTo(ContainSubstring(outsideHost), "rendered:\n%s", content)
			// The endpoint that IS mapped still points at the harness, so the
			// entry proves the sibling went and not that the whole component
			// was dropped.
			Expect(content).To(ContainSubstring("simulator.example.com"), "rendered:\n%s", content)
		},
		Entry("otelcol.exporter.otlphttp traces_endpoint", "otelcol.exporter.otlphttp", "traces_endpoint"),
		Entry("otelcol.exporter.otlphttp logs_endpoint", "otelcol.exporter.otlphttp", "logs_endpoint"),
		Entry("otelcol.exporter.otlphttp metrics_endpoint", "otelcol.exporter.otlphttp", "metrics_endpoint"),
		Entry("otelcol.exporter.otlphttp client.proxy_url", "otelcol.exporter.otlphttp", "client", "proxy_url"),
		Entry("otelcol.exporter.faro client.proxy_url", "otelcol.exporter.faro", "client", "proxy_url"),
		Entry("loki.write endpoint.proxy_url", "loki.write", "endpoint", "*", "proxy_url"),
		Entry("loki.write endpoint.oauth2.proxy_url", "loki.write", "endpoint", "*", "oauth2", "proxy_url"),
		Entry("prometheus.remote_write endpoint.proxy_url", "prometheus.remote_write", "endpoint", "*", "proxy_url"),
		Entry("pyroscope.write endpoint.proxy_url", "pyroscope.write", "endpoint", "*", "proxy_url"),
		Entry("prometheus.write.queue endpoint.proxy_url", "prometheus.write.queue", "endpoint", "*", "proxy_url"),
	)

	// oauth2.token_url is HIGH-3's third shape and gets its own entry because
	// its declared type is `string` inside a NON-repeatable block, a path shape
	// the endpoint_paths rewrite never visits at all.
	DescribeTable("refuses an oauth2 token endpoint, which is a credential exchange as well as an address",
		func(component string, path ...string) {
			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: component, Label: "exfil",
					Props: canaryProps(path, "https://"+outsideHost+"/token"),
				}},
			}
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(visual.Render(result.Graph, payload).Content).NotTo(ContainSubstring(outsideHost))
		},
		Entry("loki.write", "loki.write", "endpoint", "*", "oauth2", "token_url"),
		Entry("prometheus.remote_write", "prometheus.remote_write", "endpoint", "*", "oauth2", "token_url"),
		Entry("pyroscope.write", "pyroscope.write", "endpoint", "*", "oauth2", "token_url"),
	)
})

var _ = Describe("Transform: a literal scrape target is re-pointed at the harness", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	// CRITICAL-2's own probe graph, verbatim from the finding.
	scrapeGraph := func(labels map[string]interface{}) visual.GraphDocument {
		return visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{ID: "s", Component: "prometheus.scrape", Label: "ssrf", Props: map[string]interface{}{
					"targets": []interface{}{labels},
				}},
				{ID: "w", Component: "prometheus.remote_write", Label: "rw"},
			},
			Edges: []visual.GraphEdge{{
				ID: "e1", From: visual.PortRef{Node: "s", Port: "forward_to"},
				To: visual.PortRef{Node: "w", Port: "receiver"},
			}},
		}
	}

	It("forces __address__ and __scheme__, so the finding's own graph reaches the harness and nothing else", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: scrapeGraph(map[string]interface{}{
				"__address__": outsideHost, "__scheme__": "https", "job": "x",
			}),
			Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring(outsideHost), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`__address__ = "simulator.example.com:9111"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`__scheme__ = "http"`), "rendered:\n%s", content)
		// The user's own label survives: the class re-points the target, it
		// does not delete the pipeline.
		Expect(content).To(ContainSubstring(`job = "x"`), "rendered:\n%s", content)

		// §6.5 requires the user be told, and told specifically enough to
		// explain why the sandbox scraped something they did not name.
		var forced []simulate.Rewrite
		for _, r := range result.Rewrites {
			if r.Kind == simulate.RewriteTargetAddressForced {
				forced = append(forced, r)
			}
		}
		Expect(forced).To(HaveLen(1))
		Expect(forced[0].From).To(Equal(outsideHost))
		Expect(forced[0].To).To(Equal("simulator.example.com:9111"))
		Expect(forced[0].Path).To(Equal([]string{"targets", "0", "__address__"}))
	})

	It("takes the forced address from the harness it was given, so no address is baked into the transform", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph:  scrapeGraph(map[string]interface{}{"__address__": outsideHost}),
			Schema: payload, Policy: policy, Harness: altHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(visual.Render(result.Graph, payload).Content).
			To(ContainSubstring(`__address__ = "other.example.org:8111"`))
	})

	It("forces the address onto a target set that never named one", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph:  scrapeGraph(map[string]interface{}{"job": "x"}),
			Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(visual.Render(result.Graph, payload).Content).
			To(ContainSubstring(`__address__ = "simulator.example.com:9111"`))
	})

	It("drops the meta labels that steer a request without being the address", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: scrapeGraph(map[string]interface{}{
				"__address__":      outsideHost,
				"__metrics_path__": "/exfil",
				"__param_token":    "CANARY-TARGET-PARAM",
				"job":              "x",
			}),
			Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring("/exfil"), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring("CANARY-TARGET-PARAM"), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring("__param_token"), "rendered:\n%s", content)
	})

	It("refuses an expression at the target set, so sys.env cannot supply the address", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: scrapeGraph(map[string]interface{}{
				"__address__": map[string]interface{}{"$expr": `sys.env("EXFIL_TARGET")`},
			}),
			Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(visual.Render(result.Graph, payload).Content).NotTo(ContainSubstring("sys.env"))
	})

	// The indirect vector: discovery.relabel is NOT a Sources-category node, so
	// rule G never stubs a user-authored one. Its output feeds a scrape through
	// a wire, and the canonical relabel idiom copies a discovered label into
	// __address__ — so its literal targets steer a request just as directly as
	// prometheus.scrape's do.
	It("forces the address on a user-authored discovery node, not only on the scrape", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{{
				ID: "d", Component: "discovery.relabel", Label: "sd",
				Props: map[string]interface{}{
					"targets": []interface{}{map[string]interface{}{"__address__": outsideHost}},
				},
			}},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring(outsideHost), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`__address__ = "simulator.example.com:9111"`), "rendered:\n%s", content)
	})

	It("fails the run closed when the harness has no target address to force", func() {
		harness := testHarness()
		harness.TargetAddress = ""
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph:  scrapeGraph(map[string]interface{}{"__address__": outsideHost}),
			Schema: payload, Policy: policy, Harness: harness,
		})
		Expect(errorCodes(transformErrors(err))).To(ContainElement(simulate.CodeHarnessIncomplete))
	})
})

// The honest statement of where the reachability boundary actually is.
//
// This Describe asserts a NEGATIVE about the transform, deliberately, because
// the alternative is a comfortable lie. A discovery.relabel rule computes
// __address__ at RUNTIME from label data — `target_label = "__address__"` with
// a `replacement` assembled from regex captures — so the address the sandbox
// dials need never appear as a token in the rendered config. No analysis of
// that text can bound it. `rule.*.target_label` and `rule.*.replacement` are on
// discovery.relabel's sim_keep for a real reason (relabel is the single thing
// users most want to simulate) and the overlay's own acknowledgement for
// target_label — "a relabel destination LABEL name, never a network address" —
// is simply wrong: __address__ IS a label name, and writing it is how
// Prometheus retargets a scrape.
//
// The control that answers this graph is the sandbox's network: Docker's
// `internal: true` sim network gives it no route off itself, proven by
// execution in e2e/sandbox_egress_test.go (P-deny-ip dials by literal IP from
// the sandbox's own namespace and goes red the moment the network is made
// reachable), and the retarget graph below is submitted to a live sandbox in
// that same file.
//
// This spec fails if someone re-adds a static gate that refuses the graph. That
// is the point: a repo whose tests say the transform contains reachability, and
// whose architecture says the network does, will drift until nobody knows which
// control is load-bearing. Whoever makes this red must update
// docs/proofs/simulator-containment.md and VB-1 §6.4 in the same change.
var _ = Describe("Transform: a runtime retarget is NOT contained by the transform", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	// retargetGraph is the round-2 attack panel's own graph. Every literal in it
	// is inert on its face: "10", "0", "9" and "1" are label values, "${1}:9999"
	// carries no host-shaped token, and the assembled address exists only once
	// Alloy has run the rule.
	retargetGraph := func() visual.GraphDocument {
		return visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{ID: "d", Component: "discovery.relabel", Label: "retarget", Props: map[string]interface{}{
					"targets": []interface{}{map[string]interface{}{
						"__address__": "127.0.0.1:1",
						"o1":          "10", "o2": "0", "o3": "9", "o4": "1",
					}},
					"rule": []interface{}{map[string]interface{}{
						"source_labels": []interface{}{"o1", "o2", "o3", "o4"},
						"separator":     ".",
						"regex":         "(.*)",
						"target_label":  "__address__",
						"replacement":   "${1}:9999",
						"action":        "replace",
					}},
				}},
				{ID: "s", Component: "prometheus.scrape", Label: "scrape"},
				{ID: "w", Component: "prometheus.remote_write", Label: "rw"},
			},
			Edges: []visual.GraphEdge{
				{ID: "e1", From: visual.PortRef{Node: "d", Port: "output"}, To: visual.PortRef{Node: "s", Port: "targets"}},
				{ID: "e2", From: visual.PortRef{Node: "s", Port: "forward_to"}, To: visual.PortRef{Node: "w", Port: "receiver"}},
			},
		}
	}

	It("accepts the graph and leaves the retarget rule intact in the rendered config", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: retargetGraph(), Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred(),
			"the transform must not pretend to contain a runtime retarget; if this now refuses the graph, "+
				"the architecture changed and docs/proofs/simulator-containment.md must say so")

		content := visual.Render(result.Graph, payload).Content

		// The transform DID force the literal seed address at config time — the
		// target_set class still earns its place — so the assertion below is
		// about the rule, not about a literal the class missed.
		Expect(content).To(ContainSubstring(`__address__ = "simulator.example.com:9111"`), "rendered:\n%s", content)

		// And the rule that overwrites it at runtime survives verbatim. These
		// two assertions together ARE the finding.
		Expect(content).To(ContainSubstring(`target_label = "__address__"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`replacement = "${1}:9999"`), "rendered:\n%s", content)
	})

	It("carries no host-shaped literal at all, which is why no text gate could have caught it", func() {
		// The claim that makes the deletion of rule P5 correct rather than
		// merely convenient: P5 refused host-SHAPED literals, and this graph has
		// none. It would have sailed through P5 exactly as it sails through the
		// transform now, while P5 was busy refusing five of six ordinary graphs.
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: retargetGraph(), Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		var hostShaped []string
		for _, literal := range renderedStringLiterals(content) {
			for _, host := range netshape.Hosts(literal) {
				if host == "simulator.example.com" {
					continue // the harness's own address, written by the transform
				}
				hostShaped = append(hostShaped, host)
			}
		}
		Expect(hostShaped).To(BeEmpty(),
			"a literal in this render names a host, so the attack graph is no longer the un-catchable one it was written to be:\n%s", content)
	})

	// simsvc.CheckEndpoints is declared defence-in-depth on the same config, and
	// it is honest about the same limit: it too sees no host here.
	It("passes the simulator's own endpoint allowlist, which is a second text gate with the same blind spot", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: retargetGraph(), Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(simsvc.CheckEndpoints(visual.Render(result.Graph, payload).Content,
			[]string{"simulator.example.com", "localhost", "127.0.0.1"})).To(Succeed())
	})
})

// renderedStringLiterals returns every string literal in a rendered Alloy
// config, parsed rather than regexed so the set is exactly what a text gate
// would see.
func renderedStringLiterals(content string) []string {
	file, err := parser.ParseFile("rendered.alloy", []byte(content))
	Expect(err).NotTo(HaveOccurred(), "rendered:\n%s", content)

	var out []string
	ast.Walk(literalCollector(func(value string) { out = append(out, value) }), file)
	return out
}

// literalCollector visits every STRING literal in a parsed config.
type literalCollector func(string)

func (c literalCollector) Visit(node ast.Node) ast.Visitor {
	lit, ok := node.(*ast.LiteralExpr)
	if !ok || lit.Kind != token.STRING {
		return c
	}
	if value, err := strconv.Unquote(lit.Value); err == nil {
		c(value)
	}
	return c
}
