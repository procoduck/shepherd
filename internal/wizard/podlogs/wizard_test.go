package podlogs_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/podlogs"
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Pod Logs Wizard Suite")
}

var _ = Describe("PodLogsWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("pod-logs")
	Expect(getErr).NotTo(HaveOccurred())

	DescribeTable("rendered output matches golden file, passes Stage 1, and is checked to role=logs",
		func(fixtureName string, state map[string]any) {
			result, err := wiz.Commit(state)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("logs"))

			goldenPath := "testdata/" + fixtureName + ".golden.alloy"
			goldenBytes, readErr := os.ReadFile(goldenPath)
			Expect(readErr).NotTo(HaveOccurred(), "golden file %s is missing — it must be committed", goldenPath)
			Expect(result.Contents).To(Equal(string(goldenBytes)),
				"output does not match golden file %s", goldenPath)

			r := validate.Stage1(string(goldenBytes))
			Expect(r.Valid).To(BeTrue(), "golden file %s fails Stage 1: %v", goldenPath, r.Diagnostics)
		},
		Entry("raw", "raw", map[string]any{
			"namespace_pattern": "prod-.*",
			"logs_dest_name":    "loki-prod",
			"cluster_pattern":   "prod-.*",
		}),
		Entry("json-format", "json-format", map[string]any{
			"namespace_pattern": "staging-.*",
			"log_format":        "json",
			"logs_dest_name":    "loki-staging",
			"cluster_pattern":   "staging-.*",
		}),
	)

	It("requires namespace_pattern", func() {
		_, err := wiz.Commit(map[string]any{"logs_dest_name": "loki-prod"})
		Expect(err).To(HaveOccurred())
	})

	It("requires logs_dest_name", func() {
		_, err := wiz.Commit(map[string]any{"namespace_pattern": "prod-.*"})
		Expect(err).To(HaveOccurred())
	})

	It("refuses an unrecognized log_format rather than emitting an unparseable stage block", func() {
		_, err := wiz.Commit(map[string]any{
			"namespace_pattern": "prod-.*",
			"logs_dest_name":    "loki-prod",
			"log_format":        "raw", // not a real loki.process stage name
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("log_format"))
	})
})
