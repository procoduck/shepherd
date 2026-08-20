package simulate_test

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/simulate"
	"shepherd/internal/visual"
)

// Rule K's coverage, driven off the SHIPPED artifact rather than a case list.
//
// The suite this replaces asserted that the transform dropped every prop the
// artifact declares `secret`. That is 338 of 6482 declared attribute paths, so
// it said nothing at all about the other 6144 — and three real credentials
// (otelcol.receiver.solace auth.sasl_xauth2.bearer, otelcol.receiver.cloudflare
// secret, otelcol.processor.resourcedetection openshift.token) walked through
// it, rendered with zero diagnostics and passed real `alloy validate`.
//
// The table below is generated from the artifact, so it grows with an Alloy
// bump instead of going stale, and it fails in BOTH directions: red if a path
// that is not on a keep list reaches the render, and red if a path that IS on
// one does not. The second half matters as much as the first — a deny-by-default
// transform that quietly becomes deny-everything tells the user nothing about
// their pipeline, which is its own critical failure.
//
// WHAT IT STRUCTURALLY CANNOT ASK, named here because a round-2 review found the
// file being read as though it could. Every assertion in it compares rule K to
// the OVERLAY, so it is green for any leak the overlay authorises — and the
// counts below pin that agreement as intended behaviour. It measured fidelity
// to the keep list through all five of the round-2 credential findings (a
// credential at a user-chosen key inside a kept `map`, one parked in a
// user-named target label, `params`, `metrics_path`, and a probe exporter's
// `address` key), because the keep list said yes to each of them.
// transform_keys_test.go is the file that can fail when the keep list ITSELF is
// wrong: it plants the credential at a key the USER chose rather than at a path
// the schema declares.

// canaryPath is one (component, canonical attribute path) pair to probe.
type canaryPath struct {
	component string
	canon     []string
	typ       string
}

func (c canaryPath) String() string { return c.component + " " + strings.Join(c.canon, ".") }

// stringCarryingTypes are the declared types whose value can hold a credential
// the renderer will emit as text. bool and number are excluded because no
// credential fits in one, not because they are trusted: rule K drops them too
// unless they are kept, which the disclosure-count spec below asserts.
var stringCarryingTypes = map[string]bool{
	"string": true, "secret": true, "capsule": true,
	"duration": true, "list": true, "map": true,
}

