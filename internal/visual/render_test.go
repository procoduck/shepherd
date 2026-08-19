package visual_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
	"shepherd/internal/version"
	"shepherd/internal/visual"
)

func TestVisual(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Visual Suite")
}

// corpusSchema is the schema the product actually serves: the embedded
// artifact for the pinned Alloy version, deep-merged with the same overlay the
// HTTP handler merges, decoded into the renderer's payload type.
//
// It is deliberately NOT a hand-written fixture. Until 2026-08-19 this file
// declared twelve components whose port model was the mirror image of the
// shipped artifact's (prometheus.scrape "exported" metrics; prometheus.remote_write
// "accepted" a receiver). Every corpus golden matched, and none of them was
// config Alloy would accept: corrupting a port name inside the committed
// artifact left this package green. Loading the real payload is what makes the
// goldens below evidence rather than decoration.
// It panics rather than asserting so that the golden regenerator
// (gen_goldens_test.go), which runs outside Ginkgo, can call it too.
func corpusSchema() visual.SchemaPayload {
	payload, err := loadShippedSchema()
	if err != nil {
		panic("loading the shipped schema artifact: " + err.Error())
	}
	return payload
}

var (
	shippedOnce    sync.Once
	shippedPayload visual.SchemaPayload
	shippedRaw     map[string]any
	shippedErr     error
)

func loadShippedSchema() (visual.SchemaPayload, error) {
	shippedOnce.Do(func() {
		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		if err != nil {
			shippedErr = err
			return
		}
		merged, _, err := reg.Get(reg.CurrentVersion())
		if err != nil {
			shippedErr = err
			return
		}
		raw, err := json.Marshal(merged)
		if err != nil {
			shippedErr = err
			return
		}
		shippedRaw = merged
		shippedErr = json.Unmarshal(raw, &shippedPayload)
	})
	return shippedPayload, shippedErr
}

// shippedComponents is the merged payload as raw JSON maps, for assertions that
// need to see exactly what the server serves rather than what the renderer's
// struct captures.
func shippedComponents() map[string]any {
	GinkgoHelper()
	_, err := loadShippedSchema()
	Expect(err).NotTo(HaveOccurred())
	comps, ok := shippedRaw["components"].(map[string]any)
	Expect(ok).To(BeTrue(), "schema payload must carry a components map")
	return comps
}

func readCorpus(name string) ([]byte, []byte) {
	GinkgoHelper()
	root := filepath.Join("testdata", "corpus")
	graph, err := os.ReadFile(filepath.Join(root, name+".graph.json"))
	Expect(err).NotTo(HaveOccurred(), "reading %s.graph.json", name)
	golden, err := os.ReadFile(filepath.Join(root, name+".golden.alloy"))
	Expect(err).NotTo(HaveOccurred(), "reading %s.golden.alloy", name)
	return graph, golden
}

func unmarshalDoc(data []byte) visual.GraphDocument {
	GinkgoHelper()
	var doc visual.GraphDocument
	Expect(json.Unmarshal(data, &doc)).To(Succeed())
	return doc
}

var corpusNames = []string{
	"minimal-scrape",
	"fanin-fanout",
	"nested-blocks",
	"bindings-secret",
	"logs-chain",
	"disabled-node",
	"label-edgecases",
	"otel-three-signals",
	"kitchen-sink",
}

