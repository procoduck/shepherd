package clustermetrics_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/clustermetrics"
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cluster Metrics Wizard Suite")
}

var _ = Describe("ClusterMetricsWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("cluster-metrics")
	Expect(getErr).NotTo(HaveOccurred())

	DescribeTable("rendered output matches golden file, passes Stage 1, and is checked to role=metrics",
		func(fixtureName string, state map[string]any) {
			result, err := wiz.Commit(state)
			Expect(err).NotTo(HaveOccurred())
			// Went through wizard.Register's wrapper (wiz came from the
			// default registry, not a bare &clustermetrics.Wizard{}), so a
			// non-nil error here would already mean role enforcement
			// refused this fixture — this assertion is redundant with that,
			// but names the property this suite exists to protect.
			Expect(result.Role).To(Equal("metrics"))

			goldenPath := "testdata/" + fixtureName + ".golden.alloy"
			goldenBytes, readErr := os.ReadFile(goldenPath)
			Expect(readErr).NotTo(HaveOccurred(), "golden file %s is missing — it must be committed", goldenPath)
			Expect(result.Contents).To(Equal(string(goldenBytes)),
				"output does not match golden file %s", goldenPath)

			r := validate.Stage1(string(goldenBytes))
			Expect(r.Valid).To(BeTrue(), "golden file %s fails Stage 1: %v", goldenPath, r.Diagnostics)
		},
		Entry("basic", "basic", map[string]any{
			"job_name":          "cluster-metrics",
			"scrape_interval":   "60s",
			"docker_only":       false,
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
		}),
		Entry("docker-only", "docker-only", map[string]any{
			"job_name":          "docker-cluster",
			"scrape_interval":   "30s",
			"docker_only":       true,
			"metrics_dest_name": "prom-staging",
			"cluster_pattern":   "staging-.*",
		}),
	)

	It("requires metrics_dest_name", func() {
		_, err := wiz.Commit(map[string]any{})
		Expect(err).To(HaveOccurred())
	})
})
