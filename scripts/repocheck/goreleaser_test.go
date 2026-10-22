// Specs over .goreleaser.yaml. Every spec here was written red first against
// the tree it guards; the spec comment records what the tree looked like
// when it failed.
package repocheck_test

import (
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// goreleaserConfig is the subset of .goreleaser.yaml these specs read.
type goreleaserConfig struct {
	Release struct {
		Prerelease string `yaml:"prerelease"`
	} `yaml:"release"`
	DockerManifests []struct {
		NameTemplate string `yaml:"name_template"`
		SkipPush     string `yaml:"skip_push"`
	} `yaml:"docker_manifests"`
	Changelog struct {
		Filters struct {
			Exclude []string `yaml:"exclude"`
		} `yaml:"filters"`
	} `yaml:"changelog"`
}

// Red run, 2026-09-10: the exclude list was ['^docs:', '^chore:', '^test:',
// '^ci:'] -- unscoped, so it missed every scoped conventional-commit subject
// this repo actually uses: `git log` shows four "chore(release): prepare
// vX" commits (the release-prep commit itself), plus docs(...), test(chart):
// and ci(...) subjects, none of which matched the plain prefix.
var _ = Describe(".goreleaser.yaml", func() {
	It("keeps release-prep and scoped chore/docs/test/ci commits out of the changelog", func() {
		var cfg goreleaserConfig
		loadYAML(".goreleaser.yaml", &cfg)

		var combined []*regexp.Regexp
		for _, pat := range cfg.Changelog.Filters.Exclude {
			combined = append(combined, regexp.MustCompile(pat))
		}
		matches := func(subject string) bool {
			for _, re := range combined {
				if re.MatchString(subject) {
					return true
				}
			}
			return false
		}

		for _, subject := range []string{
			"chore(release): prepare v0.3.5",
			"test(chart): render NOTES offline, and satisfy errcheck",
			"docs(chart): correct the pages",
			"ci(deps): bump an action",
			// Unscoped forms must still match too.
			"chore: tidy",
			"docs: rewrite the README",
			"test: add a spec",
			"ci: fix a workflow",
		} {
			Expect(matches(subject)).To(BeTrue(), subject)
		}

		// feat/fix stay in the changelog regardless of scope.
		for _, subject := range []string{
			"feat(auth): add local users",
			"fix: gitsync name collisions",
		} {
			Expect(matches(subject)).To(BeFalse(), subject)
		}
	})

	// Red run, 2026-09-10: release.yml's goreleaser step pinned
	// `distribution: goreleaser` but left `version: latest` -- every release
	// could silently pick up a new goreleaser major/behaviour change between
	// runs, even though .goreleaser.yaml declares a `version: 2` config
	// schema that assumes a v2.x CLI.
	It("pins the goreleaser CLI to an exact v2 release, not latest", func() {
		rel := loadWorkflow("release.yml")
		release, ok := rel.Jobs["release"]
		Expect(ok).To(BeTrue(), "release.yml has no release job")

		var version string
		var found bool
		for _, s := range release.Steps {
			if regexp.MustCompile(`^goreleaser/goreleaser-action@`).MatchString(s.Uses) {
				v, ok := s.With["version"].(string)
				Expect(ok).To(BeTrue(), "goreleaser step has no with.version")
				version, found = v, true
			}
		}
		Expect(found).To(BeTrue(), "no goreleaser-action step found")
		Expect(version).NotTo(Equal("latest"))
		Expect(version).To(MatchRegexp(`^v2\.\d+\.\d+$`),
			"goreleaser version must be an exact v2.x.y release, matching .goreleaser.yaml's `version: 2` config schema")
	})

	// Red run, 2026-10-22 (v0.19.0-rc.1 prep): the release block had no
	// `prerelease` key. goreleaser's default is false, so a vX.Y.Z-rc.N tag
	// would have published an ordinary GitHub release -- which GitHub then
	// marks "Latest" -- even though the :latest image manifests were already
	// skipped for prereleases. Both halves have to agree.
	It("marks a prerelease tag's GitHub release as a prerelease and never moves :latest for it", func() {
		var cfg goreleaserConfig
		loadYAML(".goreleaser.yaml", &cfg)

		Expect(cfg.Release.Prerelease).To(Equal("auto"),
			"release.prerelease must be auto, so a -rc tag is published as a GitHub prerelease, not the new Latest")

		latest := 0
		for _, m := range cfg.DockerManifests {
			if regexp.MustCompile(`:latest$`).MatchString(m.NameTemplate) {
				latest++
				Expect(m.SkipPush).To(Equal("{{ if .Prerelease }}true{{ end }}"),
					m.NameTemplate+" must skip its push on a prerelease")
			}
		}
		Expect(latest).To(BeNumerically(">", 0), "no :latest docker manifest found")
	})
})
