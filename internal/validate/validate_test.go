package validate_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
)

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
