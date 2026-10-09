package selfmonitoring_test

import (
	"errors"
	"fmt"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/signals"
	"shepherd/internal/validate"
	"shepherd/internal/version"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/selfmonitoring"
	"shepherd/internal/wizard/wizardtest"
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Self Monitoring Wizard Suite")
}

var _ = Describe("SelfMonitoringWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("self-monitoring")
	Expect(getErr).NotTo(HaveOccurred())

	// The role follows what the pipeline carries when the form leaves it on
	// Auto (B6): metrics alone is checked to role=metrics, metrics and logs
	// to role=singleton.
	DescribeTable("rendered output matches golden file, passes Stage 1, and is checked to the role it carries",
		func(fixtureName string, state map[string]any) {
			result, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			wantRole := "singleton"
			if state["logs_enabled"] == false {
				wantRole = "metrics"
			}
			Expect(result.Role).To(Equal(wantRole))
			Expect(result.Matchers).To(ContainElement(fmt.Sprintf("role=%q", wantRole)))

			goldenPath := "testdata/" + fixtureName + ".golden.alloy"
			goldenBytes, readErr := os.ReadFile(goldenPath)
			Expect(readErr).NotTo(HaveOccurred(), "golden file %s is missing — it must be committed", goldenPath)
			Expect(result.Contents).To(Equal(string(goldenBytes)),
				"output does not match golden file %s", goldenPath)

			r := validate.Stage1(string(goldenBytes))
			Expect(r.Valid).To(BeTrue(), "golden file %s fails Stage 1: %v", goldenPath, r.Diagnostics)
		},
		Entry("metrics-only", "metrics-only", map[string]any{
			"job_name":          "alloy-self",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-prod",
			"logs_enabled":      false,
			"cluster_pattern":   "prod-.*",
		}),
		Entry("metrics-and-logs", "metrics-and-logs", map[string]any{
			"job_name":          "alloy-self",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-prod",
			"logs_enabled":      true,
			"log_path":          "/var/log/alloy/*.log",
			"logs_dest_name":    "loki-prod",
			"cluster_pattern":   "prod-.*",
		}),
		// Secret-mode destinations (#229) on both writers.
		Entry("secret-auth", "secret-auth", map[string]any{
			"job_name":          "alloy-self",
			"scrape_interval":   "60s",
			"metrics_dest_name": "prom-basic",
			"logs_enabled":      true,
			"log_path":          "/var/log/alloy/*.log",
			"logs_dest_name":    "loki-oauth",
			"cluster_pattern":   "prod-.*",
		}),
	)

	// B6 (2026-10-09 walkthrough): the wizard forced role="singleton" even
	// with log collection off, so it could not target a fleet split into
	// metrics and logs collectors. It now mirrors App Observability (#289):
	// an optional role field whose Auto choice follows the signals, an
	// explicit choice checked against them, and the role matcher always
	// emitted.
	Describe("collector role", func() {
		base := func() map[string]any {
			return map[string]any{
				"metrics_dest_name": "prom-prod", "logs_enabled": true,
				"log_path": "/var/log/alloy/*.log", "logs_dest_name": "loki-prod",
				"cluster_pattern": "prod-.*",
			}
		}

		It("left on Auto, is singleton when logs are collected", func() {
			result, err := wiz.Commit(base(), wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("singleton"))
			Expect(result.Matchers).To(Equal([]string{`cluster=~"prod-.*"`, `role="singleton"`}))
		})

		It("left on Auto, is metrics when log collection is off", func() {
			state := base()
			state["logs_enabled"] = false
			result, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("metrics"))
			Expect(result.Matchers).To(Equal([]string{`cluster=~"prod-.*"`, `role="metrics"`}))
		})

		It("left on Auto, is metrics when logs are asked for but no logs destination is named", func() {
			state := base()
			delete(state, "logs_dest_name")
			result, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Contents).NotTo(ContainSubstring("loki.source.file"))
			Expect(result.Role).To(Equal("metrics"))
		})

		It("keeps an explicit singleton with log collection off", func() {
			state := base()
			state["logs_enabled"] = false
			state["role"] = "singleton"
			result, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("singleton"))
			Expect(result.Matchers).To(ContainElement(`role="singleton"`))
		})

		It("refuses role=metrics with logs, naming the fix rather than the role check", func() {
			state := base()
			state["role"] = "metrics"
			_, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`pick the "singleton" role, or turn off log collection`))
			Expect(err.Error()).NotTo(ContainSubstring("does not match its declared role"))
		})

		It("refuses a role it does not offer, by name", func() {
			state := base()
			state["logs_enabled"] = false
			state["role"] = "logs"
			_, err := wiz.Commit(state, wizardtest.Destinations())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(`role "logs" is not offered`))
		})

		It("offers metrics and singleton with no static default, so the form shows Auto", func() {
			for _, step := range wiz.Schema().Steps {
				for _, f := range step.Fields {
					if f.Name == "role" {
						Expect(f.Required).To(BeFalse())
						Expect(f.Default).To(BeNil())
						Expect(f.Options).To(Equal([]string{"metrics", "singleton"}))
						return
					}
				}
			}
			Fail("no role field in the schema")
		})

		// Every offered role must be reachable by some valid state — the
		// dead-end-option rule appobservability's spec of the same name pins.
		It("commits at every role it offers", func() {
			for _, step := range wiz.Schema().Steps {
				for _, f := range step.Fields {
					if f.Name != "role" {
						continue
					}
					for _, role := range f.Options {
						state := base()
						state["logs_enabled"] = false
						state["role"] = role
						result, err := wiz.Commit(state, wizardtest.Destinations())
						Expect(err).NotTo(HaveOccurred(), "role option %q is offered but nothing commits at it", role)
						Expect(result.Role).To(Equal(role))
					}
					return
				}
			}
			Fail("no role field in the schema")
		})
	})

	It("requires metrics_dest_name", func() {
		_, err := wiz.Commit(map[string]any{}, wizardtest.Destinations())
		Expect(err).To(HaveOccurred())
	})

	// S4 (docs/archive/plans/2026-09-14-walkthrough-fixes.md, F7): the runner UI
	// only seeds a field's Default into wizard state (WizardRunnerPage.tsx),
	// never its Placeholder — log_path had a Placeholder but no Default, so
	// a state that left it blank silently dropped the whole log-collection
	// block even with logs_enabled true and a Loki destination named.
	It("tails logs at the default path when log_path is blank", func() {
		result, err := wiz.Commit(map[string]any{
			"metrics_dest_name": "prom-prod",
			"logs_enabled":      true,
			"logs_dest_name":    "loki-prod",
		}, wizardtest.Destinations())
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Role).To(Equal("singleton"))
		Expect(result.Contents).To(ContainSubstring("loki.source.file"))
		Expect(result.Contents).To(ContainSubstring("/var/log/alloy/*.log"))
		// B2: the defaulted path is a first-class warning, not a client guess.
		Expect(result.Warnings).To(ContainElement(ContainSubstring("default")))
	})

	// B2: logs asked for but no destination named — the block is silently
	// dropped, so the preview must say so as a warning rather than hide it.
	It("warns when logs are requested but no destination is set", func() {
		result, err := wiz.Commit(map[string]any{
			"metrics_dest_name": "prom-prod",
			"logs_enabled":      true,
		}, wizardtest.Destinations())
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Contents).NotTo(ContainSubstring("loki.source.file"), "logs are dropped with no destination")
		Expect(result.Warnings).To(ContainElement(ContainSubstring("no logs destination")))
	})

	It("emits no warnings when the form is complete", func() {
		result, err := wiz.Commit(map[string]any{
			"metrics_dest_name": "prom-prod",
			"logs_enabled":      true,
			"logs_dest_name":    "loki-prod",
			"log_path":          "/var/log/app/*.log",
		}, wizardtest.Destinations())
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Warnings).To(BeEmpty())
	})

	It("declares a default log path in the schema", func() {
		sc := wiz.Schema()
		var logPathField *wizard.StepField
		for si := range sc.Steps {
			for fi := range sc.Steps[si].Fields {
				if sc.Steps[si].Fields[fi].Name == "log_path" {
					logPathField = &sc.Steps[si].Fields[fi]
				}
			}
		}
		Expect(logPathField).NotTo(BeNil(), "expected a log_path field in the schema")
		Expect(logPathField.Default).To(Equal("/var/log/alloy/*.log"))
	})
})

