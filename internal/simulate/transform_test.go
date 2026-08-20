package simulate_test

import (
	"bytes"
	"log/slog"
	"math/rand"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/simulate"
	"shepherd/internal/visual"
)

const authoredHost = "authored.example.net"

var _ = Describe("Transform: rule D — destination endpoints", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	// §6.4 fail-closed: a Destinations component the harness cannot emulate must
	// stop the run, and one it can must be re-pointed. Driving the table off the
	// shipped overlay rather than a hand-written list means a new destination
	// arriving in a future Alloy bump fails HERE, at the transform, instead of
	// silently widening the hole.
	It("covers every Destinations component: mapped ones are re-pointed, unmapped ones fail closed", func() {
		var mapped, failed int
		for name, comp := range policy.Components {
			if comp.Category != "destinations" {
				continue
			}
			doc := singleNodeGraph(name)
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			if comp.Destination == nil {
				errs := transformErrors(err)
				Expect(errorCodes(errs)).To(ConsistOf(simulate.CodeCannotRewriteDestination),
					"unmapped destination %q must fail closed", name)
				failed++
				continue
			}
			Expect(err).NotTo(HaveOccurred(), "mapped destination %q must transform", name)
			Expect(result.Rewrites).NotTo(BeEmpty(), "destination %q must record what happened to it", name)

			content := visual.Render(result.Graph, payload).Content
			switch comp.Destination.Receiver {
			case simulate.ReceiverNone:
				Expect(result.Rewrites[0].Detail).To(Equal("local sink; nothing to rewrite"))
			case simulate.ReceiverOTLPGRPC:
				Expect(content).To(ContainSubstring(testHarness().OTLPGRPCAddress), "in %q", name)
			case simulate.ReceiverSyslog:
				Expect(content).To(ContainSubstring(testHarness().SyslogHost), "in %q", name)
			case simulate.ReceiverFile:
				Expect(content).To(ContainSubstring(testHarness().CaptureDir), "in %q", name)
			default:
				Expect(content).To(ContainSubstring(testHarness().CaptureBaseURL), "in %q", name)
			}
			mapped++
		}
		// 14, not 15, and 7, not 6: otelcol.exporter.splunkhec moved from
		// sim_destination to the deliberately-unmappable list (availability
		// fix, VB-1 §6.4) — its splunk.token is required AND secret, so a
		// mapped destination could never render it and would always be
		// missing a required attribute, the same reason datadog was already
		// unmappable.
		Expect(mapped).To(Equal(14), "the overlay must map exactly the 14 destinations §6.4 lists")
		Expect(failed).To(Equal(7), "the 7 deliberately-unmappable destinations must still fail closed")
	})

	// The renderer can emit an endpoint URL in four shapes (render.go: a typed
	// literal, the $expr escape, a GraphBinding, a capsule). Rule D has to catch
	// every shape it can appear in, so each is asserted against the same
	// property: the authored host is gone and the harness host is there.
	DescribeTable("rewrites the endpoint whatever shape the authored value takes",
		func(component string, props map[string]interface{}, bindings []visual.GraphBinding, expect func(simulate.HarnessEndpoints) string) {
			doc := singleNodeGraph(component)
			doc.Nodes[0].Props = props
			doc.Bindings = bindings

			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred())

			content := visual.Render(result.Graph, payload).Content
			Expect(content).NotTo(ContainSubstring(authoredHost),
				"the authored destination host must not survive into the sandbox:\n%s", content)
			Expect(content).To(ContainSubstring(expect(testHarness())), "rendered:\n%s", content)
		},
		Entry("literal, prometheus.remote_write", "prometheus.remote_write",
			map[string]interface{}{"endpoint": []interface{}{map[string]interface{}{"url": "https://" + authoredHost + "/api/v1/write"}}},
			nil,
			func(h simulate.HarnessEndpoints) string { return h.CaptureBaseURL + "/capture/prometheus/api/v1/write" }),
		Entry("$expr escape, loki.write", "loki.write",
			map[string]interface{}{"endpoint": []interface{}{map[string]interface{}{
				"url": map[string]interface{}{"$expr": `sys.env("LOKI_URL") + "//` + authoredHost + `"`},
			}}},
			nil,
			func(h simulate.HarnessEndpoints) string { return h.CaptureBaseURL + "/capture/loki/loki/api/v1/push" }),
		Entry("GraphBinding, pyroscope.write", "pyroscope.write",
			map[string]interface{}{},
			[]visual.GraphBinding{{Node: "d1", Prop: "url", Ref: visual.BindingRef{Expr: `"https://` + authoredHost + `/ingest"`}}},
			func(h simulate.HarnessEndpoints) string { return h.CaptureBaseURL + "/capture/pyroscope/ingest" }),
		Entry("nested block path, otelcol.exporter.otlphttp", "otelcol.exporter.otlphttp",
			map[string]interface{}{"client": map[string]interface{}{"endpoint": "https://" + authoredHost + ":4318"}},
			nil,
			func(h simulate.HarnessEndpoints) string { return h.CaptureBaseURL + "/capture/otlphttp" }),
		Entry("absent path is created, otelcol.exporter.otlp", "otelcol.exporter.otlp",
			map[string]interface{}{},
			nil,
			func(h simulate.HarnessEndpoints) string { return h.OTLPGRPCAddress }),
	)

	It("downgrades TLS on the gRPC exporter, because the in-pod capture endpoint is unauthenticated", func() {
		doc := singleNodeGraph("otelcol.exporter.otlp")
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(visual.Render(result.Graph, payload).Content).To(ContainSubstring("insecure = true"))
	})

	It("takes the harness from its argument, never from a constant", func() {
		doc := corpusGraph("minimal-scrape")
		one, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
		two, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: altHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		first := visual.Render(one.Graph, payload).Content
		second := visual.Render(two.Graph, payload).Content
		Expect(first).NotTo(Equal(second))
		Expect(first).To(ContainSubstring(testHarness().CaptureBaseURL))
		Expect(first).To(ContainSubstring(testHarness().TargetAddress))
		Expect(second).To(ContainSubstring(altHarness().CaptureBaseURL))
		Expect(second).To(ContainSubstring(altHarness().TargetAddress))
		Expect(second).NotTo(ContainSubstring(testHarness().CaptureBaseURL))
	})

	It("reports a missing harness field instead of falling back to a default", func() {
		doc := singleNodeGraph("prometheus.remote_write")
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: simulate.HarnessEndpoints{},
		})
		Expect(errorCodes(transformErrors(err))).To(ConsistOf(simulate.CodeHarnessIncomplete))
	})

	It("never mutates the graph it was given", func() {
		doc := corpusGraph("bindings-secret")
		before := visual.Render(doc, payload).Content
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(visual.Render(doc, payload).Content).To(Equal(before),
			"the authored graph must still render for the Code tab after a transform")
	})
})

