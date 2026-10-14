// Specs over THIRD_PARTY_NOTICES.md and where it ships. Written red first:
// the comments record what the tree looked like when each failed.
package repocheck_test

import (
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const noticesFile = "THIRD_PARTY_NOTICES.md"

// lockedVersion is the version web/pnpm-lock.yaml resolves a package to.
func lockedVersion(pkg string) string {
	GinkgoHelper()
	m := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(pkg) + `@([^:(\s]+):\s*$`).
		FindStringSubmatch(readRepoFile("web/pnpm-lock.yaml"))
	Expect(m).NotTo(BeNil(), "%s is not in web/pnpm-lock.yaml", pkg)
	return m[1]
}

// finalStage is a Dockerfile's last stage: what the published image holds.
func finalStage(dockerfile string) string {
	GinkgoHelper()
	text := readRepoFile(dockerfile)
	i := strings.LastIndex(text, "\nFROM ")
	Expect(i).To(BeNumerically(">=", 0), dockerfile)
	return text[i:]
}

// Red run, 2026-10-14: THIRD_PARTY_NOTICES.md did not exist, while the SPA
// embedded in the shepherd binary and images shipped elkjs 0.12.0 (EPL-2.0
// OR GPL-3.0-or-later, whose §3.2 asks for a notice and a source pointer).
var _ = Describe("third-party notices", func() {
	It("names the elkjs version web/pnpm-lock.yaml ships", func() {
		notices := readRepoFile(noticesFile)
		m := regexp.MustCompile(`(?m)^- \*\*Version:\*\* ([^\s]+)\s*$`).FindStringSubmatch(notices)
		Expect(m).NotTo(BeNil(), "%s has no `- **Version:** x.y.z` line for elkjs", noticesFile)
		Expect(m[1]).To(Equal(lockedVersion("elkjs")),
			"elkjs was bumped: update its version, tag and commit in %s", noticesFile)
		Expect(notices).To(ContainSubstring("https://github.com/kieler/elkjs/tree/" + m[1]))
	})

	It("ships in the server release archive", func() {
		var cfg struct {
			Archives []struct {
				ID    string `yaml:"id"`
				Files []any  `yaml:"files"`
			} `yaml:"archives"`
		}
		loadYAML(".goreleaser.yaml", &cfg)
		var files []any
		for _, a := range cfg.Archives {
			if a.ID == "server" {
				files = a.Files
			}
		}
		Expect(files).To(ContainElement(noticesFile), "the server archive (it holds the binary that embeds elkjs)")
	})

	It("is in the build context of every shepherd release image", func() {
		var cfg struct {
			Dockers []struct {
				ID         string   `yaml:"id"`
				IDs        []string `yaml:"ids"`
				Dockerfile string   `yaml:"dockerfile"`
				ExtraFiles []string `yaml:"extra_files"`
			} `yaml:"dockers"`
		}
		loadYAML(".goreleaser.yaml", &cfg)
		seen := 0
		for _, d := range cfg.Dockers {
			if len(d.IDs) != 1 || d.IDs[0] != "shepherd" {
				continue
			}
			seen++
			Expect(d.ExtraFiles).To(ContainElement(noticesFile), d.ID)
			Expect(d.Dockerfile).To(Equal("deploy/Dockerfile.goreleaser"), d.ID)
		}
		Expect(seen).To(BeNumerically(">", 0))
	})

	for _, dockerfile := range []string{
		"deploy/Dockerfile.goreleaser", // the published image
		"deploy/Dockerfile.local",      // the dev / kind image, same SPA
		"deploy/Dockerfile.init",       // the init/CLI image, same binary
	} {
		It("is copied into the final stage of "+dockerfile, func() {
			Expect(finalStage(dockerfile)).To(MatchRegexp(
				`(?m)^COPY THIRD_PARTY_NOTICES\.md /licenses/THIRD_PARTY_NOTICES\.md$`))
		})
	}
})
