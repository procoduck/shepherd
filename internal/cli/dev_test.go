package cli

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

func TestDevSeed(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Dev Seed Suite")
}

// allSeedPipelineItems returns every pipeline the dev seed creates, across
// both orgs.
func allSeedPipelineItems() []seedPipelineItem {
	items := append([]seedPipelineItem{}, platformPipelineItems()...)
	return append(items, dataEngPipelineItems()...)
}

// findSeedPipelineItem panics via Gomega if the named item isn't present —
// tests below assume it exists.
func findSeedPipelineItem(name string) seedPipelineItem {
	for _, item := range allSeedPipelineItems() {
		if item.name == name {
			return item
		}
	}
	Fail("seed pipeline " + name + " not found")
	return seedPipelineItem{}
}

var _ = Describe("seed pipeline contents", func() {
	It("pass stage-1 syntax validation, both raw and declare-wrapped as served", func() {
		for _, item := range allSeedPipelineItems() {
			raw := validate.Stage1(item.contents)
			Expect(raw.Valid).To(BeTrue(), "pipeline %q raw contents failed stage 1: %+v", item.name, raw.Diagnostics)

			wrapped := validate.WrapForValidation(item.name, item.contents)
			w := validate.Stage1(wrapped)
			Expect(w.Valid).To(BeTrue(), "pipeline %q declare-wrapped contents failed stage 1: %+v", item.name, w.Diagnostics)
		}
	})

	It("never seeds an enabled pipeline with empty matchers (R3-C1: empty matchers means match nothing)", func() {
		for _, item := range allSeedPipelineItems() {
			if item.enabled {
				Expect(item.matchers).NotTo(BeEmpty(), "enabled pipeline %q has no matchers and would match nothing", item.name)
			}
		}
	})

	It("matches base-metrics against the real alloy-metrics collector labels", func() {
		baseMetrics := findSeedPipelineItem("base-metrics")

		cl := merge.CollectorLabels{
			CollectorID: "prod-metrics-collector",
			Labels:      map[string]string{"cluster": seedClusterPlatformName, "role": "metrics"},
		}
		matched, err := merge.MatchesPipeline(merge.Pipeline{
			Name: baseMetrics.name, Matchers: baseMetrics.matchers, Source: baseMetrics.source,
		}, cl)
		Expect(err).NotTo(HaveOccurred())
		Expect(matched).To(BeTrue())
	})

	It("does not match base-metrics against an unrelated collector", func() {
		baseMetrics := findSeedPipelineItem("base-metrics")

		cl := merge.CollectorLabels{
			CollectorID: "staging-metrics-collector",
			Labels:      map[string]string{"cluster": seedClusterStagingName, "role": "metrics"},
		}
		matched, err := merge.MatchesPipeline(merge.Pipeline{
			Name: baseMetrics.name, Matchers: baseMetrics.matchers, Source: baseMetrics.source,
		}, cl)
		Expect(err).NotTo(HaveOccurred())
		Expect(matched).To(BeFalse())
	})

	It("assembles a non-empty, stage-1-valid served config for the seeded prod metrics collector", func() {
		var mergePipelines []merge.Pipeline
		for _, item := range platformPipelineItems() {
			if !item.enabled {
				continue
			}
			mergePipelines = append(mergePipelines, merge.Pipeline{
				ID: item.name, Name: item.name, Contents: item.contents,
				Matchers: item.matchers, Source: item.source,
			})
		}

		cl := merge.CollectorLabels{
			CollectorID: "prod-metrics-collector",
			Labels:      map[string]string{"cluster": seedClusterPlatformName, "role": "metrics"},
		}
		result, err := merge.Assemble("prod-metrics-collector", "prod-eu-1/metrics", cl, mergePipelines, "test", "2024-01-01T00:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Content).To(ContainSubstring("prometheus.remote_write"))
		Expect(result.Content).NotTo(ContainSubstring("No pipelines matched"))

		stage1 := validate.Stage1(result.Content)
		Expect(stage1.Valid).To(BeTrue(), "assembled config failed stage 1: %+v", stage1.Diagnostics)
	})

	It("seeds a visual-source demo pipeline with a valid alloy-graph/v1 wizard_state (D1/R3-H5)", func() {
		demo := findSeedPipelineItem("demo-visual")
		Expect(demo.source).To(Equal("visual"))
		Expect(demo.matchers).NotTo(BeEmpty())
		Expect(demo.wizardState).NotTo(BeEmpty())

		// Mirrors the stage-1 check above: the demo graph is valid JSON,
		// its "kind" is alloy-graph/v1, and its rendered contents (already
		// covered by the raw/declare-wrapped loop above) pass stage 1.
		var doc struct {
			Kind  string `json:"kind"`
			Nodes []struct {
				ID        string `json:"id"`
				Component string `json:"component"`
			} `json:"nodes"`
			Edges []struct {
				ID string `json:"id"`
			} `json:"edges"`
		}
		Expect(json.Unmarshal([]byte(demo.wizardState), &doc)).To(Succeed(), "demo-visual wizard_state must be valid JSON")
		Expect(doc.Kind).To(Equal("alloy-graph/v1"))
		Expect(doc.Nodes).NotTo(BeEmpty())
		Expect(doc.Edges).NotTo(BeEmpty())

		stage1 := validate.Stage1(demo.contents)
		Expect(stage1.Valid).To(BeTrue(), "demo-visual contents failed stage 1: %+v", stage1.Diagnostics)
	})
})

var _ = Describe("demoVisualGraph", func() {
	// Regression: the visual canvas resolves an edge by matching its port name
	// against a handle built from the schema's prop/export. A port name that does
	// not exist on the component is not an error anywhere — React Flow simply drops
	// the edge and L1 reports the node as unwired — so a graph can look saved and
	// valid while rendering with no connections at all. This asserts every seeded
	// edge references a port the served schema actually declares.
	It("references only ports that exist in the schema artifact", func() {
		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		merged, _, err := reg.Get(reg.CurrentVersion())
		Expect(err).NotTo(HaveOccurred())

		components, ok := merged["components"].(map[string]any)
		Expect(ok).To(BeTrue(), "schema payload must carry components")

		var graph struct {
			Nodes []struct {
				ID        string `json:"id"`
				Component string `json:"component"`
			} `json:"nodes"`
			Edges []struct {
				From struct {
					Node string `json:"node"`
					Port string `json:"port"`
				} `json:"from"`
				To struct {
					Node string `json:"node"`
					Port string `json:"port"`
				} `json:"to"`
			} `json:"edges"`
		}
		Expect(json.Unmarshal([]byte(demoVisualGraph), &graph)).To(Succeed())
		Expect(graph.Edges).NotTo(BeEmpty())

		componentOf := map[string]string{}
		for _, n := range graph.Nodes {
			componentOf[n.ID] = n.Component
		}

		// portRole returns the D1 role the artifact declares for a port, looked up
		// across both lists. Which list a port lives in is an Alloy implementation
		// detail (Arguments vs Exports struct) and NOT the dataflow direction:
		// prometheus.remote_write's "receiver" is an export that data flows INTO
		// (role "accepts"), and prometheus.scrape's "forward_to" is an argument
		// that data flows OUT of (role "produces"). The canvas always draws
		// source -> destination, so the check is on the role, not on the list.
		portRole := func(componentName, port string) (string, bool) {
			def, found := components[componentName].(map[string]any)
			Expect(found).To(BeTrue(), "component %q must exist in the schema", componentName)
			for _, side := range []struct{ list, key string }{{"inputs", "prop"}, {"outputs", "export"}} {
				raw, ok := def[side.list].([]any)
				if !ok {
					continue
				}
				for _, p := range raw {
					decl, isMap := p.(map[string]any)
					if !isMap {
						continue
					}
					name, isStr := decl[side.key].(string)
					if !isStr || name != port {
						continue
					}
					role, _ := decl["role"].(string) //nolint:errcheck // an absent role fails the assertion below, which is the point
					return role, true
				}
			}
			return "", false
		}

		for _, e := range graph.Edges {
			fromComp := componentOf[e.From.Node]
			toComp := componentOf[e.To.Node]
			Expect(fromComp).NotTo(BeEmpty())
			Expect(toComp).NotTo(BeEmpty())

			fromRole, fromFound := portRole(fromComp, e.From.Port)
			Expect(fromFound).To(BeTrue(),
				"edge source %s.%s: %q declares no such port", e.From.Node, e.From.Port, fromComp)
			Expect(fromRole).To(Equal("produces"),
				"edge source %s.%s: %q has role %q; data must leave the source", e.From.Node, e.From.Port, fromComp, fromRole)

			toRole, toFound := portRole(toComp, e.To.Port)
			Expect(toFound).To(BeTrue(),
				"edge target %s.%s: %q declares no such port", e.To.Node, e.To.Port, toComp)
			Expect(toRole).To(Equal("accepts"),
				"edge target %s.%s: %q has role %q; data must enter the destination", e.To.Node, e.To.Port, toComp, toRole)
		}
	})
})
