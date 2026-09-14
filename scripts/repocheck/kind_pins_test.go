// Specs over the kind node image, Calico and NGF pins: they must live in
// deploy/versions.env (not as Go literals in e2e/k8s), each commented, the
// node image tag-only (no digest — Renovate cannot make sense of a kind node
// tag as a container digest the way it does the images in this file), and
// Renovate must be told to leave kindest/node alone entirely. Every spec here
// was written red first against the tree it guards; the spec comment records
// what the tree looked like when it failed.
package repocheck_test

import (
	"encoding/json"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Red run, 2026-09-14: deploy/versions.env had no KIND_NODE_IMAGE,
// CALICO_VERSION or NGF_CHART_VERSION line at all — kindNodeImage() in
// e2e/k8s/main_test.go fell back to the literal "kindest/node:v1.31.4"
// (main_test.go:136), calicoManifest was a package-level const
// (main_test.go:54-58), and ngfChartVersion was a package-level const
// (route_conformance_test.go:298). This spec failed with:
//
//	Expected
//	    <string>: ...(versions.env contents, no KIND_NODE_IMAGE line)...
//	to match regular expression
//	    <string>: (?m)^KIND_NODE_IMAGE=kindest/node:v[0-9]+\.[0-9]+\.[0-9]+$
var _ = Describe("kind pins", func() {
	It("deploy/versions.env pins KIND_NODE_IMAGE, CALICO_VERSION and NGF_CHART_VERSION, each commented and tag-only", func() {
		versionsEnv := readRepoFile("deploy/versions.env")

		Expect(versionsEnv).To(MatchRegexp(`(?m)^KIND_NODE_IMAGE=kindest/node:v[0-9]+\.[0-9]+\.[0-9]+$`),
			"KIND_NODE_IMAGE must be a bare image:tag, no @sha256 digest — a kind node image is not scanned or pulled by digest here")
		Expect(versionsEnv).NotTo(MatchRegexp(`(?m)^KIND_NODE_IMAGE=.*@sha256`),
			"KIND_NODE_IMAGE must stay tag-only so Renovate's digest-shaped regex never matches it even before the exclusion rule")
		Expect(versionsEnv).To(MatchRegexp(`(?m)^CALICO_VERSION=v[0-9]+\.[0-9]+\.[0-9]+$`))
		Expect(versionsEnv).To(MatchRegexp(`(?m)^NGF_CHART_VERSION=[0-9]+\.[0-9]+\.[0-9]+$`))

		for _, key := range []string{"KIND_NODE_IMAGE", "CALICO_VERSION", "NGF_CHART_VERSION"} {
			re := regexp.MustCompile(`(?m)^#[^\n]*\n` + key + `=`)
			Expect(re.MatchString(versionsEnv)).To(BeTrue(), "%s must have a `#` comment on the line immediately above it", key)
		}
	})

	It("e2e/k8s/main_test.go reads KIND_NODE_IMAGE and CALICO_VERSION from versions.env instead of hardcoding them", func() {
		mainTest := readRepoFile("e2e/k8s/main_test.go")

		Expect(mainTest).NotTo(ContainSubstring("kindest/node:"),
			"main_test.go must not hardcode the kind node image — read KIND_NODE_IMAGE from deploy/versions.env")
		Expect(mainTest).NotTo(ContainSubstring("projectcalico/calico/v"),
			"main_test.go must not hardcode the Calico manifest URL — read CALICO_VERSION from deploy/versions.env")

		Expect(mainTest).To(ContainSubstring(`readVersionsEnvValue("KIND_NODE_IMAGE")`))
		Expect(mainTest).To(ContainSubstring(`readVersionsEnvValue("CALICO_VERSION")`))
		Expect(mainTest).To(ContainSubstring("E2E_K8S_NODE_IMAGE"),
			"the env override must still win over KIND_NODE_IMAGE — D2 requires it")
	})

	It("e2e/k8s/route_conformance_test.go reads NGF_CHART_VERSION from versions.env instead of a Go constant", func() {
		routeConformance := readRepoFile("e2e/k8s/route_conformance_test.go")

		Expect(routeConformance).NotTo(ContainSubstring(`ngfChartVersion = "`),
			"route_conformance_test.go must not declare ngfChartVersion as a Go constant — read NGF_CHART_VERSION from deploy/versions.env")
		Expect(routeConformance).To(ContainSubstring(`readVersionsEnvValue("NGF_CHART_VERSION")`))
	})

	It("renovate.json disables updates to kindest/node entirely", func() {
		var cfg struct {
			PackageRules []struct {
				MatchPackageNames []string `json:"matchPackageNames"`
				MatchUpdateTypes  []string `json:"matchUpdateTypes"`
				Enabled           *bool    `json:"enabled"`
			} `json:"packageRules"`
		}
		Expect(json.Unmarshal([]byte(readRepoFile("renovate.json")), &cfg)).To(Succeed())

		var found bool
		for _, r := range cfg.PackageRules {
			for _, n := range r.MatchPackageNames {
				if n != "kindest/node" {
					continue
				}
				Expect(r.Enabled).NotTo(BeNil(), "the kindest/node rule must set enabled")
				Expect(*r.Enabled).To(BeFalse(), "the kindest/node rule must disable updates, not just group them")
				Expect(r.MatchUpdateTypes).To(BeEmpty(),
					"no matchUpdateTypes: a bump must be excluded entirely, digest included, not just major/minor/patch")
				found = true
			}
		}
		Expect(found).To(BeTrue(), "no renovate rule disables kindest/node")
	})
})