var _ = Describe("Renderer", func() {
	schema := corpusSchema()

	// 7.2.1 — corpus renders byte-exact
	// RED-RUN PROOF: changing attribute emission order in render.go (e.g. emitting in props-map
	// order instead of schema order) causes all corpus entries with props to fail with
	// "Expected '...' to equal '...'" — the golden files become mismatched. Since the schema is
	// now the shipped artifact, renaming a port inside it (e.g. prometheus.remote_write's
	// "receiver" export) also fails here, as an edge_unresolved diagnostic.
	DescribeTable("7.2.1 corpus renders byte-exact",
		func(name string) {
			graphJSON, golden := readCorpus(name)
			doc := unmarshalDoc(graphJSON)
			result := visual.Render(doc, schema)
			Expect(result.Diagnostics).To(BeEmpty(), "unexpected diagnostics for %s", name)
			Expect(result.Content).To(Equal(string(golden)), "output mismatch for %s", name)
		},
		Entry("minimal-scrape", "minimal-scrape"),
		Entry("fanin-fanout", "fanin-fanout"),
		Entry("nested-blocks", "nested-blocks"),
		Entry("bindings-secret", "bindings-secret"),
		Entry("logs-chain", "logs-chain"),
		Entry("disabled-node", "disabled-node"),
		Entry("label-edgecases", "label-edgecases"),
		Entry("otel-three-signals", "otel-three-signals"),
		Entry("kitchen-sink", "kitchen-sink"),
	)

	// 7.2.2 — render is permutation-invariant
	// RED-RUN PROOF: removing the topological sort tie-break (the category/component/label
	// secondary sort in the Kahn algorithm) causes kitchen-sink to produce different output
	// on different node-array orderings — the DescribeTable entry "kitchen-sink" fails.
	DescribeTable("7.2.2 render is permutation-invariant",
		func(name string) {
			graphJSON, golden := readCorpus(name)
			doc := unmarshalDoc(graphJSON)
			rng := rand.New(rand.NewSource(42))
			for i := range 5 {
				// Shuffle nodes and edges
				shuffled := doc
				nodes := make([]visual.GraphNode, len(doc.Nodes))
				copy(nodes, doc.Nodes)
				edges := make([]visual.GraphEdge, len(doc.Edges))
				copy(edges, doc.Edges)
				rng.Shuffle(len(nodes), func(a, b int) { nodes[a], nodes[b] = nodes[b], nodes[a] })
				rng.Shuffle(len(edges), func(a, b int) { edges[a], edges[b] = edges[b], edges[a] })
				shuffled.Nodes = nodes
				shuffled.Edges = edges
				result := visual.Render(shuffled, schema)
				Expect(result.Content).To(Equal(string(golden)),
					"permutation %d of %s produced different output", i, name)
			}
		},
		Entry("minimal-scrape", "minimal-scrape"),
		Entry("fanin-fanout", "fanin-fanout"),
		Entry("kitchen-sink", "kitchen-sink"),
	)

	// 7.2.3 — node_map is complete and correct
	It("7.2.3 node_map is complete and correct for all corpus entries", func() {
		for _, name := range corpusNames {
			graphJSON, _ := readCorpus(name)
			doc := unmarshalDoc(graphJSON)
			result := visual.Render(doc, schema)
			Expect(result.Diagnostics).To(BeEmpty(), "corpus entry %s must render cleanly", name)

			lines := splitLines(result.Content)

			// Count expected non-disabled nodes
			activeCount := 0
			for _, n := range doc.Nodes {
				if !n.Disabled {
					activeCount++
				}
			}
			Expect(result.NodeMap).To(HaveLen(activeCount),
				"node_map should have exactly one entry per non-disabled node in %s", name)

			// Every entry has valid, non-overlapping ranges covering the component block
			seen := map[[2]int]string{}
			for id, r := range result.NodeMap {
				Expect(r.StartLine).To(BeNumerically("<=", r.EndLine),
					"node %s in %s: start > end", id, name)
				Expect(r.StartLine).To(BeNumerically(">=", 1),
					"node %s in %s: start < 1", id, name)
				Expect(r.EndLine).To(BeNumerically("<=", len(lines)),
					"node %s in %s: end > line count", id, name)

				key := [2]int{r.StartLine, r.EndLine}
				Expect(seen).NotTo(HaveKey(key),
					"node_map ranges overlap: %s and %s both map to lines %d-%d in %s",
					id, seen[key], r.StartLine, r.EndLine, name)
				seen[key] = id

				// The start line must contain the component name
				if r.StartLine <= len(lines) {
					Expect(lines[r.StartLine-1]).To(ContainSubstring(nodeComponent(doc, id)),
						"line %d of %s does not contain component name for node %s",
						r.StartLine, name, id)
				}
			}
		}
	})

	// 7.2.4 — fan-in preserves edge order
	It("7.2.4 fan-in preserves edge order", func() {
		graphJSON, _ := readCorpus("fanin-fanout")
		doc := unmarshalDoc(graphJSON)

		// Original order: first(0), second(1) → targets = [first.output, second.output]
		result := visual.Render(doc, schema)
		Expect(result.Content).To(ContainSubstring("discovery.relabel.first.output, discovery.relabel.second.output"))

		// Swap orders on the edges going into prometheus.scrape targets
		swapped := doc
		edges := make([]visual.GraphEdge, len(doc.Edges))
		copy(edges, doc.Edges)
		for i := range edges {
			if edges[i].To.Port == "targets" {
				if edges[i].Order != nil && *edges[i].Order == 0 {
					o := 1
					edges[i].Order = &o
				} else if edges[i].Order != nil && *edges[i].Order == 1 {
					o := 0
					edges[i].Order = &o
				}
			}
		}
		swapped.Edges = edges
		swappedResult := visual.Render(swapped, schema)
		Expect(swappedResult.Content).To(ContainSubstring("discovery.relabel.second.output, discovery.relabel.first.output"))
	})

	// 7.2.5 — sanitize collision is an error, never auto-suffixed
	// RED-RUN PROOF: adding auto-suffix logic (e.g. appending "_2" to the second node's label)
	// causes this test to fail — render succeeds instead of returning label_collision.
	It("7.2.5 sanitize collision is error, never auto-suffixed", func() {
		doc := visual.GraphDocument{
			SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{ID: "a", Component: "prometheus.remote_write", Label: "my-sink", Disabled: false},
				{ID: "b", Component: "prometheus.remote_write", Label: "my_sink", Disabled: false},
			},
		}
		result := visual.Render(doc, schema)
		Expect(result.Content).To(BeEmpty(), "content must be empty on collision")
		Expect(result.Diagnostics).To(HaveLen(1))
		Expect(result.Diagnostics[0].Code).To(Equal("label_collision"))
		// Both node IDs must be named
		Expect([]string{result.Diagnostics[0].NodeID, result.Diagnostics[0].NodeID2}).To(
			ConsistOf("a", "b"))
	})

	// 7.2.6 — disabled node emission
	It("7.2.6 disabled node is skipped in output but leaves a comment", func() {
		graphJSON, _ := readCorpus("disabled-node")
		doc := unmarshalDoc(graphJSON)
		result := visual.Render(doc, schema)
		Expect(result.Diagnostics).To(BeEmpty())
		// Disabled node appears as a comment
		Expect(result.Content).To(ContainSubstring(`// node "app" disabled`))
		// Disabled node block is NOT emitted
		Expect(result.Content).NotTo(ContainSubstring(`prometheus.scrape "app" {`))
		// Other nodes ARE emitted
		Expect(result.Content).To(ContainSubstring(`prometheus.remote_write "sink" {`))
		// render is total — succeeds even though remote_write has no incoming wires
		Expect(result.Content).NotTo(BeEmpty())
	})

	// 7.2.7 — secret-typed prop with a literal value is refused
	// RED-RUN PROOF: removing the secret-by-value check from render.go causes this test to
	// fail — render succeeds and emits the plaintext secret instead of an error.
	It("7.2.7 secret-typed literal prop returns secret_by_value error", func() {
		doc := visual.GraphDocument{
			SchemaVersion: "alloy-v1.18.1",
			Nodes: []visual.GraphNode{
				{
					ID:        "s1",
					Component: "prometheus.scrape",
					Label:     "app",
					Disabled:  false,
					Props:     map[string]interface{}{"bearer_token": "plaintext-secret"},
				},
			},
		}
		result := visual.Render(doc, schema)
		Expect(result.Diagnostics).NotTo(BeEmpty(), "expected secret_by_value diagnostic")
		Expect(result.Diagnostics[0].Code).To(Equal("secret_by_value"))
		Expect(result.Diagnostics[0].NodeID).To(Equal("s1"))
	})

	It("7.2.9 unknown component produces diagnostic but still renders", func() {
		doc := visual.GraphDocument{SchemaVersion: "alloy-v1.18.1", Nodes: []visual.GraphNode{{ID: "u1", Component: "nonexistent.component", Label: "test"}}}
		result := visual.Render(doc, schema)
		Expect(result.Diagnostics).To(ContainElement(HaveField("Code", "unknown_component")))
		Expect(result.Content).NotTo(BeEmpty())
	})
})

