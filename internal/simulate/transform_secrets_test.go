package simulate_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/simulate"
	"shepherd/internal/visual"
)

// §11 item 6's post-condition, kept in its own file so the red proof
// (docs/proofs/transform-secret-drop.md) names exactly one place to look.
//
// The credentials below are chosen so that rule K is the ONLY thing that can
// remove them: sys.env("PROM_PASSWORD") sits two blocks deep at a path rule D
// never touches, and sys.env("TOKEN") is a binding with no secret-source node
// behind it, so rule S1 has nothing to match. Verified against the real binary:
// a config with a rewritten capture URL and an intact sys.env password passes
// `alloy validate --stability.level=experimental` in grafana/alloy:v1.18.1 with
// exit 0. Validation cannot be the safety net.
//
// This file predates the deny-by-default inversion and every credential in it
// is one the artifact declares `secret`, which is why it stayed green through
// all three CRITICAL-1 leaks. transform_keep_test.go is the file that covers
// the other 6144 attribute paths; this one stays because the shapes it names —
// a $expr two blocks deep, a binding with no source behind it, a capsule handle
// — are still the shapes rule K has to refuse, and it names them individually.

// leakedTokens is the grep list. Every entry is a value or a name that must not
// survive into the sandbox in any form.
var leakedTokens = []string{
	`sys.env("SCRAPE_TOKEN")`,
	`sys.env("PROM_PASSWORD")`,
	`sys.env("TOKEN")`,
	"loki-literal-token",
	"remote.kubernetes.secret",
	"otelcol.auth.basic",
	authoredHost,
}

var _ = Describe("Transform: no credential reaches the sandbox", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	It("drops every credential form the renderer can emit", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: worstCaseGraph(), Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		render := visual.Render(result.Graph, payload)
		for _, token := range leakedTokens {
			Expect(render.Content).NotTo(ContainSubstring(token),
				"%q survived the transform:\n%s", token, render.Content)
		}
		for _, d := range render.Diagnostics {
			Expect(d.Code).NotTo(Equal("secret_by_value"), "%s", d.Message)
		}
		// The transform must not merely delete: what it kept has to be the
		// harness, or the sandbox run reports an empty capture that reads as a
		// pipeline fault.
		Expect(render.Content).To(ContainSubstring(testHarness().CaptureBaseURL))
	})

	// This is the assertion the red proof breaks. It is deliberately narrow: a
	// binding onto a secret-typed attribute is invisible to rule D (not an
	// endpoint path) and to rule S1 (no secret-source node), and the renderer
	// emits it as a plain expression, so it raises no secret_by_value
	// diagnostic either. The type-driven sweep is the only thing standing
	// between it and the sandbox.
	It("drops a secret-typed binding that no other rule can see", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{{
				ID: "n1", Component: "prometheus.scrape", Label: "app", Props: map[string]interface{}{},
			}},
			Bindings: []visual.GraphBinding{{
				Node: "n1", Prop: "bearer_token", Ref: visual.BindingRef{Expr: `sys.env("TOKEN")`},
			}},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring(`sys.env("TOKEN")`), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring("bearer_token"), "rendered:\n%s", content)
	})

	It("removes the secret source and every reference to it, on the shipped corpus graph", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: corpusGraph("bindings-secret"), Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		for _, n := range result.Graph.Nodes {
			Expect(n.Component).NotTo(Equal("remote.kubernetes.secret"))
		}
		content := visual.Render(result.Graph, payload).Content
		// P2: the reference token the renderer would have emitted.
		Expect(content).NotTo(ContainSubstring("remote.kubernetes.secret." + visual.SanitizeLabel("creds") + "."))
		// P2': the component name at all.
		Expect(content).NotTo(ContainSubstring("remote.kubernetes.secret"))
		Expect(content).NotTo(ContainSubstring("prometheus.example.com"))
		Expect(content).To(ContainSubstring(testHarness().CaptureBaseURL + "/capture/prometheus/api/v1/write"))
	})

	// §6.4: "other secret uses get \"simulated\"". A string-typed use is
	// substitutable; a secret-typed one is not, because a literal there is
	// exactly what the renderer's secret_by_value check refuses.
	It("substitutes a removed secret source only where a literal string is legal", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{ID: "n1", Component: "remote.kubernetes.secret", Label: "creds", Props: map[string]interface{}{
					"namespace": "monitoring", "name": "creds",
				}},
				{ID: "n2", Component: "loki.write", Label: "sink", Props: map[string]interface{}{
					"endpoint": []interface{}{map[string]interface{}{
						// string-typed: substitutable
						"name": map[string]interface{}{"$expr": `remote.kubernetes.secret.creds.data["name"]`},
						// secret-typed: must be deleted, never substituted
						"bearer_token": map[string]interface{}{"$expr": `remote.kubernetes.secret.creds.data["token"]`},
					}},
				}},
			},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).To(ContainSubstring(`name = "simulated"`), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring("bearer_token"), "rendered:\n%s", content)
		Expect(rewriteKinds(result.Rewrites)).To(ContainElements(
			simulate.RewriteSecretNodeRemoved, simulate.RewriteSecretRefSubstituted, simulate.RewriteSecretDropped))
	})
})

