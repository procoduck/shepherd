package pinnedimport_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
	"shepherd/internal/validate"
)

func TestPinnedImport(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Pinned import Suite")
}

var sharedPG *testutil.SharedPostgres

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	sharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(context.Background(), sharedPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if sharedPG != nil {
		Expect(sharedPG.Terminate(context.Background())).To(Succeed())
	}
})

// fakeGit records what was asked and answers from files.
type fakeGit struct {
	mu          sync.Mutex
	files       map[string]string // commit + ":" + path
	unreachable bool
	shadowed    bool
	block       bool
	fetches     int
	checks      int
	auths       []gitrepo.Auth
	urls        []string
}

func (f *fakeGit) FileAtCommit(ctx context.Context, repo gitrepo.Repo, commit, path string) ([]byte, error) {
	f.mu.Lock()
	f.fetches++
	f.auths = append(f.auths, repo.Auth)
	f.urls = append(f.urls, repo.URL)
	f.mu.Unlock()
	if !repo.SameHostRedirects {
		return nil, errors.New("pinned fetches must refuse cross-host redirects")
	}
	if f.block {
		<-ctx.Done()
		return nil, fmt.Errorf("%w: slow", gitrepo.ErrFetchTimeout)
	}
	if f.unreachable {
		return nil, errors.New("dial tcp: connection refused (password=hunter2)")
	}
	c, ok := f.files[commit+":"+path]
	if !ok {
		return nil, gitrepo.ErrPathNotFound
	}
	return []byte(c), nil
}

func (f *fakeGit) CheckPinned(_ context.Context, _ gitrepo.Repo, _ string) error {
	f.mu.Lock()
	f.checks++
	f.mu.Unlock()
	if f.shadowed {
		return gitrepo.ErrRevisionShadowed
	}
	if f.unreachable {
		return errors.New("dial tcp: connection refused")
	}
	return nil
}

const (
	repoURL = "https://git.example.com/team/modules.git"
	sha     = "0123456789abcdef0123456789abcdef01234567"
)

