package appobservability_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/appobservability"
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "App Observability Wizard Suite")
}

var _ = Describe("AppObservabilityWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("app-observability")
	Expect(getErr).NotTo(HaveOccurred())

	DescribeTable("rendered output matches golden file and passes Stage 1",
		func(fixtureName string, state map[string]any) {
			result, err := wiz.Commit(state)
			Expect(err).NotTo(HaveOccurred())

			goldenPath := "testdata/" + fixtureName + ".golden.alloy"
			goldenBytes, readErr := os.ReadFile(goldenPath)
			// A missing golden must fail, never self-heal: writing current output
			// as the baseline would make the comparison prove nothing. Regenerate
			// deliberately by updating the committed file.
			Expect(readErr).NotTo(HaveOccurred(), "golden file %s is missing — it must be committed", goldenPath)
			Expect(result.Contents).To(Equal(string(goldenBytes)),
				"output does not match golden file %s", goldenPath)

			// Golden itself must pass Stage 1 syntax check.
			r := validate.Stage1(string(goldenBytes))
			Expect(r.Valid).To(BeTrue(), "golden file %s fails Stage 1: %v", goldenPath, r.Diagnostics)
		},
		Entry("metrics-only", "metrics-only", map[string]any{
			"scrape_url":        "http://myapp:9090/metrics",
			"job_name":          "myapp",
			"scrape_interval":   "60s",
			"logs_enabled":      false,
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
			"role":              "metrics",
		}),
		Entry("metrics-and-logs", "metrics-and-logs", map[string]any{
			"scrape_url":        "http://app:9090/metrics",
			"job_name":          "app",
			"scrape_interval":   "30s",
			"logs_enabled":      true,
			"log_path":          "/var/log/app/*.log",
			"log_format":        "json",
			"metrics_dest_name": "prom-prod",
			"logs_dest_name":    "loki-prod",
			"cluster_pattern":   "prod-.*",
			"role":              "metrics",
		}),
	)
})
