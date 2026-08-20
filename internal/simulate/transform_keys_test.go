package simulate_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/simulate"
	"shepherd/internal/visual"
)

// Whether the KEEP LIST ITSELF is fit, which is the question transform_keep_test.go
// structurally cannot ask.
//
// That file plants a canary at every attribute path the artifact declares and
// asserts the canary appears in the render if and only if the path is on a keep
// list. It is a fidelity check: it measures rule K against the overlay, so it
// stays green for any leak the overlay authorises. Round 2 measured what that
// costs. The build-time guard in internal/schema reads only the segments the
// ARTIFACT declares, so `prometheus.remote_write.external_labels` is one guarded
// segment while `external_labels.<whatever the user typed>` is two — and the
// second one reached the sandbox with nobody having looked at it. Thirteen kept
// paths in the shipped overlay are declared `map`. The same hole in the
// target_set class let `targets = [{__address__ = …, api_key = "…"}]` through,
// because the class forces two labels and copies the rest verbatim.
//
// Every spec below plants a credential at a key the USER chose rather than at a
// path the schema declares, and asserts it does not survive. They fail when the
// keep list is wrong, not when rule K disagrees with it.
//
// Red-proved in docs/proofs/transform-secret-drop.md: with rule K's
// constrainKeys removed the map sweep goes red naming every path, and with
// checkProvenance's mirror of it removed as well the run ships the credential.

// userKeyCanary is planted at a key the schema never declares. The key name is
// what the guard reads; the value is what a leak would carry.
const (
	userKeyCanary = "CANARY-USER-KEY-CREDENTIAL"
	userKeyName   = "api_key"
)

