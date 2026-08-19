//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	billy "github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	billyutil "github.com/go-git/go-billy/v6/util"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	xhttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/plumbing/transport/ssh/knownhosts"
	"github.com/go-git/go-git/v6/storage/memory"
	gossh "golang.org/x/crypto/ssh"
)

// Gitea connection details for the e2e compose stack
// (e2e/docker-compose.e2e.yaml). The admin user and password here must
// match the gitea-init service's `gitea admin user create` command
// exactly — there is no wizard, no fixture endpoint, just a fixed account
// created once at stack boot per docs/git-provider-design.md §4.
const (
	// giteaHostBaseURL is how this test process (running on the host, not
	// in the compose network) reaches Gitea — used for all repo/token/key
	// setup via Gitea's own REST API and for pushing fixture commits.
	// Published as 3080 (not Gitea's own default 3000) purely to dodge the
	// common local collision with other dev tooling that also defaults to
	// 3000 — the container's internal port (what shepherd itself talks to
	// over the compose network as gitea:3000) is unaffected.
	giteaHostBaseURL = "http://localhost:13080"
	// giteaHostSSHAddr is the host-published address of Gitea's SSH
	// listener, used only to observe its real host key (see
	// fetchSSHHostKey) — the key itself is identical regardless of
	// whether it's reached via the published port or the compose network.
	giteaHostSSHAddr = "localhost:12222"
	// giteaInternalBaseURL / giteaInternalSSHAddr are how shepherd (running
	// inside the compose network) reaches the same Gitea instance — these
	// are what repo_url values must use.
	giteaInternalBaseURL = "http://gitea:3000"
	giteaInternalSSHAddr = "gitea:2222"

	giteaAdminUser = "shepherd-admin"
	giteaAdminPass = "Sh3pherd-Admin-Pass-1"
)

// giteaAPI makes one authenticated (admin basic auth) JSON request against
// Gitea's REST API and decodes a 2xx JSON response into out (which may be
// nil).
func giteaAPI(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, giteaHostBaseURL+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(giteaAdminUser, giteaAdminPass)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response fully read or discarded below

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048)) //nolint:errcheck // best-effort diagnostic
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// giteaCreateRepo creates a repository owned by the admin user via the
// Gitea REST API, with an initial commit (auto_init) so it has a real
// default branch to clone and push onto. It returns the branch name plus
// clone URLs from both this test process's vantage point (host-published
// port) and shepherd's (the compose network's internal DNS name) — never
// trusting the API response's clone_url, which reflects Gitea's ROOT_URL
// setting rather than either of those.
func giteaCreateRepo(ctx context.Context, name string) (branch, hostCloneURL, internalCloneURL string, err error) {
	body, err := json.Marshal(map[string]any{
		"name":           name,
		"private":        false,
		"auto_init":      true,
		"default_branch": "main",
		"description":    "shepherd e2e fixture",
	})
	if err != nil {
		return "", "", "", fmt.Errorf("marshal create-repo body: %w", err)
	}

	var result struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := giteaAPI(ctx, http.MethodPost, "/api/v1/user/repos", body, &result); err != nil {
		return "", "", "", fmt.Errorf("create repo %q: %w", name, err)
	}

	branch = result.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	hostCloneURL = fmt.Sprintf("%s/%s/%s.git", giteaHostBaseURL, giteaAdminUser, name)
	internalCloneURL = fmt.Sprintf("%s/%s/%s.git", giteaInternalBaseURL, giteaAdminUser, name)
	return branch, hostCloneURL, internalCloneURL, nil
}

// giteaCreateToken mints a personal access token for the admin user with
// full scope, for the `pat` (and, doubling as a stand-in real credential
// for the mocked token-exchange strategies, `github_app`) auth kind cases.
func giteaCreateToken(ctx context.Context, name string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"name":   name,
		"scopes": []string{"all"},
	})
	if err != nil {
		return "", fmt.Errorf("marshal create-token body: %w", err)
	}

	var result struct {
		Sha1 string `json:"sha1"`
	}
	if err := giteaAPI(ctx, http.MethodPost, "/api/v1/users/"+giteaAdminUser+"/tokens", body, &result); err != nil {
		return "", fmt.Errorf("create token %q: %w", name, err)
	}
	if result.Sha1 == "" {
		return "", fmt.Errorf("create token %q: response had no sha1", name)
	}
	return result.Sha1, nil
}

// giteaAddDeployKey registers publicAuthorizedKey as a read-only deploy
// key on owner/repo, for the `ssh` auth kind case.
func giteaAddDeployKey(ctx context.Context, owner, repo, title, publicAuthorizedKey string) error {
	body, err := json.Marshal(map[string]any{
		"title":     title,
		"key":       publicAuthorizedKey,
		"read_only": true,
	})
	if err != nil {
		return fmt.Errorf("marshal add-deploy-key body: %w", err)
	}
	path := fmt.Sprintf("/api/v1/repos/%s/%s/keys", owner, repo)
	if err := giteaAPI(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("add deploy key to %s/%s: %w", owner, repo, err)
	}
	return nil
}

