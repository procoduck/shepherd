// Specs over .github/dependabot.yml. Every spec here was written red first
// against the tree it guards; the spec comment records what the tree looked
// like when it failed.
package repocheck_test

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// dependabotConfig is the subset of .github/dependabot.yml these specs read.
type dependabotConfig struct {
	Version int                `yaml:"version"`
	Updates []dependabotUpdate `yaml:"updates"`
}

type dependabotUpdate struct {
	Ecosystem string                   `yaml:"package-ecosystem"`
	Directory string                   `yaml:"directory"`
	Schedule  dependabotSchedule       `yaml:"schedule"`
	Groups    map[string]dependabotGrp `yaml:"groups"`
}

type dependabotSchedule struct {
	Interval string `yaml:"interval"`
}

type dependabotGrp struct {
	UpdateTypes []string `yaml:"update-types"`
}

// Red run, 2026-09-10: .github/dependabot.yml does not exist -- the repo has
// no automated dependency-update coverage for gomod, npm or github-actions,
// so every one of them can drift silently between manual bumps. Container
// images are Renovate's (renovate.json, spec below): Dependabot's docker
// ecosystem cannot read the ARG-driven FROMs this repo uses and never opened
// a PR in the months it watched deploy/.
var _ = Describe(".github/dependabot.yml", func() {
	It("declares weekly grouped updates for every ecosystem the repo ships", func() {
		var cfg dependabotConfig
		loadYAML(".github/dependabot.yml", &cfg)

		Expect(cfg.Version).To(Equal(2))

		type key struct{ eco, dir string }
		want := []key{
			{"gomod", "/"},
			{"npm", "/web"},
			{"github-actions", "/"},
		}

		got := map[key]dependabotUpdate{}
		for _, u := range cfg.Updates {
			got[key{u.Ecosystem, u.Directory}] = u
		}

		for _, k := range want {
			u, ok := got[k]
			Expect(ok).To(BeTrue(), "missing updates entry for ecosystem %q directory %q", k.eco, k.dir)
			Expect(u.Schedule.Interval).To(Equal("weekly"), "%s %s must schedule weekly", k.eco, k.dir)
			Expect(u.Groups).NotTo(BeEmpty(), "%s %s must group its updates", k.eco, k.dir)

			var sawMinor, sawPatch bool
			for _, g := range u.Groups {
				for _, t := range g.UpdateTypes {
					if t == "minor" {
						sawMinor = true
					}
					if t == "patch" {
						sawPatch = true
					}
				}
			}
			Expect(sawMinor).To(BeTrue(), "%s %s must group minor updates", k.eco, k.dir)
			Expect(sawPatch).To(BeTrue(), "%s %s must group patch updates", k.eco, k.dir)
		}

		Expect(got).To(HaveLen(3), "expected exactly the three ecosystems (images are Renovate's), got %d entries", len(got))
	})
})

// renovate.json owns the container-image pins. The regex manager must keep
// covering every file that restates an image string (versions.env is the
// source of truth; the Dockerfile ARG defaults and compose fallbacks restate
// it and `make check-docker` fails when they drift), and an Alloy TAG bump
// must stay disabled: it is a schema bump (`make schema`, overlay review),
// never a pin refresh. Red run: deleting the grafana/alloy package rule, or
// dropping deploy/versions.env from the file patterns, fails this spec.
var _ = Describe("renovate.json", func() {
	It("pins image digests across every file that restates a pin, and never proposes an Alloy tag bump", func() {
		var cfg struct {
			Extends        []string `json:"extends"`
			CustomManagers []struct {
				CustomType          string   `json:"customType"`
				ManagerFilePatterns []string `json:"managerFilePatterns"`
				MatchStrings        []string `json:"matchStrings"`
				DatasourceTemplate  string   `json:"datasourceTemplate"`
			} `json:"customManagers"`
			PackageRules []struct {
				MatchPackageNames []string `json:"matchPackageNames"`
				MatchUpdateTypes  []string `json:"matchUpdateTypes"`
				Enabled           *bool    `json:"enabled"`
			} `json:"packageRules"`
		}
		Expect(json.Unmarshal([]byte(readRepoFile("renovate.json")), &cfg)).To(Succeed())
		Expect(cfg.Extends).To(ContainElement(":pinDigests"))
		Expect(cfg.CustomManagers).To(HaveLen(1))
		m := cfg.CustomManagers[0]
		Expect(m.CustomType).To(Equal("regex"))
		Expect(m.DatasourceTemplate).To(Equal("docker"))
		joined := strings.Join(m.ManagerFilePatterns, "\n")
		for _, must := range []string{"deploy/versions", "deploy/Dockerfile", "docker-compose", "mockmsft/Dockerfile"} {
			Expect(joined).To(ContainSubstring(must), "renovate must manage the files that restate an image pin")
		}
		Expect(m.MatchStrings[0]).To(ContainSubstring("currentDigest"))

		var alloyFrozen bool
		for _, r := range cfg.PackageRules {
			for _, n := range r.MatchPackageNames {
				if n == "grafana/alloy" && r.Enabled != nil && !*r.Enabled {
					Expect(r.MatchUpdateTypes).To(ConsistOf("major", "minor", "patch"))
					alloyFrozen = true
				}
			}
		}
		Expect(alloyFrozen).To(BeTrue(), "an Alloy tag bump is a schema bump and must not come from Renovate")
	})
})
