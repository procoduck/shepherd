package database_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/database"
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Database Wizard Suite")
}

var _ = Describe("DatabaseWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("database")
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
		Entry("postgres", "postgres", map[string]any{
			"engine":            "postgres",
			"connection_env":    "APP_PG_DSN",
			"job_name":          "app-db",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
		}),
		Entry("mysql", "mysql", map[string]any{
			"engine":            "mysql",
			"connection_env":    "APP_MYSQL_DSN",
			"job_name":          "app-db",
			"scrape_interval":   "30s",
			"metrics_dest_name": "prom-staging",
			"cluster_pattern":   "staging-.*",
		}),
		Entry("redis", "redis", map[string]any{
			"engine":            "redis",
			"connection_env":    "APP_REDIS_ADDR",
			"job_name":          "app-cache",
			"scrape_interval":   "15s",
			"metrics_dest_name": "prom-prod",
			"cluster_pattern":   "prod-.*",
		}),
	)

	It("requires connection_env", func() {
		_, err := wiz.Commit(map[string]any{"engine": "postgres", "metrics_dest_name": "prom-prod"})
		Expect(err).To(HaveOccurred())
	})

	It("requires metrics_dest_name", func() {
		_, err := wiz.Commit(map[string]any{"engine": "postgres", "connection_env": "APP_PG_DSN"})
		Expect(err).To(HaveOccurred())
	})

	It("refuses an unsupported engine", func() {
		_, err := wiz.Commit(map[string]any{
			"engine": "mongodb", "connection_env": "APP_DSN", "metrics_dest_name": "prom-prod",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("engine"))
	})
})
