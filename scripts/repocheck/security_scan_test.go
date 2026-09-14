package repocheck_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The security scanners are controls, and a control that quietly stops
// running is the failure mode this repo keeps naming (SECURITY.md,
// docs/project-status.md §6). These specs pin the SHAPE of
// security-scan.yml and release.yml's gate so a well-meaning edit cannot
// turn a gate into a report, drop a SARIF upload, or lose the weekly run,
// without this suite going red.
var _ = Describe("security scanning workflows", func() {
	Describe(".github/workflows/security-scan.yml", func() {
		w := loadWorkflow("security-scan.yml")

		It("runs on pull requests, pushes to main and a weekly schedule", func() {
			Expect(w.On).To(HaveKey("pull_request"))
			Expect(w.On).To(HaveKey("push"))
			Expect(w.On).To(HaveKey("schedule"))
		})

		It("scans secrets over the full history on every run, from the pinned gitleaks image", func() {
			job, ok := w.Jobs["secrets"]
			Expect(ok).To(BeTrue(), "secrets job missing")
			Expect(job.If).To(BeEmpty(), "the secrets job must run unconditionally")
			runs := joinedRuns(job.Steps)
			Expect(runs).To(ContainSubstring("GITLEAKS_IMAGE"))
			Expect(runs).To(ContainSubstring("--exit-code 1"))
			var fullHistory bool
			for _, s := range job.Steps {
				if strings.HasPrefix(s.Uses, "actions/checkout@") {
					if d, ok := s.With["fetch-depth"]; ok && (d == 0 || d == "0") {
						fullHistory = true
					}
				}
			}
			Expect(fullHistory).To(BeTrue(), "gitleaks must see the whole history (fetch-depth: 0)")
		})

		It("gates both local images on critical/high while excluding only the vendored Alloy binary, and uploads SARIF", func() {
			job, ok := w.Jobs["image-scan"]
			Expect(ok).To(BeTrue(), "image-scan job missing")
			Expect(job.Steps).To(ContainElement(HaveField("Run", ContainSubstring("docker-build-local docker-build-simulator"))))
			gates := 0
			uploads := 0
			for _, s := range job.Steps {
				if strings.HasPrefix(s.Uses, "aquasecurity/trivy-action@") && s.With["exit-code"] == "1" {
					Expect(s.With["severity"]).To(Equal("CRITICAL,HIGH"))
					Expect(s.With["skip-files"]).To(Equal("usr/local/bin/alloy"), "the gate excludes exactly the vendored Alloy binary")
					Expect(s.With["ignore-unfixed"]).To(Equal(true))
					gates++
				}
				if strings.HasPrefix(s.Uses, "github/codeql-action/upload-sarif@") {
					uploads++
				}
			}
			Expect(gates).To(Equal(2), "one gate per image")
			Expect(uploads).To(Equal(2), "one SARIF upload per image")
		})

		It("fails on critical/high misconfiguration in deploy/ and reads the accepted list", func() {
			job, ok := w.Jobs["config-scan"]
			Expect(ok).To(BeTrue(), "config-scan job missing")
			var gated bool
			for _, s := range job.Steps {
				if strings.HasPrefix(s.Uses, "aquasecurity/trivy-action@") && s.With["exit-code"] == "1" {
					Expect(s.With["scan-type"]).To(Equal("config"))
					Expect(s.With["scan-ref"]).To(Equal("deploy"))
					Expect(s.With["trivyignores"]).To(Equal(".trivyignore"))
					gated = true
				}
			}
			Expect(gated).To(BeTrue(), "config-scan must have a failing gate step")
		})

		It("re-scans the last released image weekly and runs Scorecard", func() {
			Expect(w.Jobs).To(HaveKey("published"))
			Expect(w.Jobs["published"].If).To(ContainSubstring("schedule"))
			Expect(joinedRuns(w.Jobs["published"].Steps)).To(ContainSubstring("appVersion"))
			Expect(w.Jobs).To(HaveKey("scorecard"))
		})
	})

	Describe(".github/workflows/release.yml", func() {
		w := loadWorkflow("release.yml")

		It("gates the release on the images' vulnerabilities before anything is published", func() {
			verify := w.Jobs["verify"]
			gates := 0
			for _, s := range verify.Steps {
				if strings.HasPrefix(s.Uses, "aquasecurity/trivy-action@") {
					Expect(s.With["exit-code"]).To(Equal("1"), "the verify job's Trivy steps are gates, not reports")
					Expect(s.With["skip-files"]).To(Equal("usr/local/bin/alloy"))
					gates++
				}
			}
			Expect(gates).To(Equal(2), "one gate per image in verify")
			Expect(w.Jobs).To(HaveKey("scan-published"))
			Expect(w.Jobs["scan-published"].If).To(ContainSubstring("needs.release.result == 'success'"))
		})

		// v0.6.0's post-publish scan looked for "/shepherd:v0.6.0": the job had no
		// IMAGE_REGISTRY of its own and used the git ref as the image tag, but
		// goreleaser tags images with the bare version. The report scanned nothing.
		It("scans the published images under the name goreleaser actually pushed", func() {
			job := w.Jobs["scan-published"]
			Expect(job.Env["IMAGE_REGISTRY"]).To(ContainSubstring("vars.IMAGE_REGISTRY || 'ghcr.io/procoduck'"),
				"the scan job needs the same registry fallback as the release job")
			Expect(joinedRuns(job.Steps)).To(ContainSubstring("GITHUB_REF_NAME#v"),
				"the image tag is the tag name without its leading v")
			scans := 0
			for _, s := range job.Steps {
				if !strings.HasPrefix(s.Uses, "aquasecurity/trivy-action@") {
					continue
				}
				ref, _ := s.With["image-ref"].(string)
				Expect(ref).NotTo(ContainSubstring("github.ref_name"), "the git ref is not the image tag")
				Expect(ref).To(ContainSubstring("steps.version.outputs.version"))
				Expect(s.With["exit-code"]).To(Equal("0"), "post-publish scans report, never fail the release")
				scans++
			}
			Expect(scans).To(Equal(1))
		})
	})
})
