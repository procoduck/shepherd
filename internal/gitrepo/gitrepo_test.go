package gitrepo_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v6/plumbing/transport"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/gitrepo"
)

// uniqueRepoName keeps fixture repos from colliding across specs sharing
// the one Gitea container.
func uniqueRepoName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

var _ = Describe("Repo", func() {
	BeforeEach(func() {
		skipWithoutGitea()
	})

	Context("against a private repo", func() {
		var (
			ctx           context.Context
			cloneURL      string
			defaultBranch string
			pat           string
			firstSHA      string
		)

		BeforeEach(func() {
			ctx = GinkgoT().Context()

			var err error
			defaultBranch, cloneURL, err = giteaCreateRepo(ctx, uniqueRepoName("private"), true)
			Expect(err).NotTo(HaveOccurred())

			pat, err = giteaCreateToken(ctx, uniqueRepoName("tok"))
			Expect(err).NotTo(HaveOccurred())

			p, err := newPusher(ctx, cloneURL, giteaAdminUser, giteaAdminPass)
			Expect(err).NotTo(HaveOccurred())

			firstSHA, err = p.commitAndPush(ctx, map[string]string{
				"pipelines/app.alloy": "// v1\nlogging { level = \"info\" }\n",
			}, "add app.alloy")
			Expect(err).NotTo(HaveOccurred())
		})

		It("authenticates with basic auth and reads the file", func() {
			repo := gitrepo.Repo{
				URL:    cloneURL,
				Branch: defaultBranch,
				Path:   "pipelines",
				Auth:   gitrepo.BasicAuth{Username: giteaAdminUser, Password: giteaAdminPass},
			}

			sha, err := repo.LatestCommit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(Equal(firstSHA))

			files, err := repo.Files(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(HaveLen(1))
			Expect(files[0].Path).To(Equal("pipelines/app.alloy"))
			Expect(string(files[0].Content)).To(ContainSubstring("logging"))
		})

		It("authenticates with a personal access token", func() {
			repo := gitrepo.Repo{
				URL:    cloneURL,
				Branch: defaultBranch,
				Path:   "pipelines",
				Auth:   gitrepo.PATAuth{Username: giteaAdminUser, Token: pat},
			}

			sha, err := repo.LatestCommit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(Equal(firstSHA))

			files, err := repo.Files(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(HaveLen(1))
		})

		It("fails clearly on wrong credentials", func() {
			repo := gitrepo.Repo{
				URL:    cloneURL,
				Branch: defaultBranch,
				Path:   "pipelines",
				Auth:   gitrepo.BasicAuth{Username: giteaAdminUser, Password: "definitely-not-the-password"},
			}

			_, err := repo.LatestCommit(ctx)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, transport.ErrAuthenticationRequired)).To(BeTrue(),
				"expected an authentication error, got: %v", err)
		})

		It("fails clearly when anonymous against a private repo", func() {
			repo := gitrepo.Repo{
				URL:    cloneURL,
				Branch: defaultBranch,
				Path:   "pipelines",
				Auth:   gitrepo.NoneAuth{},
			}

			_, err := repo.LatestCommit(ctx)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, transport.ErrAuthenticationRequired)).To(BeTrue(),
				"expected an authentication error, got: %v", err)
		})

		It("a second push changes the tip", func() {
			repo := gitrepo.Repo{
				URL:    cloneURL,
				Branch: defaultBranch,
				Path:   "pipelines",
				Auth:   gitrepo.BasicAuth{Username: giteaAdminUser, Password: giteaAdminPass},
			}

			sha, err := repo.LatestCommit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(Equal(firstSHA))

			p, err := newPusher(ctx, cloneURL, giteaAdminUser, giteaAdminPass)
			Expect(err).NotTo(HaveOccurred())
			secondSHA, err := p.commitAndPush(ctx, map[string]string{
				"pipelines/app.alloy": "// v2\nlogging { level = \"debug\" }\n",
			}, "update app.alloy")
			Expect(err).NotTo(HaveOccurred())
			Expect(secondSHA).NotTo(Equal(firstSHA))

			sha, err = repo.LatestCommit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).To(Equal(secondSHA))

			files, err := repo.Files(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(HaveLen(1))
			Expect(string(files[0].Content)).To(ContainSubstring("debug"))
		})

		It("Files returns only *.alloy files under Path, ignoring other extensions and other directories", func() {
			p, err := newPusher(ctx, cloneURL, giteaAdminUser, giteaAdminPass)
			Expect(err).NotTo(HaveOccurred())
			_, err = p.commitAndPush(ctx, map[string]string{
				"pipelines/second.alloy": "// second\n",
				"pipelines/readme.md":    "# not alloy\n",
				"outside/ignored.alloy":  "// outside Path, must not appear\n",
			}, "seed mixed files")
			Expect(err).NotTo(HaveOccurred())

			repo := gitrepo.Repo{
				URL:    cloneURL,
				Branch: defaultBranch,
				Path:   "pipelines",
				Auth:   gitrepo.BasicAuth{Username: giteaAdminUser, Password: giteaAdminPass},
			}

			files, err := repo.Files(ctx)
			Expect(err).NotTo(HaveOccurred())

			var paths []string
			for _, f := range files {
				paths = append(paths, f.Path)
			}
			Expect(paths).To(ConsistOf("pipelines/app.alloy", "pipelines/second.alloy"))
		})

		It("trips the repo size limit", func() {
			repo := gitrepo.Repo{
				URL:    cloneURL,
				Branch: defaultBranch,
				Path:   "pipelines",
				Auth:   gitrepo.BasicAuth{Username: giteaAdminUser, Password: giteaAdminPass},
				Limits: gitrepo.Limits{MaxRepoBytes: 1}, // trips on the very first byte of any real response
			}

			_, err := repo.Files(ctx)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, gitrepo.ErrRepoTooLarge)).To(BeTrue(),
				"expected ErrRepoTooLarge, got: %v", err)
		})
	})

	Context("against a public repo", func() {
		var (
			ctx           context.Context
			cloneURL      string
			defaultBranch string
		)

		BeforeEach(func() {
			ctx = GinkgoT().Context()

			var err error
			defaultBranch, cloneURL, err = giteaCreateRepo(ctx, uniqueRepoName("public"), false)
			Expect(err).NotTo(HaveOccurred())

			p, err := newPusher(ctx, cloneURL, giteaAdminUser, giteaAdminPass)
			Expect(err).NotTo(HaveOccurred())
			_, err = p.commitAndPush(ctx, map[string]string{
				"app.alloy": "// public\n",
			}, "add app.alloy")
			Expect(err).NotTo(HaveOccurred())
		})

		It("reads anonymously with the none auth kind", func() {
			repo := gitrepo.Repo{
				URL:    cloneURL,
				Branch: defaultBranch,
				Auth:   gitrepo.NoneAuth{},
			}

			sha, err := repo.LatestCommit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(sha).NotTo(BeEmpty())

			files, err := repo.Files(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(HaveLen(1))
			Expect(files[0].Path).To(Equal("app.alloy"))
		})

		It("reads anonymously with a nil Auth, same as NoneAuth", func() {
			repo := gitrepo.Repo{URL: cloneURL, Branch: defaultBranch}

			files, err := repo.Files(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(HaveLen(1))
		})
	})

	Context("branch handling", func() {
		It("LatestCommit returns a clear error for a branch that does not exist", func() {
			ctx := GinkgoT().Context()
			_, cloneURL, err := giteaCreateRepo(ctx, uniqueRepoName("nobranch"), false)
			Expect(err).NotTo(HaveOccurred())

			repo := gitrepo.Repo{URL: cloneURL, Branch: "does-not-exist", Auth: gitrepo.NoneAuth{}}
			_, err = repo.LatestCommit(ctx)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, gitrepo.ErrBranchNotFound)).To(BeTrue(),
				"expected ErrBranchNotFound, got: %v", err)
		})
	})
})
