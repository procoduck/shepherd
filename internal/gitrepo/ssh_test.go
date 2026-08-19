package gitrepo_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/go-git/go-git/v6/plumbing/transport/ssh/knownhosts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gossh "golang.org/x/crypto/ssh"

	"shepherd/internal/gitrepo"
)

var _ = Describe("SSHAuth against a real server", func() {
	BeforeEach(func() {
		skipWithoutGitea()
	})

	var (
		ctx           = GinkgoT().Context
		cloneURL      string
		defaultBranch string
		privateKeyPEM []byte
		signer        gossh.Signer
		correctKnown  []byte
		firstSHA      string
	)

	BeforeEach(func() {
		var err error
		repoName := uniqueRepoName("ssh")
		defaultBranch, _, err = giteaCreateRepo(ctx(), repoName, true)
		Expect(err).NotTo(HaveOccurred())
		cloneURL = fmt.Sprintf("ssh://git@%s/%s/%s.git", giteaSSHAddr, giteaAdminUser, repoName)

		var publicAuthorizedKey string
		privateKeyPEM, publicAuthorizedKey, err = generateSSHKeyPair()
		Expect(err).NotTo(HaveOccurred())

		signer, err = gossh.ParsePrivateKey(privateKeyPEM)
		Expect(err).NotTo(HaveOccurred())

		Expect(giteaAddDeployKey(ctx(), giteaAdminUser, repoName, "shepherd gitrepo test key", publicAuthorizedKey)).To(Succeed())

		hostKey, err := fetchSSHHostKey(ctx(), giteaSSHAddr, signer)
		Expect(err).NotTo(HaveOccurred())
		Expect(hostKey).NotTo(BeNil())
		correctKnown = []byte(knownHostsLine(giteaSSHAddr, hostKey))

		p, err := newPusher(ctx(), fmt.Sprintf("%s/%s/%s.git", giteaBaseURL, giteaAdminUser, repoName), giteaAdminUser, giteaAdminPass)
		Expect(err).NotTo(HaveOccurred())
		firstSHA, err = p.commitAndPush(ctx(), map[string]string{
			"pipelines/app.alloy": "// v1\nlogging { level = \"info\" }\n",
		}, "add app.alloy")
		Expect(err).NotTo(HaveOccurred())
	})

	It("authenticates with a deploy key and reads the file, verifying the correct host key", func() {
		repo := gitrepo.Repo{
			URL:    cloneURL,
			Branch: defaultBranch,
			Path:   "pipelines",
			Auth: gitrepo.SSHAuth{
				PrivateKeyPEM: privateKeyPEM,
				KnownHosts:    correctKnown,
			},
		}

		sha, err := repo.LatestCommit(ctx())
		Expect(err).NotTo(HaveOccurred())
		Expect(sha).To(Equal(firstSHA))

		files, err := repo.Files(ctx())
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(HaveLen(1))
		Expect(files[0].Path).To(Equal("pipelines/app.alloy"))
		Expect(string(files[0].Content)).To(ContainSubstring("logging"))
	})

	It("fails loudly when known_hosts has the wrong host key (no accept-any-host-key mode)", func() {
		// A different, unrelated key standing in for an attacker's key on
		// a MITM'd connection: same host/port, deliberately wrong key.
		wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		wrongSSHPub, err := gossh.NewPublicKey(wrongPub)
		Expect(err).NotTo(HaveOccurred())
		wrongKnown := []byte(knownHostsLine(giteaSSHAddr, wrongSSHPub))

		repo := gitrepo.Repo{
			URL:    cloneURL,
			Branch: defaultBranch,
			Path:   "pipelines",
			Auth: gitrepo.SSHAuth{
				PrivateKeyPEM: privateKeyPEM,
				KnownHosts:    wrongKnown,
			},
		}

		_, err = repo.LatestCommit(ctx())
		Expect(err).To(HaveOccurred())
		Expect(knownhosts.IsHostKeyChanged(err)).To(BeTrue(),
			"expected a host-key-mismatch error, got: %v", err)
	})

	It("fails clearly when the private key was never registered as a deploy key on this repo", func() {
		wrongPrivatePEM, _, err := generateSSHKeyPair()
		Expect(err).NotTo(HaveOccurred())

		repo := gitrepo.Repo{
			URL:    cloneURL,
			Branch: defaultBranch,
			Path:   "pipelines",
			Auth: gitrepo.SSHAuth{
				PrivateKeyPEM: wrongPrivatePEM,
				KnownHosts:    correctKnown,
			},
		}

		_, err = repo.LatestCommit(ctx())
		Expect(err).To(HaveOccurred())
	})
})

