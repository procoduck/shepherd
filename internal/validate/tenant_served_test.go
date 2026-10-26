package validate_test

import (
	"context"
	"errors"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/validate"
)

// What a strict org is served is the pipeline with its pinned modules
// inlined (pinnedimport.Inline): ValidatePipeline validates that form too —
// Stages 1-2 and the tenant lint — and refuses the save when it cannot be
// built.
var _ = Describe("ValidatePipeline on the served (inlined) form", func() {
	v := validate.New(&config.ValidateConfig{StabilityLevel: "generally-available", Timeout: 10 * time.Second})
	src := gitImport("m", repoA, shaA, "owned.alloy")
	policy := func(inline func(context.Context, string) (string, error)) validate.TenantPolicy {
		p := strictWith(&fakeModules{
			allowed: map[string]bool{repoA: true},
			modules: map[validate.GitModule]string{{Repository: repoA, Revision: shaA, Path: "owned.alloy"}: writerModule("acme")},
		})
		p.Inline = inline
		return p
	}
	served := func(tenant string) func(context.Context, string) (string, error) {
		return func(context.Context, string) (string, error) {
			return "import.string \"m\" {\n  content = " + strconv.Quote(writerModule(tenant)) + "\n}\n", nil
		}
	}

	It("accepts when the served form passes", func() {
		r := v.ValidatePipeline(context.Background(), "p", src, policy(served("acme")))
		Expect(r.Valid).To(BeTrue(), "%v", r.Diagnostics)
	})

	It("refuses when the served form fails the lint or Stage 1", func() {
		r := v.ValidatePipeline(context.Background(), "p", src, policy(served("victim")))
		Expect(r.Valid).To(BeFalse())
		Expect(errorsOf(r.Diagnostics)).To(ContainElement(And(ContainSubstring("as served"), ContainSubstring(`"victim"`))))

		r = v.ValidatePipeline(context.Background(), "p", src, policy(func(context.Context, string) (string, error) {
			return "this is {{ not alloy", nil
		}))
		Expect(r.Valid).To(BeFalse())
		Expect(errorsOf(r.Diagnostics)).To(ContainElement(ContainSubstring("as served")))
	})

	It("refuses when the served form cannot be built, and never builds it for text without the import", func() {
		r := v.ValidatePipeline(context.Background(), "p", src, policy(func(context.Context, string) (string, error) {
			return "", errors.New("module not cached")
		}))
		Expect(r.Valid).To(BeFalse())
		Expect(errorsOf(r.Diagnostics)).To(ContainElement(ContainSubstring("module not cached")))

		called := false
		r = v.ValidatePipeline(context.Background(), "p", writerModule("acme"), policy(func(context.Context, string) (string, error) {
			called = true
			return "", errors.New("unexpected")
		}))
		Expect(r.Valid).To(BeTrue(), "%v", r.Diagnostics)
		Expect(called).To(BeFalse())
	})

	It("lints the served form in WithTenantLint too (the visual builder)", func() {
		r := validate.WithTenantLint(context.Background(), validate.Result{Valid: true}, src, policy(served("victim")))
		Expect(r.Valid).To(BeFalse())
	})
})