var _ = Describe("Transform: a credential at a user-chosen key does not reach the sandbox", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	// The sweep over every `map`-typed path the overlay keeps. Generated from
	// the shipped artifact and overlay rather than from a list, so a fourteenth
	// map attribute absorbed by a future keep entry — or by a keep_subtree
	// expansion after an Alloy bump — is covered the day it arrives.
	It("plants a credential-named key in every kept map path and finds it in no render", func() {
		var probed int
		var leaked, undisclosed, refused []string

		for _, p := range canaryPaths(payload) {
			if p.typ != "map" {
				continue
			}
			keep := policy.Components[p.component].Keep
			if !keep.Keeps(p.canon) {
				continue
			}
			probed++

			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: p.component, Label: "probe",
					Props: canaryProps(p.canon, map[string]interface{}{userKeyName: userKeyCanary}),
				}},
			}
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			if err != nil {
				// A refused run ships nothing, so it is not a LEAK — and it is
				// still a defect, which is why it is collected rather than
				// skipped. `external_labels = {"api_key" = "…"}` is an ordinary
				// settings map, and the fix for it is rule K dropping the one
				// entry with a disclosure, not the whole simulation failing.
				// Collecting it is also what makes this spec red-provable: with
				// constrainKeys removed, checkProvenance refuses every one of
				// these graphs, and a spec that skipped errors would report
				// success for a transform that had stopped guarding anything.
				refused = append(refused, p.String()+": "+err.Error())
				continue
			}

			content := visual.Render(result.Graph, payload).Content
			if strings.Contains(content, userKeyCanary) || strings.Contains(content, userKeyName) {
				leaked = append(leaked, p.String())
				continue
			}
			// §6.5: the user is told which of their settings went. A silent
			// drop here would make the simulated pipeline differ from the
			// authored one with no entry explaining why.
			if !droppedAt(result.Rewrites, append(p.canon, userKeyName)) {
				undisclosed = append(undisclosed, p.String())
			}
		}

		Expect(leaked).To(BeEmpty(),
			"%d kept map paths carried a credential at a user-chosen key into the sandbox config", len(leaked))
		Expect(refused).To(BeEmpty(),
			"%d kept map paths made the whole run fail rather than dropping one entry", len(refused))
		Expect(undisclosed).To(BeEmpty(),
			"%d kept map paths dropped the entry without telling the user", len(undisclosed))
		// Stated so the sweep cannot pass by probing nothing. Ten, not twelve:
		// prometheus.scrape's `params` left the keep list entirely (round-2),
		// and otelcol.exporter.splunkhec's two map paths
		// (splunk.telemetry.override_metrics_names/extra_attributes) went with
		// the rest of its keep list when it moved to the deliberately-
		// unmappable list — its required, secret splunk.token could never be
		// kept either way (availability fix, VB-1 §6.4).
		Expect(probed).To(Equal(10), "kept map paths probed")
	})

	// The same question of the target_set class, which is a second open key
	// space: it forces __address__ and __scheme__, drops the steering meta
	// labels, and used to copy every other label the user named verbatim.
	It("plants a credential-named label in every target_set path and finds it in no render", func() {
		var probed int
		var leaked, refused []string

		for _, p := range canaryPaths(payload) {
			class, kept := policy.Components[p.component].Keep.ClassOf(p.canon)
			if !kept || class != simulate.ClassTargetSet {
				continue
			}
			probed++

			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: p.component, Label: "probe",
					Props: canaryProps(p.canon, []interface{}{map[string]interface{}{
						// A label set that is otherwise entirely ordinary, so
						// the probe is about the one label and not about the
						// class refusing a malformed entry.
						"job": "probe", userKeyName: userKeyCanary,
					}}),
				}},
			}
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			if err != nil {
				// Collected, not skipped, for the same reason as the map sweep:
				// a label set with one odd label must lose the label, not the
				// run, and an error that went unrecorded would let this spec
				// pass while the class guarded nothing.
				refused = append(refused, p.String()+": "+err.Error())
				continue
			}

			content := visual.Render(result.Graph, payload).Content
			if strings.Contains(content, userKeyCanary) || strings.Contains(content, userKeyName) {
				leaked = append(leaked, p.String())
			}
		}

		Expect(leaked).To(BeEmpty(),
			"%d target sets carried a credential in a user-named label into the sandbox config", len(leaked))
		Expect(refused).To(BeEmpty(),
			"%d target sets made the whole run fail rather than dropping one label", len(refused))
		// Nine, not fourteen: blackbox and snmp no longer carry the class
		// (round-2), and database_observability.mysql/postgres/sql_server lost
		// it along with the rest of their disposition when the availability
		// fix made them sim_unsupported — their required data_source_name is a
		// secret no keep list can ever carry (VB-1 §6.4).
		Expect(probed).To(Equal(9), "target_set paths probed")
	})

	// The finding's own example, named so a regression says which mechanism came
	// back. `params` is how Prometheus has always carried a credential in a
	// scrape URL, and it sat on the keep list while `http_headers` — the same
	// mechanism in a different transport — was excluded for exactly that reason.
	It("no longer keeps prometheus.scrape params or metrics_path at all", func() {
		keep := policy.Components["prometheus.scrape"].Keep
		Expect(keep.Keeps([]string{"params"})).To(BeFalse(),
			"params is the canonical Prometheus query-string credential mechanism")
		Expect(keep.Keeps([]string{"metrics_path"})).To(BeFalse(),
			"metrics_path takes a query string and is the attribute form of the __metrics_path__ label target_set removes")

		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{{
				ID: "n1", Component: "prometheus.scrape", Label: "app",
				Props: map[string]interface{}{
					"targets":      []interface{}{map[string]interface{}{"job": "probe"}},
					"params":       map[string]interface{}{userKeyName: []interface{}{userKeyCanary}},
					"metrics_path": "/metrics?token=" + userKeyCanary,
					// A kept sibling, so the assertions below are about these
					// two attributes and not about the node being emptied.
					"job_name": "app",
				},
			}},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).NotTo(ContainSubstring(userKeyCanary), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring("params"), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring("metrics_path"), "rendered:\n%s", content)
		Expect(content).To(ContainSubstring(`job_name = "app"`), "rendered:\n%s", content)
		Expect(droppedAt(result.Rewrites, []string{"params"})).To(BeTrue(), "%v", result.Rewrites)
		Expect(droppedAt(result.Rewrites, []string{"metrics_path"})).To(BeTrue(), "%v", result.Rewrites)
	})

	// The probe exporters. Their `targets` looks like a Prometheus label set in
	// the artifact — both declare it `list` — and is not one: the destination
	// rides in an ordinary `address` key, so forcing __address__ and __scheme__
	// left it untouched. §6.5 refuses the alternative reading (drop the list and
	// report every probe down), so the run stops instead.
	DescribeTable("refuses a probe exporter whose destination is an ordinary address key",
		func(component string) {
			doc := visual.GraphDocument{
				Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
				Nodes: []visual.GraphNode{{
					ID: "n1", Component: component, Label: "probe",
					Props: map[string]interface{}{
						"targets": []interface{}{map[string]interface{}{
							"name": "vault", "address": outsideHost, "module": "http_2xx",
						}},
					},
				}},
			}
			_, err := simulate.Transform(simulate.TransformRequest{
				Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
			})
			errs := transformErrors(err)
			Expect(errorCodes(errs)).To(ConsistOf(simulate.CodeUnsupportedComponent))
			Expect(errs[0].Message).To(ContainSubstring(`"address" key`))
		},
		Entry("prometheus.exporter.blackbox", "prometheus.exporter.blackbox"),
		Entry("prometheus.exporter.snmp", "prometheus.exporter.snmp"),
	)

	// The node label is the one authored string that reaches the render without
	// passing rule K: the renderer writes it into the block header, and
	// checkProvenance cannot see it either, because a block label is not a
	// string literal in the parsed AST. sanitizeLabel mangles most credential
	// alphabets and this refuses the shapes it does not, which is the whole of
	// what is available — see the P1'' comment in transform.go for the residual
	// this deliberately accepts.
	It("refuses a credential-shaped node label, which the renderer writes into the block header", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{{
				ID: "n1", Component: "prometheus.scrape", Label: "ghp_0123456789abcdef",
			}},
		}
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		errs := transformErrors(err)
		Expect(errorCodes(errs)).To(ContainElement(simulate.CodeContainmentViolated))
		Expect(errs[0].Message).To(ContainSubstring("GitHub personal access token"))
	})

	// The counterpart, so the refusal above is about the shape and not about
	// labels in general: an ordinary node name still transforms.
	It("accepts an ordinary node label", func() {
		_, err := simulate.Transform(simulate.TransformRequest{
			Graph: singleNodeGraph("prometheus.scrape"), Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())
	})

	// A key the guard does not recognise still only survives because the PATH
	// was allowlisted, and that is the property the guard is allowed to lean on.
	// Asserted so nobody reads the sweeps above as a claim that rule K
	// recognises credentials.
	It("still keeps an ordinary user-chosen key, so the guard narrows an allowlist rather than being one", func() {
		doc := visual.GraphDocument{
			Kind: "alloy-graph/v1", SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{{
				ID: "n1", Component: "prometheus.remote_write", Label: "rw",
				Props: map[string]interface{}{
					"external_labels": map[string]interface{}{"cluster": "eu-1", userKeyName: userKeyCanary},
				},
			}},
		}
		result, err := simulate.Transform(simulate.TransformRequest{
			Graph: doc, Schema: payload, Policy: policy, Harness: testHarness(),
		})
		Expect(err).NotTo(HaveOccurred())

		content := visual.Render(result.Graph, payload).Content
		Expect(content).To(ContainSubstring(`cluster = "eu-1"`), "rendered:\n%s", content)
		Expect(content).NotTo(ContainSubstring(userKeyCanary), "rendered:\n%s", content)
	})
})

// droppedAt reports whether the disclosure list carries a prop_dropped entry for
// exactly this path.
func droppedAt(rewrites []simulate.Rewrite, path []string) bool {
	want := strings.Join(path, ".")
	for i := range rewrites {
		if rewrites[i].Kind == simulate.RewritePropDropped && strings.Join(rewrites[i].Path, ".") == want {
			return true
		}
	}
	return false
}