// canaryPaths enumerates every attribute path in the shipped schema that can
// carry a string, in the canonical form the keep lists are written in.
func canaryPaths(payload visual.SchemaPayload) []canaryPath {
	names := make([]string, 0, len(payload.Components))
	for name := range payload.Components {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []canaryPath
	for _, name := range names {
		comp := payload.Components[name]
		var walk func(attrs []visual.AttributeSchema, blocks []visual.BlockSchema, prefix []string)
		walk = func(attrs []visual.AttributeSchema, blocks []visual.BlockSchema, prefix []string) {
			for _, a := range attrs {
				if !stringCarryingTypes[a.Type] {
					continue
				}
				out = append(out, canaryPath{component: name, canon: append(append([]string{}, prefix...), a.Name), typ: a.Type})
			}
			for _, b := range blocks {
				child := append(append([]string{}, prefix...), b.Name)
				if b.Repeatable {
					child = append(child, "*")
				}
				walk(b.Attributes, b.Blocks, child)
			}
		}
		walk(comp.Attributes, comp.Blocks, nil)
	}
	return out
}

// canaryProps nests a probe value at a canonical path, expanding "*" into a
// one-element block list exactly as the renderer reads it.
func canaryProps(canon []string, value interface{}) map[string]interface{} {
	if len(canon) == 1 {
		return map[string]interface{}{canon[0]: value}
	}
	name, rest := canon[0], canon[1:]
	repeatable := rest[0] == "*"
	if repeatable {
		rest = rest[1:]
	}
	inner := canaryProps(rest, value)
	if repeatable {
		return map[string]interface{}{name: []interface{}{inner}}
	}
	return map[string]interface{}{name: inner}
}

// canaryValue wraps the probe string in the shape the declared type and value
// class demand, so the renderer emits it rather than refusing it as a type
// mismatch. A refused value would make the probe pass for the wrong reason.
//
// A target_set path gets the canary at __address__ — the CRITICAL-2 position —
// rather than as a bare list element, because a bare element would be dropped
// as a malformed label set and the probe would pass without ever exercising the
// class.
//
// The map key is deliberately NOT credential-shaped. This sweep measures
// whether the PATH is kept, and rule K's constrainKeys drops a user-chosen key
// whose name is credential-shaped — the probe key used to be "canary_key",
// which matches the guard's `(^|_)key$` and turned all thirteen map paths into
// false "the allowlist promises what the transform will not deliver" failures.
// The credential-named key is the subject of its own sweep, in
// transform_keys_test.go, which asserts the opposite outcome for the same
// paths.
func canaryValue(typ, class, canary string) interface{} {
	if class == simulate.ClassTargetSet {
		return []interface{}{map[string]interface{}{"__address__": canary, "job": "probe"}}
	}
	switch typ {
	case "list":
		return []interface{}{canary}
	case "map":
		return map[string]interface{}{"canary_label": canary}
	}
	return canary
}

var _ = Describe("Transform: rule K keeps exactly what the overlay allowlists", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	It("plants a canary at every string-carrying attribute path in the artifact and finds it in the render if and only if the path is allowlisted", func() {
		paths := canaryPaths(payload)
		Expect(len(paths)).To(Equal(4678),
			"the probe must cover every string-carrying attribute path the shipped artifact declares")

		var leaked, missing, refused []string
		present, absent := 0, 0
		for i, p := range paths {
			canary := fmt.Sprintf("CANARY-%d-END", i)
			keep := policy.Components[p.component].Keep
			class, kept := keep.ClassOf(p.canon)
			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: p.component, Label: "probe",
					Props: canaryProps(p.canon, canaryValue(p.typ, class, canary)),
				}},
			}
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})

			// A secret or a capsule is never keepable however the list is
			// written: the renderer refuses the first outright and emits the
			// second as a raw expression. A target_set path IS kept, but the
			// canary sits at __address__, which the class overwrites from the
			// harness — so the path survives and the value must not. That is
			// CRITICAL-2, asserted here for all fourteen components that carry
			// a target set rather than for the one the finding happened to use.
			want := kept && p.typ != "secret" && p.typ != "capsule" && class != simulate.ClassTargetSet
			if want {
				present++
			} else {
				absent++
			}

			if err != nil {
				// A refused graph ships nothing, which is containment — but a
				// path the allowlist names must not be refused, or the
				// allowlist is describing a simulation that never happens.
				if want {
					refused = append(refused, p.String()+": "+err.Error())
				}
				continue
			}
			switch got := strings.Contains(visual.Render(result.Graph, payload).Content, canary); {
			case got && !want:
				leaked = append(leaked, p.String())
			case !got && want:
				missing = append(missing, p.String())
			}
		}

		Expect(leaked).To(BeEmpty(),
			"%d attribute paths reached the sandbox config without being on any keep list", len(leaked))
		Expect(missing).To(BeEmpty(),
			"%d allowlisted attribute paths were dropped, so the simulation would show the user less than they authored", len(missing))
		Expect(refused).To(BeEmpty(),
			"%d allowlisted attribute paths made the transform fail closed, so the allowlist promises what the transform will not deliver", len(refused))
		// Stated so the probe cannot pass vacuously in either direction: an
		// allowlist that kept nothing, or a transform that kept everything,
		// changes these two numbers. `want` here is computed from the POLICY
		// alone (kept && not secret/capsule/target_set), so it moves with what
		// sim_keep NAMES, not with whether a run actually succeeds — the 19
		// components the availability fix moved to sim_unsupported already had
		// empty sim_keep and do not touch this count at all (see the "probes
		// that produced a sandbox config" count in transform_address_test.go
		// for their effect).
		// 601, down from 615: net of two availability-fix changes (VB-1 §6.4).
		// otelcol.exporter.splunkhec moved off sim_destination onto the
		// deliberately-unmappable list (its required splunk.token is a secret,
		// so no keep list could ever satisfy it), losing 22 string-carrying
		// keep paths with it. Eight components gained exactly one
		// string-carrying keep path each to cover a required, non-credential
		// attribute the old empty keep list omitted (loki.enrich and
		// pyroscope.enrich's target_match_label; otelcol.receiver.cloudflare,
		// fluentforward and tcplog's local bind address; file_stats and
		// filelog's include; prometheus.exporter.static's text). 615 - 22 + 8
		// = 601.
		// 4077, up from 4063 by the same 14: present and absent are a
		// partition of the fixed 4678-path probe, so one falls exactly as far
		// as the other rises.
		Expect(present).To(Equal(601), "string-carrying paths that reach the sandbox")
		Expect(absent).To(Equal(4077), "string-carrying paths that do not")
	})

	// The three leaks finding 1 proved, named individually so a regression says
	// which one came back rather than "N paths leaked".
	DescribeTable("refuses the string-typed credentials the type-driven sweep let through",
		func(component string, path []string, expectCode string) {
			const canary = "CANARY-PROD-CREDENTIAL"
			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: component, Label: "exfil", Props: canaryProps(path, canary),
				}},
			}
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			if expectCode != "" {
				Expect(errorCodes(transformErrors(err))).To(ContainElement(expectCode))
				return
			}
			Expect(err).NotTo(HaveOccurred())

			content := visual.Render(result.Graph, payload).Content
			Expect(content).NotTo(ContainSubstring(canary), "rendered:\n%s", content)
			Expect(content).NotTo(ContainSubstring(path[len(path)-1]), "rendered:\n%s", content)
			// The user is told, rather than left to wonder why the sandbox
			// behaved differently from production (§6.5).
			Expect(rewriteKinds(result.Rewrites)).To(ContainElement(simulate.RewritePropDropped))
		},
		// Declared `string`, and rendered with zero diagnostics and passed
		// real `alloy validate --stability.level=experimental` with exit 0
		// when this was found. otelcol.receiver.solace is now sim_unsupported
		// (availability fix, VB-1 §6.4: it also requires `queue`, which
		// sim_keep never covered, and the component's whole purpose is a real
		// external Solace broker the capture harness cannot emulate) — a
		// stronger closure than dropping this one attribute, since now
		// nothing about the component reaches the sandbox at all.
		Entry("otelcol.receiver.solace auth.sasl_xauth2.bearer",
			"otelcol.receiver.solace", []string{"auth", "sasl_xauth2", "bearer"}, simulate.CodeUnsupportedComponent),
		// Declared `string`, at the component's top level.
		Entry("otelcol.receiver.cloudflare secret",
			"otelcol.receiver.cloudflare", []string{"secret"}, ""),
		// Declared `string`, and its component cannot be simulated at all: it
		// detects the SANDBOX's environment, so §6.5 refuses it outright.
		Entry("otelcol.processor.resourcedetection openshift.token",
			"otelcol.processor.resourcedetection", []string{"openshift", "token"}, simulate.CodeUnsupportedComponent),
		// Finding M9: prometheus.exporter.* carries none of the "discovery." /
		// "loki.source." name prefix rule G used to gate stubbing on, so before
		// the category switch this node was never examined by rule G at all —
		// only ever rule K, which still empties it (its sim_keep is empty), but
		// the point stands independently of stubbing: nothing named
		// prometheus.exporter.* gets a free pass on its name. api_token_file is
		// declared `string`, not `secret`.
		Entry("prometheus.exporter.github api_token_file",
			"prometheus.exporter.github", []string{"api_token_file"}, ""),
	)

	// Finding M9, the other half: a Sources-category node the overlay maps no
	// discovery_stub for is REJECTED — either stubbed, or removed outright by
	// rule K's dropDeadWeight once every authored setting is dropped and it
	// feeds nothing — rather than passed through verbatim as the pre-rule-K
	// transform did ("each runs whatever the user authored") or left in the
	// render as an empty husk. prometheus.exporter.* is the finding's own
	// example, and it carries none of the "discovery."/"loki.source." prefix
	// rule G's stub gate used to require, so before the category switch this
	// was the exact shape of node the gate never even looked at.
	It("rejects a prometheus.exporter.* node's authored settings instead of passing them through", func() {
		const dsn = "user:CANARY-PASSWORD@tcp(prod-mysql.example.com:3306)/metrics"
		doc := singleNodeGraph("prometheus.exporter.mysql")
		doc.Nodes[0].Props = map[string]interface{}{
			"data_source_name":  dsn,
			"enable_collectors": []interface{}{"info_schema.tables"},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred(), "an unstubbed prometheus.exporter.* is rejected by removal, not by failing the whole run")

		// Nothing feeds off it here, so dropDeadWeight removes the husk
		// entirely rather than shipping an empty, unreachable component body —
		// disclosed, not silent.
		Expect(result.Graph.Nodes).To(BeEmpty(), "the node was removed, not passed through with its props stripped")
		Expect(result.Rewrites).To(ContainElement(SatisfyAll(
			HaveField("NodeID", "d1"),
			HaveField("Component", "prometheus.exporter.mysql"),
			HaveField("Detail", ContainSubstring("removed")),
		)))

		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring("CANARY-PASSWORD"), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring("prod-mysql.example.com"), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring("info_schema.tables"), "rendered:\n%s", content)
	})

	// Finding 10: GraphBindings are an unvalidated channel. render.go writes
	// bind.Prop = bind.Ref.Expr verbatim at the node's top level with no type
	// check, and the old filter removed one only when topLevelType() said
	// `secret` — which is "" for every nested or dotted prop and `string` for
	// otelcol.receiver.cloudflare's `secret`.
	DescribeTable("removes every binding, whatever prop it names",
		func(component, prop string) {
			const canary = "CANARY-BOUND-CREDENTIAL"
			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: component, Label: "sink", Props: map[string]interface{}{},
				}},
				Bindings: []visual.GraphBinding{{
					Node: "n1", Prop: prop, Ref: visual.BindingRef{Expr: `"` + canary + `"`},
				}},
			}
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Graph.Bindings).To(BeEmpty())

			content := visual.Render(result.Graph, payload).Content
			Expect(content).NotTo(ContainSubstring(canary), "rendered:\n%s", content)
		},
		Entry("top-level secret-typed prop", "prometheus.remote_write", "password"),
		Entry("dotted prop the old type lookup could not resolve", "prometheus.remote_write", "endpoint.0.basic_auth.0.password"),
		Entry("string-typed top-level prop", "otelcol.receiver.cloudflare", "secret"),
		Entry("a prop that is not credential-shaped at all", "prometheus.scrape", "job_name"),
	)

	It("refuses an expression at a kept path, so sys.env cannot be smuggled through one", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{{
				ID: "n1", Component: "prometheus.scrape", Label: "app",
				Props: map[string]interface{}{
					// job_name IS on prometheus.scrape's keep list; the value is
					// what disqualifies it.
					"job_name": map[string]interface{}{"$expr": `sys.env("EXFIL_URL")`},
					// A literal at the same kept path survives, which is what
					// makes the refusal about the value and not the path.
					"scrape_interval": "45s",
				},
			}},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring("sys.env"), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`scrape_interval = "45s"`), "rendered:\n%s", content)
	})

	It("fails closed on a component the overlay marks unsupported, naming why", func() {
		doc := singleNodeGraph("mimir.rules.kubernetes")
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		errs := transformErrors(err)
		Expect(errorCodes(errs)).To(ConsistOf(simulate.CodeUnsupportedComponent))
		Expect(errs[0].Message).To(ContainSubstring("live Kubernetes API"))
	})

	It("fails closed on a component the overlay gives no keep list", func() {
		// Unreachable for the shipped overlay — the schema guard makes sure of
		// that — and reachable for a hand-edited one, which is when the deny
		// side has to hold.
		stripped := simulate.Policy{Components: map[string]simulate.ComponentPolicy{}}
		for name, comp := range policy.Components {
			comp.Keep = nil
			stripped.Components[name] = comp
		}
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph: singleNodeGraph("prometheus.scrape"), Schema: payload, Policy: stripped, Harness: testHarness(),
		})
		Expect(errorCodes(transformErrors(err))).To(ConsistOf(simulate.CodeCannotKeepProp))
	})
})

