package validate_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
)

// fakeModules is a GitModules over a fixed allowlist and module set.
type fakeModules struct {
	allowed map[string]bool
	modules map[validate.GitModule]string
	// warn is returned as the fetch warning for every module.
	warn string
	// failing are modules whose fetch fails.
	failing map[validate.GitModule]bool
	fetches []validate.GitModule
}

func (f *fakeModules) Allowed(repository string) error {
	if f.allowed[repository] {
		return nil
	}
	return fmt.Errorf("repository %q is not on the organisation's allowlist", repository)
}

func (f *fakeModules) Fetch(_ context.Context, m validate.GitModule) (string, string, error) {
	f.fetches = append(f.fetches, m)
	if f.failing[m] {
		return "", "", errors.New("the source is unreachable and the module is not cached")
	}
	c, ok := f.modules[m]
	if !ok {
		return "", "", errors.New("path not found at the commit")
	}
	return c, f.warn, nil
}

const (
	repoA = "https://git.example.com/team/modules.git"
	shaA  = "0123456789abcdef0123456789abcdef01234567"
	shaB  = "89abcdef0123456789abcdef0123456789abcdef"
)

func gitImport(label, repo, rev, path string) string {
	s := "import.git \"" + label + "\" {\n  repository = \"" + repo + "\"\n"
	if rev != "" {
		s += "  revision = \"" + rev + "\"\n"
	}
	return s + "  path = \"" + path + "\"\n}\n"
}

func writerModule(tenant string) string {
	return `declare "out" {
  prometheus.remote_write "w" {
    endpoint {
      url     = "https://mimir.example/api/v1/push"
      headers = { "X-Scope-OrgID" = "` + tenant + `" }
    }
  }
}
`
}

func strictWith(m validate.GitModules) validate.TenantPolicy {
	p := strictFor()
	p.Git = m
	return p
}

func lintGit(src string, m validate.GitModules) []validate.Diagnostic {
	d, err := validate.TenantLintChecked(context.Background(), src, strictWith(m))
	Expect(err).NotTo(HaveOccurred())
	return d
}

