package validate_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/validate"
	"shepherd/internal/wizard/wizardtest"
)

var _ = Describe("what strict tenant mode accepts for capture-group labels, secretfilter and podlogs", func() {
	It("is valid Alloy under the real alloy validate (v1.21.2)", func() {
		bin := wizardtest.AlloyBinary()
		if bin == "" {
			Skip("no alloy binary and no docker")
		}
		real := validate.New(&config.ValidateConfig{AlloyBinary: bin, StabilityLevel: "experimental", Timeout: 2 * time.Minute})
		src := "loki.process \"p\" {\n  forward_to = []\n  stage.regex {\n    expression         = `^(?P<level>\\w+) (?P<msg>.*)$`\n" +
			"    labels_from_groups = true\n  }\n  stage.pattern {\n    pattern            = \"<level> <_> <msg>\"\n" +
			"    labels_from_groups = true\n  }\n}\n" +
			"loki.secretfilter \"s\" {\n  forward_to   = []\n  origin_label = \"job\"\n}\n" +
			"loki.source.podlogs \"pl\" {\n  forward_to                 = []\n  preserve_discovered_labels = true\n}\n"
		Expect(validate.TenantLint(src, strictFor())).To(BeEmpty())
		r := real.Stages12(context.Background(), validate.WrapForValidation("p", src))
		Expect(r.Valid).To(BeTrue(), "%v", r.Diagnostics)
		Expect(r.Skipped).To(BeEmpty())
	})
})
