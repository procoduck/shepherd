package validate_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/validate"
)

// #209: with no Alloy binary configured Stage 2 cannot run. The result must
// still say so — a bare Valid=true let the editor render "No problems" for
// config `alloy validate` would reject.
var _ = Describe("Skipped stages", func() {
	const content = `prometheus.scrape "test" {
  targets = []
  forward_to = []
}`
	newValidator := func(bin string) *validate.Validator {
		return validate.New(&config.ValidateConfig{
			AlloyBinary: bin, StabilityLevel: "generally-available", Timeout: 10 * time.Second,
		})
	}

	It("records stage 2 as skipped when no alloy binary is configured", func() {
		v := newValidator("")
		r := v.Stage2(context.Background(), content)
		Expect(r.Valid).To(BeTrue())
		Expect(r.Skipped).To(Equal([]int{2}))

		r12 := v.Stages12(context.Background(), content)
		Expect(r12.Valid).To(BeTrue())
		Expect(r12.Skipped).To(Equal([]int{2}))
	})

	It("records nothing as skipped when stage 2 actually runs", func() {
		bin, err := exec.LookPath("true")
		Expect(err).NotTo(HaveOccurred())
		r := newValidator(bin).Stages12(context.Background(), content)
		Expect(r.Valid).To(BeTrue())
		Expect(r.Skipped).To(BeEmpty())
	})

	It("does not claim stage 2 was skipped when stage 1 already failed", func() {
		r := newValidator("").Stages12(context.Background(), `prometheus.scrape "x" {`)
		Expect(r.Valid).To(BeFalse())
		Expect(r.Skipped).To(BeEmpty())
	})
})

func TestValidate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Validate Suite")
}

var _ = Describe("Stage1", func() {
	It("returns valid for correct Alloy syntax", func() {
		content := `prometheus.scrape "test" {
  targets = []
  forward_to = []
}`
		r := validate.Stage1(content)
		Expect(r.Valid).To(BeTrue())
		Expect(r.Diagnostics).To(BeEmpty())
	})

	It("returns diagnostics for syntax errors", func() {
		content := `prometheus.scrape "test" {
  targets = [
  // missing closing bracket
`
		r := validate.Stage1(content)
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).NotTo(BeEmpty())
		Expect(r.Diagnostics[0].Stage).To(Equal(1))
		Expect(r.Diagnostics[0].Line).To(BeNumerically(">", 0))
	})

	It("reports line/col at EOF for an unclosed declare block", func() {
		content := `declare "foo" {
  prometheus.scrape "a" {
  }
// missing closing brace for declare`
		r := validate.Stage1(content)
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).NotTo(BeEmpty())
		d := r.Diagnostics[0]
		Expect(d.Stage).To(Equal(1))
		// The parser reports the missing brace at EOF: end of the last line.
		Expect(d.Line).To(Equal(4))
		Expect(d.Col).To(Equal(37))
		Expect(d.Message).To(ContainSubstring("expected }"))
	})
})

var _ = Describe("WrapForValidation", func() {
	It("wraps content in a declare block", func() {
		wrapped := validate.WrapForValidation("my-pipeline", "// content")
		Expect(wrapped).To(ContainSubstring(`declare "pipe_my_pipeline"`))
		Expect(wrapped).To(ContainSubstring(`pipe_my_pipeline "default" { }`))
		Expect(wrapped).To(ContainSubstring(`// content`))
	})
})

var _ = Describe("Format", func() {
	It("canonicalises indentation and spacing for valid Alloy", func() {
		messy := "prometheus.scrape \"t\"   {\ntargets=[]\n    forward_to = []\n}"
		out, err := validate.Format(messy)
		Expect(err).NotTo(HaveOccurred())
		// Re-formatting the output is a no-op: it is already canonical.
		again, err := validate.Format(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(out))
		Expect(out).To(HaveSuffix("\n"))
		Expect(out).To(ContainSubstring("forward_to = []"))
	})

	It("returns an error for unparseable content and does not swallow it", func() {
		_, err := validate.Format("prometheus.scrape \"t\" {\n  targets = [\n")
		Expect(err).To(HaveOccurred())
	})
})
