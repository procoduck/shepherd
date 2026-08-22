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
			// role=singleton, not "metrics": this fixture's output genuinely
			// carries BOTH metrics and logs (scrape + loki blocks below), and
			// gate G6 (docs/gateway-tier-plan.md) now refuses that combination
			// for role=metrics — see wizard.Register's role check. singleton is
			// the policy row for pipelines that legitimately mix signal kinds
			// (internal/signals/policy.go); "metrics" here would have been the
			// exact silent role/signal mismatch the gate exists to catch.
			"role": "singleton",
		}),
	)
})

// A select field that offers a choice no valid state can commit is a dead end
// dressed as an option. Review of this wizard found exactly that: "logs" was
// offered as a collector role while Commit unconditionally emits the
// prometheus.scrape/remote_write block, so role enforcement (gate G6) refused
// every commit at that role. The refusal was correct — the dropdown was not.
//
// This spec generalizes the fix rather than pinning the one bad value: for
// EVERY role the schema offers, some valid state must commit successfully.
// Adding a new role option that Commit cannot satisfy fails here.
//
// Red run, executed: putting "logs" back into the role field's Options fails
// this spec with `role option "logs" is offered by the schema but no valid
// state commits at that role`.
var _ = Describe("every offered role is satisfiable", func() {
	It("commits successfully for each role option the schema advertises", func() {
		wiz, err := wizard.Default().Get("app-observability")
		Expect(err).NotTo(HaveOccurred())

		var roleField *wizard.StepField
		for _, step := range wiz.Schema().Steps {
			for i := range step.Fields {
				if step.Fields[i].Name == "role" {
					roleField = &step.Fields[i]
				}
			}
		}
		Expect(roleField).NotTo(BeNil(), "the role field disappeared — this spec would pass vacuously")
		Expect(roleField.Options).NotTo(BeEmpty())

		// The two shapes this wizard can produce: metrics alone, and metrics
		// plus logs. A role is satisfiable if either one commits at it.
		shapes := []map[string]any{
			{
				"scrape_url": "http://myapp:9090/metrics", "job_name": "myapp",
				"scrape_interval": "60s", "logs_enabled": false,
				"metrics_dest_name": "prom-prod", "cluster_pattern": "prod-.*",
			},
			{
				"scrape_url": "http://myapp:9090/metrics", "job_name": "myapp",
				"scrape_interval": "60s", "logs_enabled": true,
				"log_path": "/var/log/app/*.log", "log_format": "json",
				"metrics_dest_name": "prom-prod", "logs_dest_name": "loki-prod",
				"cluster_pattern": "prod-.*",
			},
		}

		for _, role := range roleField.Options {
			satisfiable := false
			var lastErr error
			for _, shape := range shapes {
				state := map[string]any{"role": role}
				for k, v := range shape {
					state[k] = v
				}
				if _, commitErr := wiz.Commit(state); commitErr == nil {
					satisfiable = true
					break
				} else {
					lastErr = commitErr
				}
			}
			Expect(satisfiable).To(BeTrue(),
				"role option %q is offered by the schema but no valid state commits at that role — "+
					"the last refusal was: %v", role, lastErr)
		}
	})
})