var _ = Describe("Strict tenant mode's pinned import.git (#390)", func() {
	var mods *fakeModules
	BeforeEach(func() {
		mods = &fakeModules{
			allowed: map[string]bool{repoA: true},
			modules: map[validate.GitModule]string{
				{Repository: repoA, Revision: shaA, Path: "owned.alloy"}:   writerModule("acme"),
				{Repository: repoA, Revision: shaA, Path: "foreign.alloy"}: writerModule("victim"),
			},
		}
	})

	It("accepts a SHA-pinned import from an allowlisted repository whose module uses owned tenants", func() {
		Expect(lintGit(gitImport("m", repoA, shaA, "owned.alloy"), mods)).To(BeEmpty())
		Expect(mods.fetches).To(ConsistOf(validate.GitModule{Repository: repoA, Revision: shaA, Path: "owned.alloy"}))
	})

	It("lints the fetched module: one writing to a foreign tenant is refused", func() {
		Expect(errorsOf(lintGit(gitImport("m", repoA, shaA, "foreign.alloy"), mods))).
			To(ConsistOf(And(ContainSubstring(`import.git "m"`), ContainSubstring(`"victim"`))))
	})

	It("lints what the importing text gives the module's arguments", func() {
		mods.modules[validate.GitModule{Repository: repoA, Revision: shaA, Path: "arg.alloy"}] = `declare "out" {
  argument "tenant" { }
  prometheus.remote_write "w" {
    endpoint {
      url     = "https://mimir.example/api/v1/push"
      headers = { "X-Scope-OrgID" = argument.tenant.value }
    }
  }
}
`
		src := gitImport("m", repoA, shaA, "arg.alloy")
		Expect(lintGit(src+"m.out \"x\" {\n  tenant = \"acme\"\n}\n", mods)).To(BeEmpty())
		Expect(errorsOf(lintGit(src+"m.out \"x\" {\n  tenant = \"victim\"\n}\n", mods))).
			To(ConsistOf(ContainSubstring(`"victim"`)))
	})

	It("refuses a branch, a tag, the default revision, a short SHA and an upper-case SHA", func() {
		for rev, want := range map[string]string{
			"main":                  "full 40-character commit SHA",
			"v1.2.3":                "full 40-character commit SHA",
			"":                      "defaults to",
			shaA[:12]:               "short",
			strings.ToUpper(shaA):   "lower-case",
			shaA + "0":              "full 40-character commit SHA",
			strings.Repeat("g", 40): "full 40-character commit SHA",
		} {
			Expect(errorsOf(lintGit(gitImport("m", repoA, rev, "owned.alloy"), mods))).
				To(ConsistOf(ContainSubstring(want)), "revision %q", rev)
		}
		Expect(mods.fetches).To(BeEmpty(), "nothing is fetched for a refused revision")
	})

	It("refuses a repository not on the allowlist", func() {
		Expect(errorsOf(lintGit(gitImport("m", "https://git.example.com/other/modules.git", shaA, "owned.alloy"), mods))).
			To(ConsistOf(ContainSubstring("not on the organisation's allowlist")))
		Expect(mods.fetches).To(BeEmpty())
	})

	It("refuses repository, revision or path it cannot read as one literal", func() {
		src := "import.git \"m\" {\n  repository = env(\"REPO\")\n  revision = \"" + shaA + "\"\n  path = \"owned.alloy\"\n}\n"
		Expect(errorsOf(lintGit(src, mods))).To(ConsistOf(ContainSubstring("cannot be verified")))
		src = "import.git \"m\" {\n  repository = \"" + repoA + "\"\n  revision = \"" + shaA + "\"\n}\n"
		Expect(errorsOf(lintGit(src, mods))).To(ConsistOf(ContainSubstring("path")))
	})

	It("refuses a path that is not a clean relative path", func() {
		for _, p := range []string{"/etc/m.alloy", "../m.alloy", "a/../m.alloy", "./m.alloy", "."} {
			Expect(errorsOf(lintGit(gitImport("m", repoA, shaA, p), mods))).
				To(ConsistOf(ContainSubstring("clean relative path")), p)
		}
	})

	It("refuses a module whose fetch fails, and warns for one served from the cache", func() {
		m := validate.GitModule{Repository: repoA, Revision: shaA, Path: "owned.alloy"}
		mods.failing = map[validate.GitModule]bool{m: true}
		Expect(errorsOf(lintGit(gitImport("m", repoA, shaA, "owned.alloy"), mods))).
			To(ConsistOf(ContainSubstring("unreachable")))

		mods.failing = nil
		mods.warn = "the source could not be reached; checked against Shepherd's cached copy"
		ds := lintGit(gitImport("m", repoA, shaA, "owned.alloy"), mods)
		Expect(errorsOf(ds)).To(BeEmpty())
		Expect(ds).To(ConsistOf(And(
			HaveField("Warning", true),
			HaveField("Message", ContainSubstring("cached copy")))))
	})

	It("follows nested imports in fetched modules to depth 4 and refuses deeper", func() {
		chain := func(n int) {
			for i := 1; i <= n; i++ {
				body := writerModule("acme")
				if i < n {
					body = gitImport("n", repoA, shaA, fmt.Sprintf("l%d.alloy", i+1)) + body
				}
				mods.modules[validate.GitModule{Repository: repoA, Revision: shaA, Path: fmt.Sprintf("l%d.alloy", i)}] = body
			}
		}
		chain(4)
		Expect(lintGit(gitImport("m", repoA, shaA, "l1.alloy"), mods)).To(BeEmpty(), "four levels")

		mods.fetches = nil
		chain(5)
		Expect(errorsOf(lintGit(gitImport("m", repoA, shaA, "l1.alloy"), mods))).
			To(ConsistOf(ContainSubstring("more than 4 deep")))
	})

	It("applies the same rules to nested imports: a nested branch revision or foreign tenant is refused", func() {
		mods.modules[validate.GitModule{Repository: repoA, Revision: shaA, Path: "outer.alloy"}] = gitImport("n", repoA, "main", "owned.alloy")
		Expect(errorsOf(lintGit(gitImport("m", repoA, shaA, "outer.alloy"), mods))).
			To(ConsistOf(ContainSubstring("full 40-character commit SHA")))

		mods.modules[validate.GitModule{Repository: repoA, Revision: shaB, Path: "outer.alloy"}] = gitImport("n", repoA, shaA, "foreign.alloy")
		Expect(errorsOf(lintGit(gitImport("m", repoA, shaB, "outer.alloy"), mods))).
			To(ConsistOf(ContainSubstring(`"victim"`)))
	})

	It("still refuses import.http and import.file", func() {
		for _, src := range []string{
			"import.http \"h\" {\n  url = \"https://example.com/m.alloy\"\n}\n",
			"import.file \"f\" {\n  filename = \"/etc/m.alloy\"\n}\n",
		} {
			Expect(errorsOf(lintGit(src, mods))).To(ConsistOf(ContainSubstring("cannot be verified")), src)
		}
	})

	It("refuses import.git with no module source configured, and leaves non-strict orgs alone", func() {
		src := gitImport("m", repoA, shaA, "owned.alloy")
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring("cannot be verified")))
		Expect(validate.TenantLint(src, validate.TenantPolicy{})).To(BeEmpty())
	})
})