// ---------------------------------------------------------------------------
// The corpus is only evidence if it is anchored to the shipped artifact on one
// side and to the real Alloy binary on the other. The two suites below are
// those anchors.
// ---------------------------------------------------------------------------

var _ = Describe("Corpus ↔ shipped artifact", func() {
	// Modelled on internal/cli/dev_test.go's seed guard. A port name that the
	// artifact does not declare is not an error anywhere in the canvas — React
	// Flow simply never creates the edge, and the renderer reports
	// edge_unresolved — so a corpus entry can look wired while rendering with no
	// connections at all. This asserts every corpus edge names a port the served
	// schema declares, on the side and with the D1 role the renderer requires.
	It("every corpus edge names a declared port with a resolvable role pair", func() {
		components := shippedComponents()

		for _, name := range corpusNames {
			graphJSON, _ := readCorpus(name)
			doc := unmarshalDoc(graphJSON)

			componentOf := map[string]string{}
			for _, n := range doc.Nodes {
				componentOf[n.ID] = n.Component
			}

			Expect(doc.Edges).NotTo(BeEmpty(), "%s: a corpus entry with no edges proves nothing about wiring", name)
			for _, e := range doc.Edges {
				fromComp, toComp := componentOf[e.From.Node], componentOf[e.To.Node]
				Expect(fromComp).NotTo(BeEmpty(), "%s: edge %s has no source node", name, e.ID)
				Expect(toComp).NotTo(BeEmpty(), "%s: edge %s has no target node", name, e.ID)

				from := lookupPort(components, fromComp, e.From.Port)
				to := lookupPort(components, toComp, e.To.Port)
				Expect(from).NotTo(BeNil(), "%s: edge %s: %s declares no port %q", name, e.ID, fromComp, e.From.Port)
				Expect(to).NotTo(BeNil(), "%s: edge %s: %s declares no port %q", name, e.ID, toComp, e.To.Port)

				// D1: the canvas always points source → destination. Data leaves
				// the source through a "produces" port and enters the
				// destination through an "accepts" port, whichever of the two is
				// the argument and whichever is the export.
				Expect(from.role).To(Equal("produces"),
					"%s: edge %s: %s.%s has role %q; a source port must produce", name, e.ID, fromComp, e.From.Port, from.role)
				Expect(to.role).To(Equal("accepts"),
					"%s: edge %s: %s.%s has role %q; a destination port must accept", name, e.ID, toComp, e.To.Port, to.role)
				Expect(wireTypesCompatible(from.wireType, to.wireType)).To(BeTrue(),
					"%s: edge %s: %s.%s is %s but %s.%s is %s", name, e.ID,
					fromComp, e.From.Port, from.wireType, toComp, e.To.Port, to.wireType)
			}
		}
	})

	It("every corpus node names a component the artifact declares", func() {
		components := shippedComponents()
		for _, name := range corpusNames {
			graphJSON, _ := readCorpus(name)
			for _, n := range unmarshalDoc(graphJSON).Nodes {
				Expect(components).To(HaveKey(n.Component), "%s: node %s", name, n.ID)
			}
		}
	})
})

