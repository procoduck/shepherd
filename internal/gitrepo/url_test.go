package gitrepo_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/gitrepo"
)

var _ = Describe("RemoteTransport", func() {
	DescribeTable("classifies a clone URL by the transport it is fetched over",
		func(raw string, want gitrepo.Transport) {
			got, err := gitrepo.RemoteTransport(raw)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("https", "https://gitea.internal/team/configs.git", gitrepo.TransportHTTP),
		Entry("http with a port", "http://gitea:3000/team/configs.git", gitrepo.TransportHTTP),
		Entry("Azure DevOps", "https://dev.azure.com/org/project/_git/repo", gitrepo.TransportHTTP),
		Entry("ssh:// with user and port", "ssh://git@gitea.internal:2222/team/configs.git", gitrepo.TransportSSH),
		Entry("scp-like", "git@gitea.internal:team/configs.git", gitrepo.TransportSSH),
		Entry("scp-like without a user", "gitea.internal:team/configs.git", gitrepo.TransportSSH),
		// Cloned before M6 (net/url escapes the space), so it must still save.
		Entry("Azure DevOps project with a space", "https://dev.azure.com/org/My Project/_git/repo", gitrepo.TransportHTTP),
		Entry("ssh:// with a bracketed IPv6 host", "ssh://git@[2001:db8::1]:2222/team/configs.git", gitrepo.TransportSSH),
	)

	DescribeTable("refuses what no clone could fetch, saying why",
		func(raw, wantMsg string) {
			_, err := gitrepo.RemoteTransport(raw)
			Expect(err).To(MatchError(HavePrefix(wantMsg)))
		},
		Entry("empty", "", "clone URL is required"),
		Entry("plain words (go-git reads them as a local path)", "not a url", `clone URL "not a url" is not a git remote URL`),
		Entry("a local path", "/srv/git/configs.git", `clone URL "/srv/git/configs.git" is not a git remote URL`),
		Entry("a relative path with a colon", "./repo:configs", `clone URL "./repo:configs" is not a git remote URL`),
		Entry("file://", "file:///srv/git/configs.git", `clone URL "file:///srv/git/configs.git" is not a git remote URL`),
		Entry("git://", "git://gitea.internal/team/configs.git", `clone URL "git://gitea.internal/team/configs.git" is not a git remote URL`),
		Entry("no host", "https:///team/configs.git", `clone URL "https:///team/configs.git" has no host`),
		Entry("no repository path", "https://gitea.internal/", `clone URL "https://gitea.internal/" names no repository`),
		Entry("a trailing newline", "https://gitea.internal/team/configs.git\n", `clone URL "https://gitea.internal/team/configs.git\n" is not a git remote URL`),
		Entry("a leading space", " https://gitea.internal/team/configs.git", `clone URL " https://gitea.internal/team/configs.git" is not a git remote URL`),
		Entry("an interior tab", "https://gitea.internal/team\tconfigs.git", `clone URL "https://gitea.internal/team\tconfigs.git" is not a git remote URL`),
		Entry("a space in the host", "https://gitea internal/team/configs.git", `clone URL "https://gitea internal/team/configs.git" is not a git remote URL`),
		Entry("scp-like with an IPv6 host", "git@[2001:db8::1]:team/configs.git", `clone URL "git@[2001:db8::1]:team/configs.git" puts an IPv6 address in the git@host:path form`),
	)
})