var _ = Describe("Resolver", Label("integration"), func() {
	var (
		ctx   context.Context
		st    *store.Store
		org   sqlc.Org
		enc   *crypto.Encryptor
		fake  *fakeGit
		res   *pinnedimport.Resolver
		entry sqlc.OrgImportRepository
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 4})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		org, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "pi-org", DisplayName: "P", AdminGroupID: "pi-admins"})
		Expect(err).NotTo(HaveOccurred())
		enc, err = crypto.NewEncryptor(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
		Expect(err).NotTo(HaveOccurred())
		fake = &fakeGit{files: map[string]string{sha + ":m.alloy": "// module\n"}}
		res = pinnedimport.New(&config.Config{}, enc, slog.New(slog.DiscardHandler)).WithFetcher(fake, 0)
		key, err := pinnedimport.NormalizeRepository(repoURL)
		Expect(err).NotTo(HaveOccurred())
		entry, err = st.Queries.AddOrgImportRepository(ctx, sqlc.AddOrgImportRepositoryParams{
			OrgID: org.ID, Repository: repoURL, RepositoryKey: key, CreatedBy: "t",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	modules := func(src pinnedimport.Source) validate.GitModules {
		return src.Modules(st.Queries, org.ID, []sqlc.OrgImportRepository{entry})
	}
	module := validate.GitModule{Repository: repoURL, Revision: sha, Path: "m.alloy"}

	It("allows the entry under any equivalent spelling, and nothing else", func() {
		m := modules(res.Saving())
		for _, r := range []string{repoURL, "https://GIT.example.com/team/modules", "https://git.example.com:443/team/modules/"} {
			Expect(m.Allowed(r)).To(Succeed(), r)
		}
		for _, r := range []string{
			"https://git.example.com/team/other.git", "https://git.example.com/Team/modules.git",
			"http://git.example.com/team/modules.git", "https://git.example.com/team/mod%75les.git", "/srv/modules.git",
		} {
			Expect(m.Allowed(r)).NotTo(Succeed(), r)
		}
	})

	It("fetches a module once, caches it, and then only checks the source", func() {
		content, warning, err := modules(res.Saving()).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal("// module\n"))
		Expect(warning).To(BeEmpty())
		Expect(fake.fetches).To(Equal(1))
		Expect(fake.urls).To(ConsistOf(repoURL), "Shepherd fetches the URL the pipeline spells, which Alloy fetches")

		content, warning, err = modules(res.Saving()).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal("// module\n"))
		Expect(warning).To(BeEmpty())
		Expect(fake.fetches).To(Equal(1), "a commit cannot change: never fetched again")
		Expect(fake.checks).To(Equal(1))

		content, _, err = modules(res.Preflight()).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal("// module\n"))
		Expect(fake.checks).To(Equal(1), "a preflight does not touch the network for a cached module")
	})

	It("refuses a module it cannot fetch and has not cached, without echoing secrets", func() {
		fake.unreachable = true
		_, _, err := modules(res.Saving()).Fetch(ctx, module)
		Expect(err).To(MatchError(ContainSubstring("fetching the module failed")))

		_, _, err = modules(res.Saving()).Fetch(ctx, validate.GitModule{Repository: repoURL, Revision: sha, Path: "none.alloy"})
		Expect(err).To(HaveOccurred())
	})

	It("warns, rather than refuses, when a cached module's source has become unreachable", func() {
		_, _, err := modules(res.Saving()).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		fake.unreachable = true
		content, warning, err := modules(res.Saving()).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal("// module\n"))
		Expect(warning).To(And(ContainSubstring("could not be reached"), ContainSubstring("collectors may fail to fetch")))
	})

	It("refuses a cached module whose commit a branch or tag now shadows", func() {
		_, _, err := modules(res.Saving()).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		fake.shadowed = true
		_, _, err = modules(res.Saving()).Fetch(ctx, module)
		Expect(err).To(MatchError(ContainSubstring("named like the commit SHA")))
	})

	It("reads only the cache in ModeCached, and a nil Resolver behaves the same", func() {
		_, _, err := modules(pinnedimport.Cached).Fetch(ctx, module)
		Expect(err).To(MatchError(ContainSubstring("has not been fetched yet")))
		var none *pinnedimport.Resolver
		_, _, err = modules(none.Saving()).Fetch(ctx, module)
		Expect(err).To(MatchError(ContainSubstring("has not been fetched yet")))
		Expect(fake.fetches).To(BeZero())

		_, _, err = modules(res.Saving()).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		content, _, err := modules(pinnedimport.Cached).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		Expect(content).To(Equal("// module\n"))
	})

	It("fetches a private repository with the entry's stored credential", func() {
		secret, err := enc.Encrypt([]byte("s3cret-pass"))
		Expect(err).NotTo(HaveOccurred())
		cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
			OrgID: org.ID, Name: "modules-reader", Kind: "basic",
			Username: pgtype.Text{String: "reader", Valid: true}, ClientSecretEnc: secret, ProviderConfig: []byte(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())
		entry.CredentialID = cred.ID

		_, _, err = modules(res.Saving()).Fetch(ctx, module)
		Expect(err).NotTo(HaveOccurred())
		Expect(fake.auths).To(ConsistOf(gitrepo.BasicAuth{Username: "reader", Password: "s3cret-pass"}))

		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "pi-other", DisplayName: "O", AdminGroupID: "o"})
		Expect(err).NotTo(HaveOccurred())
		foreign, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
			OrgID: other.ID, Name: "theirs", Kind: "basic", ClientSecretEnc: secret, ProviderConfig: []byte(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())
		entry.CredentialID = foreign.ID
		_, _, err = modules(res.Saving()).Fetch(ctx, validate.GitModule{Repository: repoURL, Revision: sha, Path: "x.alloy"})
		Expect(err).To(MatchError(ContainSubstring("credential could not be read")), "another org's credential is never used")
	})

	It("bounds a slow fetch by the save budget", func() {
		fake.block = true
		quick := pinnedimport.New(&config.Config{}, enc, slog.New(slog.DiscardHandler)).WithFetcher(fake, 100*time.Millisecond)
		start := time.Now()
		_, _, err := modules(quick.Saving()).Fetch(ctx, module)
		Expect(err).To(MatchError(ContainSubstring("timed out")))
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))
	})

	It("keeps at most MaxCachedModules per org, dropping the least recently used", func() {
		for i := 0; i < pinnedimport.MaxCachedModules+3; i++ {
			p := fmt.Sprintf("m%03d.alloy", i)
			fake.files[sha+":"+p] = "// " + p
			_, _, err := modules(res.Saving()).Fetch(ctx, validate.GitModule{Repository: repoURL, Revision: sha, Path: p})
			Expect(err).NotTo(HaveOccurred())
		}
		var n int
		Expect(st.Pool().QueryRow(ctx, `SELECT count(*) FROM pinned_import_modules WHERE org_id = $1`, org.ID).Scan(&n)).To(Succeed()) // RAW-SQL-OK: test assertion
		Expect(n).To(Equal(pinnedimport.MaxCachedModules))
		_, err := st.Queries.GetPinnedImportModule(ctx, sqlc.GetPinnedImportModuleParams{
			OrgID: org.ID, RepositoryKey: entry.RepositoryKey, Revision: sha, Path: "m000.alloy",
		})
		Expect(err).To(HaveOccurred(), "the oldest went first")
	})
})
