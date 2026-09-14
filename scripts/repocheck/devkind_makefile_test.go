package repocheck_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Red run, 2026-09-14: none of the five dev-kind targets exist yet.
// `mkTargetLine("dev-kind")` fails with `target "dev-kind" not found in
// Makefile` (scripts/repocheck/helpers_test.go's mkTargetLine Expect).
// docs/plans/2026-09-14-kind-dev-stack.md §K6 is the contract: dev-kind
// depends on preflight-k8s, docker-build-local and docker-build-simulator
// and calls `./scripts/dev-kind.sh up`; dev-kind-reload depends on
// docker-build-local and calls `reload`; dev-kind-seed/status/down call
// `seed`/`status`/`down` with no extra prerequisites; all five carry a
// `## ` help comment and are declared .PHONY.
var _ = Describe("the dev-kind Makefile targets", func() {
	verbByTarget := map[string]string{
		"dev-kind":        "up",
		"dev-kind-reload": "reload",
		"dev-kind-seed":   "seed",
		"dev-kind-status": "status",
		"dev-kind-down":   "down",
	}

	It("declares all five targets phony", func() {
		mk := readRepoFile("Makefile")
		for t := range verbByTarget {
			Expect(mk).To(MatchRegexp(`(?m)^\.PHONY:.*\b`+t+`\b`), t)
		}
	})

	It("gives every target a help comment", func() {
		for t := range verbByTarget {
			Expect(mkTargetLine(t)).To(MatchRegexp(`##\s+\S`), t)
		}
	})

	It("makes dev-kind depend on the k8s preflight and both local image builds", func() {
		line := mkTargetLine("dev-kind")
		// Match dev-kind, not dev-kind-reload/-seed/-status/-down.
		Expect(line).To(MatchRegexp(`^dev-kind:`))
		Expect(line).To(MatchRegexp(`\bpreflight-k8s\b`))
		Expect(line).To(MatchRegexp(`\bdocker-build-local\b`))
		Expect(line).To(MatchRegexp(`\bdocker-build-simulator\b`))
	})

	It("makes dev-kind-reload depend on docker-build-local only", func() {
		line := mkTargetLine("dev-kind-reload")
		Expect(line).To(MatchRegexp(`\bdocker-build-local\b`))
		Expect(line).NotTo(MatchRegexp(`\bdocker-build-simulator\b`))
		Expect(line).NotTo(MatchRegexp(`\bpreflight-k8s\b`))
	})

	It("gives dev-kind-seed, dev-kind-status and dev-kind-down no prerequisites", func() {
		for _, t := range []string{"dev-kind-seed", "dev-kind-status", "dev-kind-down"} {
			line := mkTargetLine(t)
			Expect(line).To(MatchRegexp(`^` + t + `: ?##`))
		}
	})

	It("has each recipe call scripts/dev-kind.sh with the matching verb", func() {
		for t, verb := range verbByTarget {
			recipe := makeRecipe(t)
			Expect(recipe).To(ContainSubstring("./scripts/dev-kind.sh " + verb))
		}
	})

	It("prints the down verb from a dry run instead of treating it as up to date", func() {
		out, err := runMake("-n", "dev-kind-down")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("./scripts/dev-kind.sh down"))
		Expect(out).NotTo(ContainSubstring("is up to date"))
	})
})