// TestMixedSignalOutputRequiresSingleton is the concrete demonstration this
// package's doc comment promises: self-monitoring's own real
// "metrics-and-logs" golden — not a synthetic fixture — genuinely carries
// both Metrics and Logs (internal/signals.Derive proves it against the
// pinned schema artifact), so declaring this wizard's role "metrics" (or
// "logs") instead of "singleton" would be refused by exactly the mechanism
// wizard.Register applies to every wizard (internal/wizard/role.go). This is
// what makes role="singleton" a necessity for THIS wizard rather than an
// arbitrary choice: role="metrics" is not merely a worse label, it is
// unusable the moment log collection is enabled.
func TestMixedSignalOutputRequiresSingleton(t *testing.T) {
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		t.Fatalf("schema.New: %v", err)
	}

	mixed, err := os.ReadFile("testdata/metrics-and-logs.golden.alloy")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	sig, err := signals.Derive(string(mixed), reg)
	if err != nil {
		t.Fatalf("signals.Derive: %v", err)
	}
	if !sig.Proven() {
		t.Fatalf("signals.Derive could not prove the golden's signal set: unknown=%v unclassified=%v",
			sig.Unknown, sig.Unclassified)
	}
	if !sig.Has(signals.Metrics) || !sig.Has(signals.Logs) {
		t.Fatalf("expected the mixed golden to carry both Metrics and Logs, got %s", sig.Combined)
	}

	for _, badRole := range []string{"metrics", "logs"} {
		if err := signals.Enforce(badRole, sig.Combined); !errors.Is(err, signals.ErrSignalMismatch) {
			t.Errorf("signals.Enforce(%q, ...) = %v, want a signals.ErrSignalMismatch — "+
				"this wizard's own mixed-signal output must be refused under a restricted role", badRole, err)
		}
	}
	if err := signals.Enforce("singleton", sig.Combined); err != nil {
		t.Errorf("signals.Enforce(\"singleton\", ...) = %v, want nil — singleton is Unrestricted", err)
	}
}
