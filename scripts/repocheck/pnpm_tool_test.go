package repocheck_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// versionsEnvValue returns KEY's value from deploy/versions.env.
func versionsEnvValue(key string) string {
	GinkgoHelper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=(.*)$`).FindStringSubmatch(readRepoFile("deploy/versions.env"))
	Expect(m).NotTo(BeNil(), "%s missing from deploy/versions.env", key)
	return strings.TrimSpace(m[1])
}

// The image builds install pnpm from a committed npm manifest + lockfile
// (deploy/pnpm-tool) with `npm ci`, so the tarball is checked against a
// committed sha512 instead of whatever the registry serves for
// pnpm@${PNPM_VERSION} (Scorecard Pinned-Dependencies, alerts 135/136).
// PNPM_VERSION in deploy/versions.env stays the one pin: the manifest, the
// lockfile and every package in it must name exactly that version.
//
// Red run, 2026-10-13: deploy/pnpm-tool/ did not exist and both Dockerfiles
// ran `npm install --global ... pnpm@${PNPM_VERSION}`; every spec below failed.
var _ = Describe("the pnpm tool manifest (deploy/pnpm-tool)", func() {
	It("pins exactly PNPM_VERSION in package.json", func() {
		want := versionsEnvValue("PNPM_VERSION")
		var pkg struct {
			Private      bool              `json:"private"`
			Dependencies map[string]string `json:"dependencies"`
		}
		Expect(json.Unmarshal([]byte(readRepoFile("deploy/pnpm-tool/package.json")), &pkg)).To(Succeed())
		Expect(pkg.Private).To(BeTrue(), "the tool manifest must never be publishable")
		Expect(pkg.Dependencies).To(Equal(map[string]string{"pnpm": want}),
			"deploy/pnpm-tool/package.json must depend on exactly pnpm@%s (deploy/versions.env PNPM_VERSION), no range", want)
	})

	It("locks every package at PNPM_VERSION with a sha512 integrity from the public registry", func() {
		want := versionsEnvValue("PNPM_VERSION")
		var lock struct {
			LockfileVersion int `json:"lockfileVersion"`
			Packages        map[string]struct {
				Version      string            `json:"version"`
				Resolved     string            `json:"resolved"`
				Integrity    string            `json:"integrity"`
				Dependencies map[string]string `json:"dependencies"`
			} `json:"packages"`
		}
		Expect(json.Unmarshal([]byte(readRepoFile("deploy/pnpm-tool/package-lock.json")), &lock)).To(Succeed())
		Expect(lock.LockfileVersion).To(BeNumerically(">=", 3))

		Expect(lock.Packages).To(HaveKey(""))
		Expect(lock.Packages[""].Dependencies).To(Equal(map[string]string{"pnpm": want}),
			"the lockfile's root must match package.json (regenerate it with npm install --package-lock-only)")
		Expect(lock.Packages).To(HaveKey("node_modules/pnpm"))

		pkgName := regexp.MustCompile(`^node_modules/(pnpm|@pnpm/exe\.[a-z0-9-]+)$`)
		for path, p := range lock.Packages {
			if path == "" {
				continue
			}
			m := pkgName.FindStringSubmatch(path)
			Expect(m).NotTo(BeNil(), "unexpected package %q in the pnpm tool lockfile", path)
			Expect(p.Version).To(Equal(want), "%s is %s, deploy/versions.env PNPM_VERSION is %s", path, p.Version, want)
			Expect(p.Resolved).To(Equal("https://registry.npmjs.org/"+m[1]+"/-/"+filepath.Base(m[1])+"-"+want+".tgz"), path)
			Expect(p.Integrity).To(HavePrefix("sha512-"), path)
		}
	})

	It("is what both image builds install pnpm from, by npm ci, never by name from the registry", func() {
		for _, f := range []string{"deploy/Dockerfile.local", "deploy/Dockerfile.init"} {
			df := readRepoFile(f)
			Expect(df).NotTo(MatchRegexp(`npm (install|i) [^\n]*pnpm@`), "%s installs pnpm by name, unpinned by hash", f)
			Expect(df).To(ContainSubstring("COPY deploy/pnpm-tool/package.json deploy/pnpm-tool/package-lock.json"), f)
			Expect(df).To(MatchRegexp(`RUN npm ci --prefix \S+ --ignore-scripts`), f)
		}
	})

	It("is left alone by the update bots, so only a PNPM_VERSION bump moves it", func() {
		// Dependabot's npm entry covers /web only (its spec pins exactly three
		// entries); Renovate runs only its image regex manager, whose file
		// patterns must not reach this directory.
		var cfg struct {
			EnabledManagers []string `json:"enabledManagers"`
			CustomManagers  []struct {
				ManagerFilePatterns []string `json:"managerFilePatterns"`
			} `json:"customManagers"`
		}
		Expect(json.Unmarshal([]byte(readRepoFile("renovate.json")), &cfg)).To(Succeed())
		Expect(cfg.EnabledManagers).To(Equal([]string{"custom.regex"}))
		for _, m := range cfg.CustomManagers {
			for _, p := range m.ManagerFilePatterns {
				re := regexp.MustCompile(strings.Trim(p, "/"))
				for _, f := range []string{"deploy/pnpm-tool/package.json", "deploy/pnpm-tool/package-lock.json"} {
					Expect(re.MatchString(f)).To(BeFalse(), "renovate pattern %s reaches %s", p, f)
				}
			}
		}
	})
})

// check-docker is the guard `make lint` runs: it must fail when the tool
// manifest or lockfile drifts from versions.env, not only the repocheck spec
// above (which runs in CI's guards job, not in make lint).
//
// Red run, 2026-10-13: check-docker passed with a fixture manifest pinning
// pnpm 0.0.1 — it compared only the Dockerfile ARG defaults.
var _ = Describe("the check-docker guard", func() {
	drifted := func(pkgVersion, lockVersion string) string {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "package.json"), []byte(
			`{"name":"x","private":true,"dependencies":{"pnpm":"`+pkgVersion+`"}}`+"\n"), 0o644)).To(Succeed())
		lock := readRepoFile("deploy/pnpm-tool/package-lock.json")
		want := versionsEnvValue("PNPM_VERSION")
		lock = strings.ReplaceAll(lock, want, lockVersion)
		Expect(os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(lock), 0o644)).To(Succeed())
		return dir
	}

	It("passes on the committed tool manifest", func() {
		out, err := runMake("check-docker")
		Expect(err).NotTo(HaveOccurred(), out)
	})

	It("fails when the tool manifest's pnpm differs from PNPM_VERSION", func() {
		want := versionsEnvValue("PNPM_VERSION")
		out, err := runMake("check-docker", "PNPM_TOOL_DIR="+drifted("0.0.1", want))
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("package.json"))
	})

	It("fails when the tool lockfile's pnpm differs from PNPM_VERSION", func() {
		want := versionsEnvValue("PNPM_VERSION")
		out, err := runMake("check-docker", "PNPM_TOOL_DIR="+drifted(want, "0.0.1"))
		Expect(err).To(HaveOccurred(), out)
		Expect(out).To(ContainSubstring("package-lock.json"))
	})
})

// The pnpm tool manifest is an image-build input like the Dockerfiles, so
// every place that decides "the images changed" must see it: the path
// filters of the two workflows that build and run the images, ci.yml's
// backend gate (which runs the image-building test-fullstack job), and the
// buildx layer-cache keys, which otherwise restore a pnpm layer from before
// the lockfile moved.
//
// Red run, 2026-10-13: none of e2e.yml's push paths, e2e-k8s.yml's PR paths,
// ci.yml's backend regex or the four cache keys named deploy/pnpm-tool.
var _ = Describe("the pnpm tool manifest as a CI image input", func() {
	triggerPaths := func(file, event string) []string {
		GinkgoHelper()
		w := loadWorkflow(file)
		ev, ok := w.On[event].(map[string]any)
		Expect(ok).To(BeTrue(), "%s has no on.%s", file, event)
		raw, ok := ev["paths"].([]any)
		Expect(ok).To(BeTrue(), "%s on.%s.paths is not a list", file, event)
		var out []string
		for _, p := range raw {
			s, ok := p.(string)
			Expect(ok).To(BeTrue(), "%s on.%s.paths entry is not a string", file, event)
			out = append(out, s)
		}
		return out
	}

	It("triggers the image-building e2e workflows", func() {
		Expect(triggerPaths("e2e.yml", "push")).To(ContainElement("deploy/pnpm-tool/**"))
		Expect(triggerPaths("e2e-k8s.yml", "pull_request")).To(ContainElement("deploy/pnpm-tool/**"))
	})

	It("counts as a backend change in ci.yml's changes gate", func() {
		Expect(readRepoFile(".github/workflows/ci.yml")).To(ContainSubstring(`|^deploy/pnpm-tool/|`))
	})

	It("is hashed into every buildx layer-cache key", func() {
		for _, file := range []string{"ci.yml", "e2e.yml", "e2e-k8s.yml"} {
			for i, s := range buildxCacheSteps(file) {
				key, ok := s.With["key"].(string)
				Expect(ok).To(BeTrue(), "%s cache step %d: with.key is not a string", file, i)
				Expect(key).To(ContainSubstring("'deploy/pnpm-tool/package-lock.json'"), "%s cache step %d", file, i)
			}
		}
	})
})
