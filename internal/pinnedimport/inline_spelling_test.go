package pinnedimport_test

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/config"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// The parser (and Alloy) read `import . git`, `import/**/.git` and
// `import.\ngit` as the block name import.git: every decision is made from
// the parse tree, never from the text, so each spelling — at the top level,
// in a declare, inside an import.string's content and inside a fetched
// module — is inlined, and nothing of import.git is served.
var _ = Describe("Inline, whatever the block name's spelling", Label("integration"), func() {
	const (
		repo = "https://git.example.com/team/modules.git"
		sha  = "0123456789abcdef0123456789abcdef01234567"
	)
	var (
		ctx   context.Context
		st    *store.Store
		org   sqlc.Org
		repos []sqlc.OrgImportRepository
	)
	spellings := []string{"import.git", "import . git", "import/**/.git", "import.\ngit", "import /* x */ .\n git"}
	importing := func(name, label, path string) string {
		return name + " \"" + label + "\" {\n  repository = \"" + repo + "\"\n  revision   = \"" + sha + "\"\n  path       = \"" + path + "\"\n}\n"
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 4})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		org, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "spell-org", DisplayName: "S", AdminGroupID: "s"})
		Expect(err).NotTo(HaveOccurred())
		key, err := pinnedimport.NormalizeRepository(repo)
		Expect(err).NotTo(HaveOccurred())
		e, err := st.Queries.AddOrgImportRepository(ctx, sqlc.AddOrgImportRepositoryParams{OrgID: org.ID, Repository: repo, RepositoryKey: key, CreatedBy: "t"})
		Expect(err).NotTo(HaveOccurred())
		repos = []sqlc.OrgImportRepository{e}
		put := func(path, content string) {
			Expect(st.Queries.PutPinnedImportModule(ctx, sqlc.PutPinnedImportModuleParams{
				OrgID: org.ID, RepositoryKey: key, Revision: sha, Path: path, Content: content,
			})).To(Succeed())
		}
		put("leaf.alloy", "declare \"leaf\" { }\n")
		// A fetched module that itself imports with an odd spelling.
		put("mid.alloy", importing("import . git", "inner", "leaf.alloy")+"declare \"mid\" { }\n")
	})

	inline := func(contents string) string {
		GinkgoHelper()
		out, err := pinnedimport.Inline(ctx, st.Queries, org.ID, repos, contents)
		Expect(err).NotTo(HaveOccurred(), contents)
		Expect(validate.ServedFormProblem(out)).To(Succeed(), out)
		Expect(validate.Stage1(out).Valid).To(BeTrue(), out)
		return out
	}

	It("inlines every spelling at the top level, in a declare, inside an import.string and inside a fetched module", func() {
		for _, name := range spellings {
			inline(importing(name, "m", "leaf.alloy"))
			inline("declare \"d\" {\n" + importing(name, "m", "leaf.alloy") + "}\n")
			inline("import.string \"s\" {\n  content = " + alloysyntax.QuoteString(importing(name, "m", "leaf.alloy")) + "\n}\n")
			inline("import.string \"s\" {\n  content = " + alloysyntax.QuoteString(
				"import.string \"t\" {\n  content = "+alloysyntax.QuoteString(importing(name, "m", "mid.alloy"))+"\n}\n") + "\n}\n")
		}
		mixed := importing("import . git", "a", "leaf.alloy") + "declare \"d\" {\n" + importing("import/**/.git", "b", "mid.alloy") + "}\n" +
			"import.string \"s\" {\n  content = " + alloysyntax.QuoteString(importing("import.\ngit", "c", "leaf.alloy")) + "\n}\n"
		out := inline(mixed)
		Expect(strings.Count(out, `import.string "`)).To(Equal(3), "a, b and s at the top level; the nested ones are inside quoted content")
	})

	It("leaves an import.string without import.git byte for byte", func() {
		src := "import.string \"s\" {\n  content = \"declare \\\"x\\\" { }\"\n}\n"
		Expect(inline(src)).To(Equal(src))
	})
})