// Nested-path bindings (#426): a doc.bindings[] entry can now point inside a
// block (`endpoint[0].name`). Rule S1 must substitute or drop it exactly as it
// does the same expression held in props, and rule K must still remove every
// binding that survives.
var _ = Describe("Transform: nested-path bindings", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	It("substitutes a nested binding to a removed secret only where a literal string is legal", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{ID: "n1", Component: "remote.kubernetes.secret", Label: "creds", Props: map[string]interface{}{
					"namespace": "monitoring", "name": "creds",
				}},
				{ID: "n2", Component: "loki.write", Label: "sink", Props: map[string]interface{}{
					"endpoint": []interface{}{map[string]interface{}{}},
				}},
			},
			Bindings: []visual.GraphBinding{
				{Node: "n2", Prop: "endpoint[0].name", Ref: visual.BindingRef{
					Node: "n1", Export: "data", Expr: `remote.kubernetes.secret.creds.data["name"]`,
				}},
				{Node: "n2", Prop: "endpoint[0].bearer_token", Ref: visual.BindingRef{
					Node: "n1", Export: "data", Expr: `remote.kubernetes.secret.creds.data["token"]`,
				}},
			},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		render := visual.Render(result.Graph, payload)
		Expect(render.Content).To(ContainSubstring(`name = "simulated"`), "rendered:\n%s", render.Content)
		Expect(render.Content).NotTo(ContainSubstring("bearer_token"), "rendered:\n%s", render.Content)
		Expect(render.Content).NotTo(ContainSubstring("remote.kubernetes.secret"))
		for _, n := range result.Graph.Nodes {
			for k := range n.Props {
				Expect(k).NotTo(ContainSubstring("["), "a binding path was written as a top-level prop")
			}
		}
		Expect(result.Graph.Bindings).To(BeEmpty())
	})

	It("drops a nested binding with no secret source behind it", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{{
				ID: "n1", Component: "prometheus.remote_write", Label: "sink", Props: map[string]interface{}{
					"endpoint": []interface{}{map[string]interface{}{"url": "https://" + authoredHost + "/api/v1/write"}},
				},
			}},
			Bindings: []visual.GraphBinding{
				{Node: "n1", Prop: "endpoint[0].basic_auth.password", Ref: visual.BindingRef{Expr: `sys.env("PROM_PASSWORD")`}},
				{Node: "n1", Prop: "endpoint[0].url", Ref: visual.BindingRef{Expr: `sys.env("PROM_URL")`}},
			},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring("sys.env"), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring(authoredHost))
		Expect(content).To(ContainSubstring(testHarness().CaptureBaseURL))
	})

	It("removes every secret reference from the shipped bindings-nested corpus graph", func() {
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: corpusGraph("bindings-nested"), Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring("remote.kubernetes.secret"))
		for _, host := range []string{"primary.example.com", "secondary.example.com", "loki.example.com"} {
			Expect(content).NotTo(ContainSubstring(host))
		}
	})
})

// worstCaseGraph carries a credential in every form the renderer can emit
// (render.go): a literal on a secret attribute, the $expr escape at the top
// level, the same escape two blocks deep, the escape inside a list element and
// inside a map value, a GraphBinding, and a capsule-typed handle. There is no
// fifth form for an edge to carry — every PortSchema.Type ranges over the
// overlay's closed wire_types map, none of which is string- or secret-shaped,
// which the schema suite asserts over the whole artifact.
func worstCaseGraph() visual.GraphDocument {
	return visual.GraphDocument{
		Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
		Nodes: []visual.GraphNode{
			{ID: "n1", Component: "remote.kubernetes.secret", Label: "creds", Props: map[string]interface{}{
				"namespace": "monitoring", "name": "prometheus-creds",
			}},
			{ID: "n2", Component: "otelcol.auth.basic", Label: "creds", Props: map[string]interface{}{
				"username": "shepherd",
			}},
			{ID: "n3", Component: "prometheus.scrape", Label: "app", Props: map[string]interface{}{
				// $expr at the top level, on a secret-typed attribute.
				"bearer_token": map[string]interface{}{"$expr": `sys.env("SCRAPE_TOKEN")`},
				// $expr inside a list element, referencing the removed source.
				"scrape_protocols": []interface{}{
					map[string]interface{}{"$expr": `remote.kubernetes.secret.creds.data["proto"]`},
				},
			}},
			{ID: "n4", Component: "prometheus.remote_write", Label: "sink", Props: map[string]interface{}{
				"endpoint": []interface{}{map[string]interface{}{
					"url": "https://" + authoredHost + "/api/v1/write",
					"basic_auth": map[string]interface{}{
						"username": "shepherd",
						// Two blocks deep, environment-sourced: invisible to
						// rules D and S1 alike.
						"password": map[string]interface{}{"$expr": `sys.env("PROM_PASSWORD")`},
					},
				}},
			}},
			{ID: "n5", Component: "loki.write", Label: "logs", Props: map[string]interface{}{
				"endpoint": []interface{}{map[string]interface{}{
					"url": "https://" + authoredHost + "/loki/api/v1/push",
					// A raw literal on a secret-typed attribute.
					"bearer_token": "loki-literal-token",
				}},
			}},
			{ID: "n6", Component: "otelcol.exporter.otlphttp", Label: "otlp", Props: map[string]interface{}{
				"client": map[string]interface{}{
					"endpoint": "https://" + authoredHost + ":4318",
					// A capsule-typed handle onto the auth component.
					"auth": "otelcol.auth.basic.creds.handler",
					// $expr inside a map value.
					"headers": map[string]interface{}{
						"x-token": map[string]interface{}{"$expr": `remote.kubernetes.secret.creds.data["header"]`},
					},
				},
			}},
			{ID: "n7", Component: "prometheus.scrape", Label: "app2", Props: map[string]interface{}{}},
		},
		Bindings: []visual.GraphBinding{
			// A binding with no secret-source node behind it at all.
			{Node: "n7", Prop: "bearer_token", Ref: visual.BindingRef{Expr: `sys.env("TOKEN")`}},
		},
	}
}