// pusher wraps an in-memory go-git clone of a fixture repo (reached over
// the host-published HTTP port) so a test can push several commits to it —
// mutating a fixture means pushing a real commit, matching what production
// does (docs/git-provider-design.md §4). It always authenticates as the
// Gitea admin user regardless of which auth kind the repo link under test
// uses to *read* the repo; the two are independent (see e.g. the ssh case,
// whose deploy key is read-only).
type pusher struct {
	repo *git.Repository
	fs   billy.Filesystem
	auth *xhttp.BasicAuth
}

// newPusher clones cloneURL (which must already have at least one commit,
// e.g. via auto_init) using HTTP Basic credentials.
func newPusher(ctx context.Context, cloneURL, username, password string) (*pusher, error) {
	auth := &xhttp.BasicAuth{Username: username, Password: password}
	wtfs := memfs.New()
	repo, err := git.CloneContext(ctx, memory.NewStorage(), wtfs, &git.CloneOptions{
		URL:           cloneURL,
		ClientOptions: []client.Option{client.WithHTTPAuth(auth)},
	})
	if err != nil {
		return nil, fmt.Errorf("clone %s for push setup: %w", cloneURL, err)
	}
	return &pusher{repo: repo, fs: wtfs, auth: auth}, nil
}

// commitAndPush writes files (path -> content, relative to repo root),
// commits them, pushes to origin, and returns the new commit hash.
func (p *pusher) commitAndPush(ctx context.Context, files map[string]string, message string) (string, error) {
	wt, err := p.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}

	for path, content := range files {
		if err := billyutil.WriteFile(p.fs, path, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("writing %s: %w", path, err)
		}
		if _, err := wt.Add(path); err != nil {
			return "", fmt.Errorf("staging %s: %w", path, err)
		}
	}

	hash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "shepherd e2e",
			Email: "e2e@shepherd.local",
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	if err := p.repo.PushContext(ctx, &git.PushOptions{
		ClientOptions: []client.Option{client.WithHTTPAuth(p.auth)},
	}); err != nil {
		return "", fmt.Errorf("push: %w", err)
	}

	return hash.String(), nil
}

// generateSSHKeyPair returns a fresh ed25519 key pair as (PEM-encoded
// PKCS#8 private key, authorized_keys-format public key line without a
// trailing newline) for the `ssh` auth kind case.
func generateSSHKeyPair() (privatePEM []byte, publicAuthorizedKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generating ed25519 key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling private key: %w", err)
	}
	privatePEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		return nil, "", fmt.Errorf("building ssh public key: %w", err)
	}
	publicAuthorizedKey = string(gossh.MarshalAuthorizedKey(sshPub))
	return privatePEM, publicAuthorizedKey, nil
}

// generateRSAKeyPair returns a fresh 2048-bit RSA key pair as
// (PKCS#8 PEM private key, PKIX PEM public key) for the `github_app` auth
// kind case: the private key signs the app JWT, the public key is what
// mockmsft's /__fixture "github_app" kind verifies it against.
func generateRSAKeyPair() (privatePEM []byte, publicPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", fmt.Errorf("generating rsa key: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling private key: %w", err)
	}
	privatePEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling public key: %w", err)
	}
	publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return privatePEM, publicPEM, nil
}

// fetchSSHHostKey opens a real SSH connection to addr (authenticating with
// signer, e.g. a key just registered as a deploy key) purely to observe
// the server's host key via the handshake's HostKeyCallback, then closes
// the connection. It exists so the test can build a genuinely correct
// known_hosts entry without shelling out to ssh-keyscan. A short retry
// loop absorbs the container's SSH listener not yet being ready to accept
// connections in the first moment after its healthcheck reports the HTTP
// side up.
func fetchSSHHostKey(ctx context.Context, addr string, signer gossh.Signer) (gossh.PublicKey, error) {
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		var captured gossh.PublicKey
		cfg := &gossh.ClientConfig{
			User: "git",
			Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)},
			HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
				captured = key
				return nil
			},
			Timeout: 10 * time.Second,
		}

		dialer := net.Dialer{Timeout: 10 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}

		sshConn, _, _, err := gossh.NewClientConn(conn, addr, cfg)
		if err != nil {
			_ = conn.Close() //nolint:errcheck // best-effort close of a connection abandoned after a handshake error
			if captured != nil {
				// The handshake's key exchange phase (where the host key
				// callback fires) completes before authentication is
				// attempted, so a captured key is valid even if auth
				// itself failed for an unrelated reason.
				return captured, nil
			}
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		_ = sshConn.Close() //nolint:errcheck // best-effort close of a connection this helper only used to observe the host key
		return captured, nil
	}
	return nil, fmt.Errorf("fetching ssh host key from %s: %w", addr, lastErr)
}

// knownHostsLine builds one OpenSSH known_hosts line for hostPort and key,
// normalized the same way go-git's own known_hosts parsing normalizes
// entries (bracketed "[host]:port" form for a non-default port).
func knownHostsLine(hostPort string, key gossh.PublicKey) string {
	return knownhosts.Normalize(hostPort) + " " + string(gossh.MarshalAuthorizedKey(key))
}
