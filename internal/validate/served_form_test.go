package validate_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/config"
	"shepherd/internal/validate"
)

// ServedFormProblem is the structural bound on what a strict org may be
// served: no import.git block, however spelt, anywhere — at the top level,
// in a declare, or inside an import.string's content at any depth — and no
// import.string whose content it cannot read.
var _ = Describe("ServedFormProblem", func() {
	block := func(name string) string {
		return name + " \"g\" {\n  repository = \"" + repoA + "\"\n  revision = \"" + shaA + "\"\n  path = \"m.alloy\"\n}\n"
	}
	str := func(content string) string {
		return "import.string \"s\" {\n  content = " + alloysyntax.QuoteString(content) + "\n}\n"
	}

	It("finds import.git in every spelling and place", func() {
		for _, name := range []string{"import.git", "import . git", "import/**/.git", "import.\ngit"} {
			for _, src := range []string{
				block(name),
				"declare \"d\" {\n" + block(name) + "}\n",
				str(block(name)),
				str(str("declare \"d\" {\n" + block(name) + "}\n")),
			} {
				Expect(validate.ServedFormProblem(src)).To(HaveOccurred(), src)
			}
		}
	})

	It("refuses an import.string whose content is not a plain literal, and passes clean text", func() {
		Expect(validate.ServedFormProblem("import.string \"s\" {\n  content = \"a\" + \"b\"\n}\n")).To(HaveOccurred())
		Expect(validate.ServedFormProblem(str("declare \"d\" { }\n") + "logging { level = \"info\" }\n// import.git in a comment\n")).To(Succeed())
	})
})

// The backstop (#390, second review): whatever Inline returns, the save
// refuses a served form with import.git left anywhere in it. A no-op Inline
// stands in for a broken one.
var _ = Describe("ValidatePipeline's served-form backstop", func() {
	v := validate.New(&config.ValidateConfig{StabilityLevel: "generally-available", Timeout: 10 * time.Second})
	noop := func(_ context.Context, c string) (string, error) { return c, nil }
	policy := func() validate.TenantPolicy {
		p := strictWith(&fakeModules{
			allowed: map[string]bool{repoA: true},
			modules: map[validate.GitModule]string{{Repository: repoA, Revision: shaA, Path: "owned.alloy"}: writerModule("acme")},
		})
		p.Inline = noop
		return p
	}
	It("refuses every spelling and place an Inline left behind", func() {
		for _, name := range []string{"import.git", "import . git", "import/**/.git", "import.\ngit"} {
			imp := name + " \"m\" {\n  repository = \"" + repoA + "\"\n  revision = \"" + shaA + "\"\n  path = \"owned.alloy\"\n}\n"
			for _, src := range []string{
				imp,
				"declare \"d\" {\n" + imp + "}\n",
				"import.string \"s\" {\n  content = " + alloysyntax.QuoteString(imp) + "\n}\n",
			} {
				r := v.ValidatePipeline(context.Background(), "p", src, policy())
				Expect(r.Valid).To(BeFalse(), src)
			}
		}
	})
})
