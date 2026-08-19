package cli

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
	"shepherd/internal/validate"
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
