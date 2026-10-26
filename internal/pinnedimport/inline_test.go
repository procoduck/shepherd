package pinnedimport_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// Strict orgs are served the module text Shepherd linted, not an import.git
// collectors would fetch themselves (#390 review): Inline rewrites each
// pinned import.git into an import.string of the cached module.
var _ = Describe("Inline", Label("integration"), func() {
	const (
		repo = "https://git.example.com/team/modules.git"
		sha  = "0123456789abcdef0123456789abcdef01234567"
	)
	var (
		ctx   context.Context
		st    *store.Store
		org   sqlc.Org
		repos []sqlc.OrgImportRepository
		key   string
	)
	importing := func(label, path string) string {
		return "import.git \"" + label + "\" {\n  repository = \"" + repo + "\"\n  revision   = \"" + sha + "\"\n  path       = \"" + path + "\"\n" +
			"  basic_auth {\n    username = \"u\"\n    password = \"p\"\n  }\n}\n"
	}
	cache := func(path, content string) {
		GinkgoHelper()
		Expect(st.Queries.PutPinnedImportModule(ctx, sqlc.PutPinnedImportModuleParams{
			OrgID: org.ID, RepositoryKey: key, Revision: sha, Path: path, Content: content,
		})).To(Succeed())
	}
	inline := func(contents string) (string, error) {
		return pinnedimport.Inline(ctx, st.Queries, org.ID, repos, contents)
	}
	// moduleOf parses out and unquotes the content of import.string label.
	moduleOf := func(text, label string) string {
		GinkgoHelper()
		marker := "import.string \"" + label + "\" {\n  content = "
		i := strings.Index(text, marker)
		Expect(i).To(BeNumerically(">=", 0), text)
		rest := text[i+len(marker):]
		end := strings.Index(rest, "\n")
		s, err := strconv.Unquote(rest[:end])
		Expect(err).NotTo(HaveOccurred(), rest[:end])
		return s
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 4})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		org, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "inline-org", DisplayName: "I", AdminGroupID: "i"})
		Expect(err).NotTo(HaveOccurred())
		key, err = pinnedimport.NormalizeRepository(repo)
		Expect(err).NotTo(HaveOccurred())
		e, err := st.Queries.AddOrgImportRepository(ctx, sqlc.AddOrgImportRepositoryParams{OrgID: org.ID, Repository: repo, RepositoryKey: key, CreatedBy: "t"})
		Expect(err).NotTo(HaveOccurred())
		repos = []sqlc.OrgImportRepository{e}
	})

	It("leaves text without an import.git block byte for byte as it is", func() {
		for _, src := range []string{
			"",
			"logging { level = \"info\" }\n",
			"// mentions import.git in a comment only\nlogging { level = \"info\" }\n",
			"import.string \"s\" {\n  content = \"declare \\\"d\\\" { }\"\n}\n",
		} {
			Expect(inline(src)).To(Equal(src))
		}
	})

	It("rewrites a pinned import.git into an import.string of the exact cached bytes, keeping its label", func() {
		module := "declare \"out\" {\n  // \"quoted\" \\ and ${x}\n  argument \"t\" { }\n}\n"
		cache("m.alloy", module)
		src := "// header\n" + importing("mods", "m.alloy") + "mods.out \"x\" {\n  t = \"acme\"\n}\n"
		got, err := inline(src)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).NotTo(ContainSubstring("import.git"))
		Expect(got).NotTo(ContainSubstring("password"), "the import's auth block is not served")
		Expect(got).To(HavePrefix("// header\n"))
		Expect(got).To(HaveSuffix("mods.out \"x\" {\n  t = \"acme\"\n}\n"))
		Expect(moduleOf(got, "mods")).To(Equal(module))
		Expect(validate.Stage1(got).Valid).To(BeTrue(), got)
	})

	It("rewrites an import.git inside a declare, and nested ones inside the module, recursively", func() {
		cache("inner.alloy", "declare \"leaf\" { }\n")
		cache("outer.alloy", importing("inner", "inner.alloy")+"declare \"mid\" { }\n")
		src := "declare \"wrap\" {\n" + importing("outer", "outer.alloy") + "}\n"
		got, err := inline(src)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).NotTo(ContainSubstring("import.git"))
		outer := moduleOf(got, "outer")
		Expect(outer).NotTo(ContainSubstring("import.git"))
		Expect(moduleOf(outer, "inner")).To(Equal("declare \"leaf\" { }\n"))
		Expect(validate.Stage1(got).Valid).To(BeTrue(), got)

		refs, err := pinnedimport.References(ctx, st.Queries, org.ID, repos, src)
		Expect(err).NotTo(HaveOccurred())
		Expect(refs).To(ConsistOf(
			pinnedimport.Ref{Key: key, Revision: sha, Path: "outer.alloy"},
			pinnedimport.Ref{Key: key, Revision: sha, Path: "inner.alloy"},
		))
	})

	It("fails closed: a module not cached, a repository off the allowlist, a non-literal or a branch revision", func() {
		_, err := inline(importing("m", "none.alloy"))
		Expect(err).To(MatchError(pinnedimport.ErrNotInlinable))
		cache("m.alloy", "declare \"d\" { }\n")
		for _, src := range []string{
			strings.Replace(importing("m", "m.alloy"), "team/modules", "other/modules", 1),
			strings.Replace(importing("m", "m.alloy"), "\""+repo+"\"", "env(\"R\")", 1),
			strings.Replace(importing("m", "m.alloy"), sha, "main", 1),
		} {
			_, err := inline(src)
			Expect(err).To(MatchError(pinnedimport.ErrNotInlinable), src)
		}
	})

	It("refuses nesting deeper than 4, and a result over the pipeline size cap", func() {
		for i := 1; i <= 5; i++ {
			body := "declare \"d\" { }\n"
			if i < 5 {
				body = importing("n", fmt.Sprintf("l%d.alloy", i+1))
			}
			cache(fmt.Sprintf("l%d.alloy", i), body)
		}
		_, err := inline(importing("m", "l2.alloy"))
		Expect(err).NotTo(HaveOccurred(), "four levels")
		_, err = inline(importing("m", "l1.alloy"))
		Expect(err).To(MatchError(pinnedimport.ErrNotInlinable))

		cache("big.alloy", "// "+strings.Repeat("\"", validate.MaxContentBytes/2+10)+"\n")
		_, err = inline(importing("m", "big.alloy"))
		Expect(err).To(MatchError(ContainSubstring("more than")))
	})
})
