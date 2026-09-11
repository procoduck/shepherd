package repocheck_test

import (
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Red run, 2026-09-11: web/package-lock.json had been tracked since commit
// afc8869 next to the real pnpm-lock.yaml. Nothing installed from it, but
// Dependabot scanned it and raised alerts (vitest < 4.1.11) against versions
// the app had long since left behind, and it invited a stray `npm install`.
var _ = Describe("web lockfiles", func() {
	It("tracks only pnpm-lock.yaml under web/", func() {
		cmd := exec.Command("git", "ls-files", "--", "web/package-lock.json", "web/yarn.lock", "web/npm-shrinkwrap.json")
		cmd.Dir = repoRoot()
		out, err := cmd.Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(out))).To(BeEmpty(), "a non-pnpm lockfile is tracked under web/")
		Expect(readRepoFile("web/pnpm-lock.yaml")).NotTo(BeEmpty())
	})
})