var _ = Describe("Transform: rule G — source stubs", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	// Every stub in the overlay is exercised, because a stub that renders to a
	// component the binary does not have (discovery.static, which Alloy v1.18.1
	// has never shipped) is exactly the kind of mistake that passes a render
	// test and fails a run.
	It("stubs every component the overlay maps, re-pointing the export the stub actually declares", func() {
		var static, lokiFile int
		for name, comp := range policy.Components {
			if comp.Stub == nil {
				continue
			}
			doc := stubGraph(name, comp.Stub.Type)
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred(), "stub for %q must transform", name)

			stub := nodeByID(result.Graph, "s1")
			content := visual.Render(result.Graph, payload).Content

			switch comp.Stub.Type {
			case simulate.StubTypeStatic:
				Expect(stub.Component).To(Equal("discovery.relabel"), "stub for %q", name)
				Expect(stub.Props).To(HaveKey("targets"))
				Expect(content).To(ContainSubstring(testHarness().TargetAddress), "stub for %q", name)
				// discovery.relabel exports `output`; the authored component
				// exported `targets`. A stub that forgets this renders a
				// reference to a port that does not exist.
				Expect(content).To(ContainSubstring("discovery.relabel.src.output"), "stub for %q renders:\n%s", name, content)
				Expect(edgePorts(result.Graph)).To(ContainElement("output"))
				static++
			case simulate.StubTypeLokiFile:
				Expect(stub.Component).To(Equal("loki.source.file"), "stub for %q", name)
				Expect(content).To(ContainSubstring(testHarness().LogDir+"/"+comp.Stub.Fixture+".log"), "stub for %q", name)
				// loki.source.file declares the same port names, so the
				// downstream forward_to wire must survive untouched.
				Expect(content).To(ContainSubstring("forward_to = [loki.write.sink.receiver]"), "stub for %q renders:\n%s", name, content)
				lokiFile++
			}
			Expect(stub.Label).To(Equal("src"), "the stub keeps the authored label so downstream references still resolve")
		}
		Expect(static).To(Equal(31), "adding local.file_match's discovery_stub (part of closing M9) moves one source from sim_keep to static")
		Expect(lokiFile).To(Equal(4), "loki.source.{kubernetes,docker,file,podlogs} are the stubbable log sources")
	})

	It("fails closed on a source the overlay does not map, with the §6.4 message", func() {
		doc := singleNodeGraph("discovery.process")
		doc.Nodes[0].ID = "s1"
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		errs := transformErrors(err)
		Expect(errs).To(HaveLen(1))
		Expect(errs[0].Code).To(Equal(simulate.CodeCannotStub))
		Expect(errs[0].Message).To(Equal("cannot stub discovery.process — use S2 for its downstream rules"))
	})

	It("reports every unstubbable source at once, not just the first", func() {
		doc := singleNodeGraph("discovery.process")
		doc.Nodes = append(doc.Nodes,
			visual.GraphNode{ID: "s2", Component: "loki.source.syslog", Label: "b", Props: map[string]interface{}{}},
			visual.GraphNode{ID: "s3", Component: "loki.source.kafka", Label: "c", Props: map[string]interface{}{}},
		)
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(transformErrors(err)).To(HaveLen(3))
	})

	It("returns no graph at all when anything failed", func() {
		doc := singleNodeGraph("discovery.process")
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).To(HaveOccurred())
		Expect(result.Graph.Nodes).To(BeEmpty(), "a partially-transformed graph must not be reachable")
	})

	It("drops the wires feeding a stubbed source and says so", func() {
		doc := corpusGraph("logs-chain")
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		var dropped, stubbed int
		for _, r := range result.Rewrites {
			switch r.Kind { //nolint:exhaustive // only the two kinds this graph can produce are counted
			case simulate.RewriteEdgeDropped:
				dropped++
			case simulate.RewriteLogSourceStubbed:
				stubbed++
			}
		}
		Expect(stubbed).To(Equal(1))
		Expect(dropped).To(Equal(1), "the local.file_match → loki.source.file wire feeds a stub and must go")

		content := visual.Render(result.Graph, payload).Content
		Expect(content).To(ContainSubstring("__path__"))
		Expect(content).To(ContainSubstring("forward_to = [loki.process."))
	})

	// §6.4: "the fixture library from 6.3a, so relabel chains behave
	// identically". Sharing the function makes that a compile-time fact; a
	// parallel copy would only be a convention that rots.
	It("serves the S2 relabel fixtures as the k8s discovery stub, address aside", func() {
		targets, ok := simulate.StubTargets("k8s-pod-targets")
		Expect(ok).To(BeTrue())
		expected := simulate.BuiltinRelabelTargets()
		Expect(targets).To(HaveLen(len(expected)))
		for i := range targets {
			for k, v := range expected[i] {
				if k == "__address__" {
					continue
				}
				Expect(targets[i]).To(HaveKeyWithValue(k, v))
			}
		}
	})

	It("serves the S2 log fixtures as the k8s log stub", func() {
		lines, ok := simulate.StubLogLines("k8s-pod-logs")
		Expect(ok).To(BeTrue())
		Expect(lines).To(Equal(simulate.BuiltinLogLines()))
	})

	It("agrees with the fixture-name list the schema guard enforces", func() {
		// internal/schema cannot import internal/simulate without costing itself
		// its leaf status, so it keeps its own copy of the names. This is what
		// makes that duplication safe.
		Expect(simulate.StubFixtureNames()).To(Equal(schema.StubFixtureNames()))
	})

	It("agrees with the value classes the schema guard accepts", func() {
		// The same duplication, and a sharper consequence: a class the guard
		// accepts but rule K does not switch on would be a keep entry that
		// passes `make schema-verify` and then silently behaves as verbatim —
		// which for target_set means the authored address survives.
		Expect(schema.KeepClasses()).To(ConsistOf(simulate.ClassVerbatim, simulate.ClassTargetSet))
	})
})