// These exercise SSHAuth's own logic (key parsing, passphrase decryption,
// the mandatory-known_hosts guard) without needing Docker or a live
// server: a missing known_hosts fails inside Repo.clientOptions, before
// any network dial is attempted, so a syntactically valid but unreachable
// URL is enough to prove the guard fires.
var _ = Describe("SSHAuth (no server required)", func() {
	It("returns a clear error when PrivateKeyPEM is empty", func() {
		auth := gitrepo.SSHAuth{KnownHosts: []byte("dummy")}
		_, ok, err := auth.SSH(GinkgoT().Context())
		Expect(ok).To(BeFalse())
		Expect(err).To(HaveOccurred())
	})

	It("decrypts a passphrase-protected private key into a working signer", func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())

		encryptedPEM := encryptRSAPrivateKeyPEM(key, "correct-horse-battery-staple")

		auth := gitrepo.SSHAuth{PrivateKeyPEM: encryptedPEM, Passphrase: "correct-horse-battery-staple"}
		signer, ok, err := auth.SSH(GinkgoT().Context())
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())

		wantPub, err := gossh.NewPublicKey(&key.PublicKey)
		Expect(err).NotTo(HaveOccurred())
		Expect(signer.PublicKey().Marshal()).To(Equal(wantPub.Marshal()))
	})

	It("fails clearly with the wrong passphrase", func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())
		encryptedPEM := encryptRSAPrivateKeyPEM(key, "correct-horse-battery-staple")

		auth := gitrepo.SSHAuth{PrivateKeyPEM: encryptedPEM, Passphrase: "definitely-wrong"}
		_, _, err = auth.SSH(GinkgoT().Context())
		Expect(err).To(HaveOccurred())
	})

	It("fails with a clear error when KnownHosts is empty — there is no accept-any-host-key mode", func() {
		privateKeyPEM, _, err := generateSSHKeyPair()
		Expect(err).NotTo(HaveOccurred())

		repo := gitrepo.Repo{
			URL:    "ssh://git@example.invalid/owner/repo.git",
			Branch: "main",
			Auth: gitrepo.SSHAuth{
				PrivateKeyPEM: privateKeyPEM,
				// KnownHosts deliberately omitted.
			},
		}

		_, err = repo.LatestCommit(GinkgoT().Context())
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("known_hosts"))
	})
})

// knownHostsLine builds one OpenSSH known_hosts line for hostPort and key,
// normalized the same way go-git's own known_hosts parsing normalizes
// addresses (bracketed host:port for a non-default port).
func knownHostsLine(hostPort string, key gossh.PublicKey) string {
	return knownhosts.Normalize(hostPort) + " " + string(gossh.MarshalAuthorizedKey(key))
}

// encryptRSAPrivateKeyPEM PEM-encodes key using the legacy
// Proc-Type-header PEM encryption — the only encrypted-key format
// golang.org/x/crypto/ssh.ParsePrivateKeyWithPassphrase decrypts for RSA
// keys (OpenSSH's own encrypted format is a different, bcrypt-based
// binary layout). This helper exists purely to exercise SSHAuth's
// passphrase path; production callers may supply either encrypted format.
func encryptRSAPrivateKeyPEM(key *rsa.PrivateKey, passphrase string) []byte {
	GinkgoHelper()
	der := x509.MarshalPKCS1PrivateKey(key)
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", der, []byte(passphrase), x509.PEMCipherAES256) //nolint:staticcheck // SA1019: legacy PEM encryption is exactly what golang.org/x/crypto/ssh.ParsePrivateKeyWithPassphrase decrypts for RSA keys; used only to exercise that path here
	Expect(err).NotTo(HaveOccurred())
	return pem.EncodeToMemory(block)
}
