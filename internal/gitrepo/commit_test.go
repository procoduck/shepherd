package gitrepo_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/gitrepo"
)

// FileAtCommit and CheckPinned are what strict tenant mode's pinned
// import.git reads (#390): one file at one full commit SHA, and whether the
// remote is reachable with no branch or tag spelled like that SHA (Alloy
// checks a revision out as a tag, then a branch, then a commit).
var _ = Describe("Repo.FileAtCommit", func() {
	BeforeEach(func() {
		skipWithoutGitea()
	})

	var (
		ctx                context.Context
		cloneURL           string
		first, second      string
		branch             string
		p                  *pusher
		adminAuth          gitrepo.BasicAuth
		publicURL, privURL string
	)

	BeforeEach(func() {
		ctx = GinkgoT().Context()
		adminAuth = gitrepo.BasicAuth{Username: giteaAdminUser, Password: giteaAdminPass}
		var err error
		branch, cloneURL, err = giteaCreateRepo(ctx, uniqueRepoName("pinned"), false)
		Expect(err).NotTo(HaveOccurred())
		publicURL = cloneURL
		p, err = newPusher(ctx, cloneURL, giteaAdminUser, giteaAdminPass)
		Expect(err).NotTo(HaveOccurred())
		first, err = p.commitAndPush(ctx, map[string]string{"modules/m.alloy": "// v1\n", "other.txt": "x"}, "v1")
		Expect(err).NotTo(HaveOccurred())
		second, err = p.commitAndPush(ctx, map[string]string{"modules/m.alloy": "// v2\n"}, "v2")
		Expect(err).NotTo(HaveOccurred())
		_, privURL, err = giteaCreateRepo(ctx, uniqueRepoName("pinned-private"), true)
		Expect(err).NotTo(HaveOccurred())
	})

	It("reads a file at an older commit, not the branch tip", func() {
		repo := gitrepo.Repo{URL: publicURL}
		got, err := repo.FileAtCommit(ctx, first, "modules/m.alloy")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("// v1\n"))
		got, err = repo.FileAtCommit(ctx, second, "modules/m.alloy")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("// v2\n"))
		Expect(branch).NotTo(BeEmpty())
	})

	It("reports a commit the remote does not have", func() {
		_, err := gitrepo.Repo{URL: publicURL}.FileAtCommit(ctx, strings.Repeat("a", 40), "modules/m.alloy")
		Expect(err).To(MatchError(gitrepo.ErrCommitNotFound))
	})

	It("reports a missing path and a directory", func() {
		_, err := gitrepo.Repo{URL: publicURL}.FileAtCommit(ctx, first, "modules/none.alloy")
		Expect(err).To(MatchError(gitrepo.ErrPathNotFound))
		_, err = gitrepo.Repo{URL: publicURL}.FileAtCommit(ctx, first, "modules")
		Expect(err).To(MatchError(gitrepo.ErrNotAFile))
	})

	It("refuses a file over max_file_bytes", func() {
		repo := gitrepo.Repo{URL: publicURL, Limits: gitrepo.Limits{MaxFileBytes: 3}}
		_, err := repo.FileAtCommit(ctx, first, "modules/m.alloy")
		Expect(err).To(MatchError(gitrepo.ErrFileTooLarge))
	})

	It("refuses a revision that is not a full lowercase SHA", func() {
		for _, rev := range []string{first[:12], strings.ToUpper(first), "main", ""} {
			_, err := gitrepo.Repo{URL: publicURL}.FileAtCommit(ctx, rev, "modules/m.alloy")
			Expect(err).To(MatchError(gitrepo.ErrNotACommit), rev)
		}
	})

	It("uses the credential for a private repository", func() {
		pp, err := newPusher(ctx, privURL, giteaAdminUser, giteaAdminPass)
		Expect(err).NotTo(HaveOccurred())
		sha, err := pp.commitAndPush(ctx, map[string]string{"m.alloy": "// private\n"}, "private")
		Expect(err).NotTo(HaveOccurred())

		_, err = gitrepo.Repo{URL: privURL}.FileAtCommit(ctx, sha, "m.alloy")
		Expect(err).To(HaveOccurred(), "anonymous read of a private repository fails")

		got, err := gitrepo.Repo{URL: privURL, Auth: adminAuth}.FileAtCommit(ctx, sha, "m.alloy")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("// private\n"))
	})

	It("refuses a commit a branch or tag of the same name would shadow", func() {
		Expect(gitrepo.Repo{URL: publicURL}.CheckPinned(ctx, first)).To(Succeed())
		// A tag spelled like the commit: Alloy would check the tag out.
		pushRef(ctx, p, "refs/tags/"+first, second)
		err := gitrepo.Repo{URL: publicURL}.CheckPinned(ctx, first)
		Expect(err).To(MatchError(gitrepo.ErrRevisionShadowed))
		_, err = gitrepo.Repo{URL: publicURL}.FileAtCommit(ctx, first, "modules/m.alloy")
		Expect(err).To(MatchError(gitrepo.ErrRevisionShadowed))
	})

	It("reports an unreachable remote from CheckPinned", func() {
		err := gitrepo.Repo{URL: publicURL + "-gone"}.CheckPinned(ctx, first)
		Expect(err).To(HaveOccurred())
		Expect(err).NotTo(MatchError(gitrepo.ErrRevisionShadowed))
	})
})

var _ = Describe("Repo redirects", func() {
	It("refuses a redirect to another host when SameHostRedirects is set", func() {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}))
		DeferCleanup(target.Close)
		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "localhost", 1)+r.URL.Path, http.StatusFound)
		}))
		DeferCleanup(redirector.Close)

		repo := gitrepo.Repo{URL: redirector.URL + "/o/r.git", SameHostRedirects: true}
		err := repo.CheckPinned(GinkgoT().Context(), strings.Repeat("a", 40))
		Expect(err).To(MatchError(gitrepo.ErrRedirectRefused))
	})
})

// pushRef points ref on the remote at commit.
func pushRef(ctx context.Context, p *pusher, ref, commit string) {
	GinkgoHelper()
	Expect(p.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), plumbing.NewHash(commit)))).To(Succeed())
	Expect(p.repo.PushContext(ctx, &git.PushOptions{
		RefSpecs:      []config.RefSpec{config.RefSpec(ref + ":" + ref)},
		ClientOptions: []client.Option{client.WithHTTPAuth(p.auth)},
	})).To(Succeed())
}