var _ = Describe("Corpus goldens are real Alloy", func() {
	// Stage 1 is in-process (the Alloy syntax parser is a library), so it always
	// runs. Two of the nine goldens used to fail it outright.
	It("every golden parses (validate stage 1), raw and declare-wrapped", func() {
		for _, name := range corpusNames {
			_, golden := readCorpus(name)
			r := validate.Stage1(string(golden))
			Expect(r.Valid).To(BeTrue(), "%s.golden.alloy failed stage 1: %+v", name, r.Diagnostics)

			wrapped := validate.WrapForValidation("corpus-"+name, string(golden))
			rw := validate.Stage1(wrapped)
			Expect(rw.Valid).To(BeTrue(), "declare-wrapped %s failed stage 1: %+v", name, rw.Diagnostics)
		}
	})

	// Stage 2 shells the pinned alloy binary. This is the check that catches a
	// golden which parses but names an export Alloy does not have — the failure
	// mode that let `prometheus.relabel "x" { forward_to = [prometheus.scrape.app.metrics] }`
	// sit in the committed corpus. It runs against a local binary when there is
	// one and otherwise through the pinned container image, so CI (which has
	// Docker) exercises it.
	It("every golden is accepted by the pinned alloy binary (validate stage 2)", func() {
		binary := findAlloyBinary()
		if binary == "" {
			// This is the only thing in the repo that proves renderer output is
			// real Alloy, so its absence must be loud: it fails by default. A
			// network blip or a missing daemon should not silently disable the
			// gate — only an explicit, deliberate opt-out may.
			if os.Getenv("SHEPHERD_SKIP_ALLOY_VALIDATE") != "" {
				Skip("SHEPHERD_SKIP_ALLOY_VALIDATE set: skipping without an alloy binary or usable docker image " + alloyImage())
			}
			Fail("no alloy binary and no usable docker image " + alloyImage() + " to run stage 2 validate; " +
				"install/start docker (or `docker pull " + alloyImage() + "`), set SHEPHERD_TEST_ALLOY_BINARY to a local " +
				"alloy binary, or set SHEPHERD_SKIP_ALLOY_VALIDATE=1 to explicitly opt out")
		}
		v := validate.New(&config.ValidateConfig{
			AlloyBinary:    binary,
			StabilityLevel: "experimental",
			Timeout:        2 * time.Minute,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()

		for _, name := range corpusNames {
			_, golden := readCorpus(name)
			// Validate exactly what the fleet is served: the declare-wrapped form.
			wrapped := validate.WrapForValidation("corpus-"+name, string(golden))
			r := v.Stages12(ctx, wrapped)
			Expect(r.Valid).To(BeTrue(), "%s.golden.alloy rejected by alloy validate: %+v", name, r.Diagnostics)
		}
	})
})

// portInfo is one declared port, flattened across the artifact's inputs and
// outputs lists — under D1 the interesting axis is the role, not which struct
// the field came from.
type portInfo struct {
	role     string
	wireType string
}

// wireTypesCompatible mirrors web/src/visual/l1.ts portsCompatible: identical
// types connect, and otel.any (the type every otelcol consumer declares) is
// compatible with any other otel.* signal.
func wireTypesCompatible(from, to string) bool {
	if from == to {
		return from != ""
	}
	otel := func(t string) bool { return strings.HasPrefix(t, "otel.") }
	return (from == "otel.any" || to == "otel.any") && otel(from) && otel(to)
}

func lookupPort(components map[string]any, component, port string) *portInfo {
	def, ok := components[component].(map[string]any)
	if !ok {
		return nil
	}
	for _, side := range []struct{ list, key string }{{"inputs", "prop"}, {"outputs", "export"}} {
		raw, ok := def[side.list].([]any)
		if !ok {
			continue
		}
		for _, entry := range raw {
			p, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			name, _ := p[side.key].(string) //nolint:errcheck // absent name means an unnamed port, which never matches
			if name != port {
				continue
			}
			role, _ := p["role"].(string) //nolint:errcheck // an absent role fails the assertion, which is the point
			wire, _ := p["type"].(string) //nolint:errcheck // same
			return &portInfo{role: role, wireType: wire}
		}
	}
	return nil
}

// alloyImage is the container image for the pinned schema version:
// version.AlloySchemaVersion is "alloy-v1.18.1", the image tag is "v1.18.1".
func alloyImage() string {
	return "grafana/alloy:" + strings.TrimPrefix(version.AlloySchemaVersion, "alloy-")
}

var (
	alloyBinaryOnce sync.Once
	alloyBinaryPath string
)

// findAlloyBinary returns a path that behaves like `alloy validate ... <file>`,
// or "" when neither a binary nor Docker is available. The docker fallback is a
// three-line shim so that internal/validate's real Stage2 code path — temp
// file, exec, output parsing — is what runs, rather than a test-only imitation.
func findAlloyBinary() string {
	alloyBinaryOnce.Do(func() {
		if env := os.Getenv("SHEPHERD_TEST_ALLOY_BINARY"); env != "" {
			alloyBinaryPath = env
			return
		}
		for _, candidate := range []string{"alloy", "/usr/local/bin/alloy"} {
			if p, err := exec.LookPath(candidate); err == nil {
				alloyBinaryPath = p
				return
			}
		}
		alloyBinaryPath = dockerAlloyShim()
	})
	return alloyBinaryPath
}

func dockerAlloyShim() string {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return ""
	}
	image := alloyImage()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, docker, "image", "inspect", image).Run(); err != nil {
		if err := exec.CommandContext(ctx, docker, "pull", "--quiet", image).Run(); err != nil {
			return ""
		}
	}

	dir, err := os.MkdirTemp("", "shepherd-alloy-shim-")
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "alloy")
	// The last argument is always the config path (internal/validate builds the
	// command); mount its directory read-only and run validate inside the image.
	script := fmt.Sprintf(`#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
dir=$(cd "$(dirname "$last")" && pwd -P)
exec %q run --rm -v "$dir:/work:ro" %s validate --stability.level=experimental "/work/$(basename "$last")"
`, docker, image)
	// 0o700: the file has to be executable — internal/validate exec's it.
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return ""
	}
	return path
}

// splitLines splits content into 1-based lines.
func splitLines(content string) []string {
	lines := []string{}
	cur := ""
	for _, ch := range content {
		if ch == '\n' {
			lines = append(lines, cur)
			cur = ""
		} else {
			cur += string(ch)
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// nodeComponent returns the component of a node by ID.
func nodeComponent(doc visual.GraphDocument, id string) string {
	for _, n := range doc.Nodes {
		if n.ID == id {
			return n.Component
		}
	}
	return ""
}
