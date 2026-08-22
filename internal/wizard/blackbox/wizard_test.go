package blackbox_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/blackbox"
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Blackbox Wizard Suite")
}

var _ = Describe("BlackboxWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("blackbox")
	Expect(getErr).NotTo(HaveOccurred())

	DescribeTable("rendered output matches golden file, passes Stage 1, and is checked to role=metrics",
		func(fixtureName string, state map[string]any) {
			result, err := wiz.Commit(state)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("metrics"))

			goldenPath := "testdata/" + fixtureName + ".golden.alloy"
			goldenBytes, readErr := os.ReadFile(goldenPath)
			Expect(readErr).NotTo(HaveOccurred(), "golden file %s is missing — it must be committed", goldenPath)
			Expect(result.Contents).To(Equal(string(goldenBytes)),
				"output does not match golden file %s", goldenPath)

			r := validate.Stage1(string(goldenBytes))
			Expect(r.Valid).To(BeTrue(), "golden file %s fails Stage 1: %v", goldenPath, r.Diagnostics)
		},
		Entry("http", "http", map[string]any{
			"probe_targets":     "https://example.com,https://example.org",
			"module":            "http_2xx",
			"job_name":          "blackbox-http",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
		}),
		Entry("tcp", "tcp", map[string]any{
			"probe_targets":     "db.internal:5432",
			"module":            "tcp_connect",
			"job_name":          "blackbox-tcp",
			"scrape_interval":   "30s",
			"metrics_dest_name": "prom-staging",
			"cluster_pattern":   "staging-.*",
		}),
	)

	It("requires probe_targets", func() {
		_, err := wiz.Commit(map[string]any{"metrics_dest_name": "prom-prod"})
		Expect(err).To(HaveOccurred())
	})

	It("requires metrics_dest_name", func() {
		_, err := wiz.Commit(map[string]any{"probe_targets": "https://example.com"})
		Expect(err).To(HaveOccurred())
	})

	It("refuses an unsupported module", func() {
		_, err := wiz.Commit(map[string]any{
			"probe_targets": "https://example.com", "module": "icmp", "metrics_dest_name": "prom-prod",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("module"))
	})

	It("treats an all-punctuation target as an unnamed probe rather than an empty block name", func() {
		result, err := wiz.Commit(map[string]any{
			"probe_targets": "://", "metrics_dest_name": "prom-prod",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Contents).To(ContainSubstring(`name    = "probe-1"`))
	})
})
