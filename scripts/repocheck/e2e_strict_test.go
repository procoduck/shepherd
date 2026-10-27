// Specs over the strict tenant e2e suite's wiring (#421): e2e-strict.yml, the
// Makefile's e2e-strict target, and `make e2e` leaving the suite out (its
// stack has no `strict` profile). Written red first: before the suite there
// was no e2e-strict.yml, no target, and `make e2e`'s filter would have run
// the strict-tenant specs against a stack without their collector.
package repocheck_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("e2e-strict.yml (strict tenant mode against a real Alloy)", func() {
	It("runs on PRs touching the verdict's code, the schemas, the suite and the Alloy pin, weekly, and on demand", func() {
		w := loadWorkflow("e2e-strict.yml")
		Expect(w.On).To(HaveKey("schedule"))
		Expect(w.On).To(HaveKey("workflow_dispatch"))
		pr, ok := w.On["pull_request"].(map[string]any)
		Expect(ok).To(BeTrue(), "e2e-strict.yml has no on.pull_request")
		raw, ok := pr["paths"].([]any)
		Expect(ok).To(BeTrue(), "on.pull_request.paths is not a list")
		var paths []string
		for _, p := range raw {
			s, ok := p.(string)
			Expect(ok).To(BeTrue())
			paths = append(paths, s)
		}
		Expect(paths).To(ContainElements(
			"internal/validate/**", "internal/serve/**", "internal/tenancy/**", "internal/pinnedimport/**",
			"internal/schema/artifacts/**", "e2e/strict_tenant_test.go", "e2e/tenantsink/**",
			"deploy/versions.env", ".github/workflows/e2e-strict.yml",
		))
		job, ok := w.Jobs["e2e-strict"]
		Expect(ok).To(BeTrue(), "e2e-strict.yml has no e2e-strict job")
		Expect(joinedRuns(job.Steps)).To(ContainSubstring("make e2e-strict"))
	})

	It("scopes its layer cache key to its own workflow and run", func() {
		steps := buildxCacheSteps("e2e-strict.yml")
		Expect(steps).NotTo(BeEmpty())
		for _, s := range steps {
			Expect(s.With["key"]).To(And(ContainSubstring("github.workflow"), ContainSubstring("github.run_id"),
				ContainSubstring("'deploy/pnpm-tool/package-lock.json'")))
		}
	})

	It("make e2e-strict builds its images, refuses an empty run, and make e2e leaves the suite out", func() {
		Expect(mkTargetLine("e2e-strict")).To(And(ContainSubstring("docker-build-local"), ContainSubstring("docker-build-init")))
		recipe := makeRecipe("e2e-strict")
		Expect(recipe).To(And(ContainSubstring("--profile strict up"), ContainSubstring("--fail-on-empty"),
			ContainSubstring("--label-filter=strict-tenant")))
		Expect(makeRecipe("e2e")).To(ContainSubstring("!strict-tenant"))
	})
})
