// Specs over .github/workflows/ci.yml. Every spec here was written red
// first against the tree it guards; the spec comment records what the tree
// looked like when it failed.
package repocheck_test

import (
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// guardNamesFromMakefile follows one level of indirection: `lint:` depends
// on `guards`, and `guards:` lists the ten check-* prerequisites. Returns
// those ten names, parsed from the Makefile rather than hard-coded, so this
// spec fails the moment the two lists (guards: and lint:) drift apart again.
func guardNamesFromMakefile() []string {
	GinkgoHelper()
	lintLine := mkTargetLine("lint")
	Expect(lintLine).To(MatchRegexp(`^lint:.*\bguards\b`), "lint: must depend on guards")

	guardsLine := mkTargetLine("guards")
	re := regexp.MustCompile(`\bcheck-[a-z-]+\b`)
	names := re.FindAllString(guardsLine, -1)
	Expect(names).NotTo(BeEmpty(), "guards: target lists no check-* prerequisites")
	return names
}

// Red run, 2026-09-10: ci.yml's guards job ran
// `make check-single-dist check-dist-consistency check-build-script
// check-raw-sql check-docker check-no-route-mocks` -- six of the ten guards
// the (then-nonexistent) `guards:` Makefile target gathers; check-gateway-pin,
// check-chartvalues-pin, check-docs-version and check-docs-drift never ran in
// CI at all. The job also never ran `golangci-lint config verify`, so a
// misplaced or misspelled .golangci.yml key (the exact failure mode the lint
// target's own comment warns about, 2026-08-22) could reach main undetected in
// any PR the `lint` job's if-gate happened to skip.
// Red run, 2026-09-11: the build job ran `go vet ./...`, which skips the
// e2ek8s-tagged kind suite, so a k8s.io module skew from a grouped
// Dependabot bump compiled nowhere in CI until the weekly kind run.
var _ = Describe("the build job", func() {
	It("vets the e2ek8s-tagged kind suite so a k8s.io module skew fails every PR", func() {
		ci := loadWorkflow("ci.yml")
		build, ok := ci.Jobs["build"]
		Expect(ok).To(BeTrue(), "ci.yml has no build job")
		Expect(joinedRuns(build.Steps)).To(ContainSubstring("go vet -tags e2ek8s ./e2e/k8s/"))
	})
})

var _ = Describe("ci.yml's guards job", func() {
	It("runs every guard the Makefile's guards target gathers, via `make guards`", func() {
		names := guardNamesFromMakefile()

		ci := loadWorkflow("ci.yml")
		guards, ok := ci.Jobs["guards"]
		Expect(ok).To(BeTrue(), "ci.yml has no guards job")
		joined := joinedRuns(guards.Steps)

		// `make guards` resolves through the Makefile's own single source of
		// truth, so asserting the literal invocation is what actually proves
		// every one of the ten guards runs -- listing them again here would
		// just be a second copy that could itself drift.
		Expect(joined).To(ContainSubstring("make guards"),
			"ci.yml's guards job must invoke `make guards`, not an inline subset (guards found in Makefile: %v)", names)
		Expect(joined).NotTo(MatchRegexp(`make\s+check-[a-z-]+\s+check-`),
			"ci.yml's guards job must not list guards inline -- it should resolve through `make guards`")
	})

	It("runs golangci-lint config verify", func() {
		ci := loadWorkflow("ci.yml")
		guards, ok := ci.Jobs["guards"]
		Expect(ok).To(BeTrue(), "ci.yml has no guards job")
		Expect(joinedRuns(guards.Steps)).To(ContainSubstring("golangci-lint config verify"))
	})
})

// Red run, 2026-09-10: SECURITY.md:43 names govulncheck "the arbiter" for
// which reachable vulnerabilities are in scope, but nothing ran it anywhere
// -- not in ci.yml, not on a schedule. The base-merged Makefile `vulncheck:`
// target (`go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...`) existed
// and passed locally, but no workflow ever invoked it, and there was no
// .github/workflows/govulncheck.yml file at all.
var _ = Describe("govulncheck", func() {
	It("gates backend changes in ci.yml's build job", func() {
		ci := loadWorkflow("ci.yml")
		build, ok := ci.Jobs["build"]
		Expect(ok).To(BeTrue(), "ci.yml has no build job")
		Expect(joinedRuns(build.Steps)).To(ContainSubstring("make vulncheck"))
	})

	It("also runs weekly via .github/workflows/govulncheck.yml", func() {
		gv := loadWorkflow("govulncheck.yml")
		schedule, ok := gv.On["schedule"].([]any)
		Expect(ok).To(BeTrue(), "govulncheck.yml has no on.schedule")
		Expect(schedule).NotTo(BeEmpty())
		entry, ok := schedule[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(entry["cron"]).NotTo(BeEmpty())

		var joined string
		for _, j := range gv.Jobs {
			joined += joinedRuns(j.Steps)
		}
		Expect(joined).To(ContainSubstring("make vulncheck"), "govulncheck.yml must run `make vulncheck`")
	})
})

// Red run, 2026-09-10: (1) the `changes` job's frontend-gate grep pattern was
// `(^web/|^proto/|^buf\.(gen\.)?yaml$|^Makefile$|^\.github/workflows/ci\.yml$)`
// -- it has no scripts/build-web.sh entry, even though both gated jobs
// (`web` and `test-ui`) run entirely through that script (see ci.yml's own
// comments on each). A change confined to scripts/build-web.sh could not
// reach either job that exists to test it. (2) top-level
// `cancel-in-progress: true` cancels a still-running push-to-main run the
// moment a second push lands, even though the header comment at lines 50-54
// calls the main re-run "the safety net" for semantic conflicts between PRs
// that merged close together -- a cancelled run provides none of that
// signal.
var _ = Describe("ci.yml housekeeping", func() {
	It("routes a scripts/build-web.sh change to the frontend gate", func() {
		ci := readRepoFile(".github/workflows/ci.yml")
		re := regexp.MustCompile(`(?m)^\s*if echo "\$files" \| grep -qE '([^']+)'; then\n\s*echo "frontend=true"`)
		m := re.FindStringSubmatch(ci)
		Expect(m).NotTo(BeNil(), "could not find the frontend gate's grep pattern in ci.yml")
		pattern := regexp.MustCompile(m[1])
		Expect(pattern.MatchString("scripts/build-web.sh")).To(BeTrue(),
			"frontend gate pattern %q must match scripts/build-web.sh", m[1])
	})

	It("does not cancel an in-progress run of a push to main", func() {
		ci := readRepoFile(".github/workflows/ci.yml")
		re := regexp.MustCompile(`(?m)^\s*cancel-in-progress:\s*(.+)$`)
		m := re.FindStringSubmatch(ci)
		Expect(m).NotTo(BeNil(), "no top-level cancel-in-progress found in ci.yml")
		expr := strings.TrimSpace(m[1])
		Expect(expr).NotTo(Equal("true"),
			"cancel-in-progress: true cancels a still-running push-to-main run; it must be scoped to pull_request")
		Expect(expr).To(ContainSubstring("pull_request"))
	})
})

// Red run, 2026-09-10: ci.yml's `test` job ran bare `go test ./...` -- no
// coverage profile was produced, so nothing local or in CI ever measured or
// published Go test coverage. The base-merged Makefile `test-cover:` target
// (`go test -coverprofile=coverage.out -covermode=atomic ./... && go tool
// cover -func=coverage.out | tail -1`) existed but nothing in ci.yml called
// it.
var _ = Describe("ci.yml's test job", func() {
	It("runs make test-cover, publishes the total to the step summary, and uploads coverage.out", func() {
		ci := loadWorkflow("ci.yml")
		test, ok := ci.Jobs["test"]
		Expect(ok).To(BeTrue(), "ci.yml has no test job")
		joined := joinedRuns(test.Steps)

		Expect(joined).To(ContainSubstring("make test-cover"))
		Expect(joined).To(ContainSubstring("GITHUB_STEP_SUMMARY"))

		var uploadsCoverage bool
		for _, s := range test.Steps {
			if strings.HasPrefix(s.Uses, "actions/upload-artifact@") {
				if p, ok := s.With["path"].(string); ok && strings.Contains(p, "coverage.out") {
					uploadsCoverage = true
				}
			}
		}
		Expect(uploadsCoverage).To(BeTrue(), "test job must upload coverage.out via actions/upload-artifact")
	})
})

// Red run, 2026-09-10: ci.yml's header comment says outright "smoke is still
// not wired into CI; that remains follow-up" (line 18), and no step in any
// job runs `make smoke`. Once S2 (base-merged) rewrote smoke around
// shepherd:local/shepherd:local-init, it depends on exactly the images
// test-fullstack's own `docker-build-local docker-build-init` prerequisites
// build -- so wiring it in here costs only the ~30-60s smoke run itself, not
// a second image build.
var _ = Describe("ci.yml's test-fullstack job", func() {
	It("runs make smoke before the fullstack Playwright suite", func() {
		ci := loadWorkflow("ci.yml")
		job, ok := ci.Jobs["test-fullstack"]
		Expect(ok).To(BeTrue(), "ci.yml has no test-fullstack job")

		idxSmoke, idxFullstack := -1, -1
		for i, s := range job.Steps {
			if idxSmoke == -1 && strings.Contains(s.Run, "make smoke") {
				idxSmoke = i
			}
			if idxFullstack == -1 && strings.Contains(s.Run, "make test-fullstack") {
				idxFullstack = i
			}
		}
		Expect(idxSmoke).To(BeNumerically(">=", 0), "no step in test-fullstack runs `make smoke`")
		Expect(idxFullstack).To(BeNumerically(">=", 0), "no step in test-fullstack runs `make test-fullstack`")
		Expect(idxSmoke).To(BeNumerically("<", idxFullstack),
			"make smoke must run before make test-fullstack (matching images, one billed job)")
	})
})
