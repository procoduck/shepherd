// Specs over .github/workflows/release.yml: the workflow that cuts a tagged
// release with goreleaser. Every spec here was written red first against the
// tree it guards; the spec comment records what the tree looked like when it
// failed.
package repocheck_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Red run, 2026-09-10: the "chart version already published" probe lived
// only inside the post-goreleaser "Publish the Helm chart to GHCR (OCI)"
// step (steps[15]), which runs after "goreleaser release" (steps[9]) has
// already pushed both images and cut the GitHub release. A duplicate chart
// version failed the workflow only once that half-published state existed,
// exactly what the comment above the appVersion check (lines 89-93) says the
// workflow tries to avoid.
var _ = Describe("release.yml", func() {
	It("refuses a duplicate chart version before anything is published", func() {
		rel := loadWorkflow("release.yml")
		release, ok := rel.Jobs["release"]
		Expect(ok).To(BeTrue(), "release.yml has no release job")
		steps := release.Steps

		idxProbe, idxGoreleaser := -1, -1
		for i, s := range steps {
			if idxProbe == -1 && strings.Contains(s.Run, "already published") {
				idxProbe = i
			}
			if idxGoreleaser == -1 && strings.HasPrefix(s.Uses, "goreleaser/goreleaser-action") {
				idxGoreleaser = i
			}
		}
		Expect(idxProbe).To(BeNumerically(">=", 0), "no step probes for an already-published chart version")
		Expect(idxGoreleaser).To(BeNumerically(">=", 0), "no goreleaser step found")
		Expect(idxProbe).To(BeNumerically("<", idxGoreleaser),
			"the already-published probe must run before goreleaser publishes images")
	})

	// Red run, 2026-09-10: release.yml had one job ("release") that checked
	// out the tagged commit and ran goreleaser directly -- no lint, no
	// `go vet`, no `go test`, no guards. A tag that failed CI on main (or
	// was never even pushed as a branch) could still cut a release.
	It("lints and tests the tagged commit before goreleaser runs", func() {
		rel := loadWorkflow("release.yml")

		verify, ok := rel.Jobs["verify"]
		Expect(ok).To(BeTrue(), "release.yml has no verify job")

		release, ok := rel.Jobs["release"]
		Expect(ok).To(BeTrue(), "release.yml has no release job")
		Expect(needsList(release.Needs)).To(ContainElement("verify"),
			"the release job must need the verify job")

		joined := joinedRuns(verify.Steps)
		Expect(joined).To(ContainSubstring("make guards"))
		Expect(joined).To(ContainSubstring("go build ./..."))
		Expect(joined).To(ContainSubstring("go vet ./..."))
		Expect(joined).To(ContainSubstring("go test ./..."))
		Expect(joined).To(ContainSubstring("make helm-lint"))
		Expect(joined).To(ContainSubstring("docker pull"))

		var usesGolangciLint bool
		for _, s := range verify.Steps {
			if strings.HasPrefix(s.Uses, "golangci/golangci-lint-action") {
				usesGolangciLint = true
			}
		}
		Expect(usesGolangciLint).To(BeTrue(), "verify job must run golangci-lint")
	})

	// Red run, 2026-09-10: release.yml published dist/checksums.txt and four
	// image manifests with no provenance attestation -- nothing tied a
	// downloaded archive or pulled image back to the workflow run and commit
	// that produced it.
	It("attests provenance for the release archives and images", func() {
		rel := loadWorkflow("release.yml")

		// Token-Permissions hardening (Scorecard): id-token/attestations: write
		// live on the jobs that actually run attest-build-provenance, not at the
		// top level, so the workflow's default token stays read-only.
		for _, jobName := range []string{"release", "attest-images"} {
			job, ok := rel.Jobs[jobName]
			Expect(ok).To(BeTrue(), "release.yml has no %s job", jobName)
			Expect(job.Permissions["id-token"]).To(Equal("write"),
				"attest-build-provenance in the %s job needs id-token: write", jobName)
			Expect(job.Permissions["attestations"]).To(Equal("write"),
				"attest-build-provenance in the %s job needs attestations: write", jobName)
		}

		// The top-level token stays read-only (Scorecard Token-Permissions): only
		// `release` may write contents/packages, and only where it publishes.
		Expect(rel.Permissions["contents"]).NotTo(Equal("write"),
			"top-level contents: write is over-broad — scope it to the release job")
		Expect(rel.Permissions["packages"]).NotTo(Equal("write"),
			"top-level packages: write is over-broad — scope it to the release job")

		release, ok := rel.Jobs["release"]
		Expect(ok).To(BeTrue(), "release.yml has no release job")
		Expect(release.Permissions["contents"]).To(Equal("write"),
			"the release job cuts the GitHub release and needs contents: write")
		Expect(release.Permissions["packages"]).To(Equal("write"),
			"the release job pushes images + chart to GHCR and needs packages: write")

		var attestsChecksums bool
		for _, s := range release.Steps {
			if strings.HasPrefix(s.Uses, "actions/attest-build-provenance@") {
				if sc, ok := s.With["subject-checksums"].(string); ok && strings.Contains(sc, "checksums.txt") {
					attestsChecksums = true
				}
			}
		}
		Expect(attestsChecksums).To(BeTrue(),
			"release job must attest dist/checksums.txt (the release archives) with actions/attest-build-provenance")

		// The four docker_manifests entries in .goreleaser.yaml aren't known
		// until goreleaser has run, so they're attested in a follow-up job
		// driven by a dynamic matrix built from dist/artifacts.json.
		joined := joinedRuns(release.Steps)
		Expect(joined).To(ContainSubstring("dist/artifacts.json"))
		Expect(joined).To(ContainSubstring("Docker Manifest"))

		attestImages, ok := rel.Jobs["attest-images"]
		Expect(ok).To(BeTrue(), "release.yml has no attest-images job")
		Expect(needsList(attestImages.Needs)).To(ContainElement("release"))

		var attestsImages bool
		for _, s := range attestImages.Steps {
			if strings.HasPrefix(s.Uses, "actions/attest-build-provenance@") {
				attestsImages = true
			}
		}
		Expect(attestsImages).To(BeTrue(), "attest-images job must call actions/attest-build-provenance")
	})

	// Red run, 2026-09-10: release.yml triggered on any tag matching the glob
	// "v*" -- "vfoo", "v1" and "version-2" (no, but "v-2" etc.) all start a
	// release. GitHub's tag filter has no \d character class equivalent
	// beyond [0-9], so this is the tightest filter it allows; a runtime
	// regex check backs it up.
	It("only starts a release for a semver tag", func() {
		rel := loadWorkflow("release.yml")

		push, ok := rel.On["push"].(map[string]any)
		Expect(ok).To(BeTrue(), "release.yml has no on.push")
		tagsRaw, ok := push["tags"].([]any)
		Expect(ok).To(BeTrue(), "on.push.tags is not a list")
		var tags []string
		for _, t := range tagsRaw {
			s, ok := t.(string)
			Expect(ok).To(BeTrue(), "tag filter entry is not a string")
			tags = append(tags, s)
		}
		Expect(tags).NotTo(ContainElement("v*"))
		Expect(tags).To(ContainElement("v[0-9]+.[0-9]+.[0-9]+"))
		Expect(tags).To(ContainElement("v[0-9]+.[0-9]+.[0-9]+-*"))

		verify, ok := rel.Jobs["verify"]
		Expect(ok).To(BeTrue(), "release.yml has no verify job")
		Expect(joinedRuns(verify.Steps)).To(ContainSubstring(`^v[0-9]+\.[0-9]+\.[0-9]+`),
			"the verify job must validate the tag against a semver regex at runtime, since GitHub's tag glob cannot fully enforce one")
	})
})

// needsList normalizes a job's `needs:` field (a bare string or a list of
// strings in YAML) into a slice.
func needsList(needs any) []string {
	switch v := needs.(type) {
	case nil:
		return nil
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
