// Specs over .github/dependabot.yml. Every spec here was written red first
// against the tree it guards; the spec comment records what the tree looked
// like when it failed.
package repocheck_test

import (
	"encoding/json"
	"regexp"
	"slices"
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
				MatchManagers     []string `json:"matchManagers"`
				MatchPackageNames []string `json:"matchPackageNames"`
				MatchUpdateTypes  []string `json:"matchUpdateTypes"`
				Enabled           *bool    `json:"enabled"`
				PinDigests        *bool    `json:"pinDigests"`
			} `json:"packageRules"`
		}
		Expect(json.Unmarshal([]byte(readRepoFile("renovate.json")), &cfg)).To(Succeed())
		// Digest pinning must NOT come from the `:pinDigests` shorthand preset:
		// Renovate removed it, and extending a preset it cannot resolve aborts
		// every run with "Cannot find preset's package" (issues #72, #82). It
		// comes from an explicit pinDigests:true packageRule on the regex
		// manager instead, which survives preset churn.
		Expect(cfg.Extends).NotTo(ContainElement(":pinDigests"),
			"the :pinDigests preset was removed from Renovate; extending it stops every run")
		var pinsDigests bool
		for _, r := range cfg.PackageRules {
			if r.PinDigests != nil && *r.PinDigests && slices.Contains(r.MatchManagers, "custom.regex") {
				pinsDigests = true
			}
		}
		Expect(pinsDigests).To(BeTrue(),
			"a pinDigests:true packageRule on the custom.regex manager must keep pinning image digests without the preset")
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

	// Renovate evaluates matchStrings with RE2 semantics, and so does Go's
	// regexp package: a pattern Go refuses to compile is one Renovate refuses
	// too, and Renovate's answer is issue #68 "Action Required: Fix Renovate
	// Configuration" plus a silent stop of every PR. The original pattern ended
	// in a lookahead, which RE2 does not have. Compiling here catches that class
	// of mistake before it reaches the app; the extraction assertions catch a
	// pattern that compiles but no longer matches the lines it exists for.
	It("uses matchStrings RE2 can compile, and they extract the pins from every file shape", func() {
		var cfg struct {
			CustomManagers []struct {
				MatchStrings []string `json:"matchStrings"`
			} `json:"customManagers"`
		}
		Expect(json.Unmarshal([]byte(readRepoFile("renovate.json")), &cfg)).To(Succeed())
		Expect(cfg.CustomManagers).To(HaveLen(1))
		var res []*regexp.Regexp
		for _, pattern := range cfg.CustomManagers[0].MatchStrings {
			re, err := regexp.Compile(pattern)
			Expect(err).NotTo(HaveOccurred(), "Renovate (RE2) cannot compile this matchString: %s", pattern)
			Expect(re.SubexpNames()).To(ContainElements("depName", "currentValue", "currentDigest"))
			res = append(res, re)
		}
		// extract returns the named groups of the first pattern that matches
		// line, the way Renovate's default matchStringsStrategy ("any") does.
		extract := func(line string) map[string]string {
			for _, re := range res {
				m := re.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				got := map[string]string{}
				for i, n := range re.SubexpNames() {
					if n != "" {
						got[n] = m[i]
					}
				}
				return got
			}
			return nil
		}

		digest := strings.Repeat("ab", 32)
		for _, tc := range []struct{ line, dep, value, digest string }{
			{"GO_IMAGE=golang:1.26-alpine@sha256:" + digest, "golang", "1.26-alpine", "sha256:" + digest},
			{"ARG NODE_IMAGE=node:24-slim@sha256:" + digest, "node", "24-slim", "sha256:" + digest},
			{"    image: ${ALLOY_IMAGE:-grafana/alloy:v1.19.2@sha256:" + digest + "}", "grafana/alloy", "v1.19.2", "sha256:" + digest},
			{"DISTROLESS_BASE_IMAGE=gcr.io/distroless/base-nossl-debian12:nonroot@sha256:" + digest, "gcr.io/distroless/base-nossl-debian12", "nonroot", "sha256:" + digest},
			{"FROM gcr.io/distroless/static-debian12:nonroot@sha256:" + digest, "gcr.io/distroless/static-debian12", "nonroot", "sha256:" + digest},
			{"KIND_NODE_IMAGE=kindest/node:v1.31.4", "kindest/node", "v1.31.4", ""},
		} {
			got := extract(tc.line)
			Expect(got).NotTo(BeNil(), "no matchString matches %q", tc.line)
			Expect(got["depName"]).To(Equal(tc.dep), tc.line)
			Expect(got["currentValue"]).To(Equal(tc.value), tc.line)
			Expect(got["currentDigest"]).To(Equal(tc.digest), tc.line)
		}
		// Lines that look like image:tag but are not must stay unmatched, or
		// Renovate would look them up in the docker datasource and fail.
		for _, line := range []string{"    restart: unless-stopped", "    condition: service_healthy", "  url: http://gitea:3000", `      - "18090:9090"`, "      - --server.http.listen-addr=0.0.0.0:12345", "      SHEPHERD_SIMULATOR_TARGET_ADDRESS: simulator:9111"} {
			Expect(extract(line)).To(BeNil(), "matchString must not match the non-image line %q", line)
		}
	})
})
