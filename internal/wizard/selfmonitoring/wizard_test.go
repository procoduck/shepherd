package selfmonitoring_test

import (
	"errors"
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
)

func TestWizard(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Self Monitoring Wizard Suite")
}

var _ = Describe("SelfMonitoringWizard golden files", func() {
	wiz, getErr := wizard.Default().Get("self-monitoring")
	Expect(getErr).NotTo(HaveOccurred())

	DescribeTable("rendered output matches golden file, passes Stage 1, and is checked to role=singleton",
		func(fixtureName string, state map[string]any) {
			result, err := wiz.Commit(state)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Role).To(Equal("singleton"))

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
	)

	It("requires metrics_dest_name", func() {
		_, err := wiz.Commit(map[string]any{})
		Expect(err).To(HaveOccurred())
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