var _ = Describe("Transform: the simulated pipeline still shows the user their own pipeline", func() {
	// The failure mode that is its own critical: an over-aggressive keep list
	// makes S3 useless. Every assertion below is on a specific authored value,
	// not on a diff count, because "fewer things changed" is not the property
	// that matters — "the scrape interval, the job label, the relabel rule
	// bodies and the log stages are still there" is.
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	transformed := func(name string) string {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: corpusGraph(name), Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred(), "corpus entry %q must transform", name)
		return visual.Render(result.Graph, payload).Content
	}

	It("keeps minimal-scrape's scrape settings", func() {
		content := transformed("minimal-scrape")
		Expect(content).To(ContainSubstring(`job_name = "app"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`scrape_interval = "30s"`), "rendered:\n%s", content)
	})

	It("keeps logs-chain's log stages and its tenant and cluster labels", func() {
		content := transformed("logs-chain")
		Expect(content).To(ContainSubstring("stage.json"), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`level = "level"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`msg = "message"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring("stage.labels"), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`tenant_id = "team-a"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`cluster = "eu-1"`), "rendered:\n%s", content)
	})

	It("keeps nested-blocks' queue tuning and external labels", func() {
		content := transformed("nested-blocks")
		Expect(content).To(ContainSubstring(`remote_timeout = "30s"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring("queue_config"), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`cluster = "eu-1"`), "rendered:\n%s", content)
		// …and drops the TLS material at the same depth, which is the whole
		// point of the allowlist being per-path.
		Expect(content).NotTo(ContainSubstring("tls_config"), "rendered:\n%s", content)
	})

	It("keeps fanin-fanout's relabel rule bodies, including the target_label the name guard flags", func() {
		content := transformed("fanin-fanout")
		Expect(content).To(ContainSubstring(`__meta_kubernetes_pod_phase`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`regex = "Running"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`action = "keep"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`target_label = "team"`), "rendered:\n%s", content)
	})

	It("keeps kitchen-sink's metric relabel rules and its scrape settings", func() {
		content := transformed("kitchen-sink")
		Expect(content).To(ContainSubstring(`job_name = "myapp"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`scrape_interval = "60s"`), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring("honor_labels = true"), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`regex = "go_.*"`), "rendered:\n%s", content)
	})
})