var _ = Describe("Transform: totality and determinism over the shared corpus", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	It("transforms every corpus graph", func() {
		for _, name := range corpusNames() {
			_, err := simulate.Transform(simulate.TransformRequest{
				Graph: corpusGraph(name), Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred(), "corpus entry %q must transform", name)
		}
	})

	// The determinism theorem, mirroring §7.2 item 2 for the renderer: the
	// output must be a function of the graph's content, not of the order its
	// nodes happen to sit in the JSON array. Without it the "what was rewritten"
	// disclosure count is not assertable.
	It("is permutation-invariant in both the render and the disclosure list", func() {
		for _, name := range corpusNames() {
			base := corpusGraph(name)
			want, err := simulate.Transform(simulate.TransformRequest{
				Graph: base, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred())
			wantContent := visual.Render(want.Graph, payload).Content

			for seed := int64(1); seed <= 5; seed++ {
				shuffled := corpusGraph(name)
				rng := rand.New(rand.NewSource(seed))
				rng.Shuffle(len(shuffled.Nodes), func(i, j int) {
					shuffled.Nodes[i], shuffled.Nodes[j] = shuffled.Nodes[j], shuffled.Nodes[i]
				})
				rng.Shuffle(len(shuffled.Edges), func(i, j int) {
					shuffled.Edges[i], shuffled.Edges[j] = shuffled.Edges[j], shuffled.Edges[i]
				})
				got, err := simulate.Transform(simulate.TransformRequest{
					Graph: shuffled, Schema: payload, Policy: policy, Harness: testHarness(),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(visual.Render(got.Graph, payload).Content).To(Equal(wantContent),
					"corpus entry %q, seed %d", name, seed)
				Expect(got.Rewrites).To(Equal(want.Rewrites), "corpus entry %q, seed %d", name, seed)
			}
		}
	})
})

var _ = Describe("Transform: the disclosure list never carries a secret", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	It("leaves From empty for every secret_* rewrite across the corpus", func() {
		var secretRewrites int
		for _, name := range corpusNames() {
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: corpusGraph(name), Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred())
			for _, r := range result.Rewrites {
				if !strings.HasPrefix(string(r.Kind), "secret_") {
					continue
				}
				secretRewrites++
				Expect(r.From).To(BeEmpty(),
					"corpus entry %q: rewrite %s at %v must not carry the value it removed", name, r.Kind, r.Path)
			}
		}
		Expect(secretRewrites).To(BeNumerically(">", 0), "the corpus must actually exercise the secret rules")
	})

	It("redacts the authored destination URL in structured logs", func() {
		doc := singleNodeGraph("prometheus.remote_write")
		doc.Nodes[0].Props = map[string]interface{}{"endpoint": []interface{}{
			map[string]interface{}{"url": "https://" + authoredHost + "/api/v1/write"},
		}}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		for _, r := range result.Rewrites {
			logger.Info("rewrite", "rewrite", r)
		}
		Expect(buf.String()).NotTo(ContainSubstring(authoredHost),
			"a destination URL may be shown to its own author, never written to a log")
		Expect(buf.String()).To(ContainSubstring(`"from":"***"`))
	})
})

// singleNodeGraph builds the smallest graph containing one component.
func singleNodeGraph(component string) visual.GraphDocument {
	return visual.GraphDocument{
		Kind:          "alloy-graph/v1",
		SchemaVersion: "alloy-v1.18.1",
		Nodes: []visual.GraphNode{{
			ID: "d1", Component: component, Label: "sink", Props: map[string]interface{}{},
		}},
	}
}

// stubGraph wires one stubbable source to the consumer its wire type demands,
// so the port rename (or its absence) is observable in the rendered reference.
func stubGraph(component, stubType string) visual.GraphDocument {
	doc := visual.GraphDocument{
		Kind:          "alloy-graph/v1",
		SchemaVersion: "alloy-v1.18.1",
		Nodes: []visual.GraphNode{
			{ID: "s1", Component: component, Label: "src", Props: map[string]interface{}{}},
		},
	}
	if stubType == simulate.StubTypeStatic {
		doc.Nodes = append(doc.Nodes, visual.GraphNode{
			ID: "c1", Component: "prometheus.scrape", Label: "scrape", Props: map[string]interface{}{},
		})
		doc.Edges = []visual.GraphEdge{{
			ID: "e1", From: visual.PortRef{Node: "s1", Port: "targets"}, To: visual.PortRef{Node: "c1", Port: "targets"},
		}}
		return doc
	}
	doc.Nodes = append(doc.Nodes, visual.GraphNode{
		ID: "c1", Component: "loki.write", Label: "sink", Props: map[string]interface{}{},
	})
	doc.Edges = []visual.GraphEdge{{
		ID: "e1", From: visual.PortRef{Node: "s1", Port: "forward_to"}, To: visual.PortRef{Node: "c1", Port: "receiver"},
	}}
	return doc
}

func edgePorts(doc visual.GraphDocument) []string {
	ports := make([]string, 0, len(doc.Edges))
	for _, e := range doc.Edges {
		ports = append(ports, e.From.Port)
	}
	return ports
}
