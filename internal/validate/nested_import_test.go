package validate_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/config"
	"shepherd/internal/validate"
	"shepherd/internal/wizard/wizardtest"
)

// Alloy loads import blocks inside a foreach template too. Strict mode
// checks import blocks only where it can follow them — the top level and
// directly in a declare — and refuses one anywhere else, whatever its kind;
// the served-form bound walks every block body.
var _ = Describe("Import blocks nested in other blocks", func() {
	imports := map[string]string{
		"import.git":  "import.git \"m\" {\n  repository = \"" + repoA + "\"\n  revision = \"" + shaA + "\"\n  path = \"owned.alloy\"\n}\n",
		"import.http": "import.http \"m\" {\n  url = \"https://example.com/m.alloy\"\n}\n",
		"import.file": "import.file \"m\" {\n  filename = \"/etc/m.alloy\"\n}\n",
	}
	foreach := func(inner string) string {
		return "foreach \"f\" {\n  collection = [1]\n  var = \"item\"\n  template {\n" + inner + "  }\n}\n"
	}
	shapes := func(imp string) map[string]string {
		return map[string]string{
			"foreach template":                 foreach(imp),
			"declare holding a foreach":        "declare \"d\" {\n" + foreach(imp) + "}\n",
			"import.string holding a foreach":  "import.string \"s\" {\n  content = " + alloysyntax.QuoteString(foreach(imp)) + "\n}\n",
			"any other block (logging is not)": "prometheus.exporter.self \"x\" {\n}\n" + foreach("declare \"inner\" {\n"+imp+"}\n"),
		}
	}
	mods := func() *fakeModules {
		return &fakeModules{
			allowed: map[string]bool{repoA: true},
			modules: map[validate.GitModule]string{{Repository: repoA, Revision: shaA, Path: "owned.alloy"}: writerModule("acme")},
		}
	}

	It("are refused by the lint, with or without a module source", func() {
		for kind, imp := range imports {
			for where, src := range shapes(imp) {
				Expect(errorsOf(validate.TenantLint(src, strictFor()))).NotTo(BeEmpty(), "%s in %s", kind, where)
				Expect(errorsOf(lintGit(src, mods()))).NotTo(BeEmpty(), "%s in %s, with modules", kind, where)
			}
		}
	})

	It("are refused by the served-form bound and the save's backstop, whatever Inline does", func() {
		v := validate.New(&config.ValidateConfig{StabilityLevel: "generally-available", Timeout: 10 * time.Second})
		p := strictWith(mods())
		p.Inline = func(_ context.Context, c string) (string, error) { return c, nil }
		for kind, imp := range imports {
			for where, src := range shapes(imp) {
				Expect(validate.ServedFormProblem(src)).To(HaveOccurred(), "%s in %s", kind, where)
				Expect(v.ValidatePipeline(context.Background(), "p", src, p).Valid).To(BeFalse(), "%s in %s", kind, where)
			}
		}
	})

	It("is a shape Alloy itself accepts", func() {
		bin := wizardtest.AlloyBinary()
		if bin == "" {
			Skip("no alloy binary or docker")
		}
		real := validate.New(&config.ValidateConfig{AlloyBinary: bin, StabilityLevel: "experimental", Timeout: 60 * time.Second})
		r := real.Stage2(context.Background(), foreach(imports["import.http"]))
		Expect(r.Valid).To(BeTrue(), "%v", r.Diagnostics)
	})
})
