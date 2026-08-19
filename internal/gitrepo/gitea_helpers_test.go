package gitrepo_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	"github.com/go-git/go-git/v6/storage/memory"
	gossh "golang.org/x/crypto/ssh"
)

// giteaCreateRepo creates a repository owned by the authenticated user via
// the Gitea REST API, with an initial commit (auto_init) so it has a real
// default branch to clone. It returns the branch name and a clone URL
// built from giteaBaseURL — never trusting the API response's clone_url,
// which reflects Gitea's configured ROOT_URL rather than the dynamic
// testcontainers host:port this suite actually talks to.
func giteaCreateRepo(ctx context.Context, name string, private bool) (defaultBranch, cloneURL string, err error) {
	body, err := json.Marshal(map[string]any{
		"name":           name,
		"private":        private,
		"auto_init":      true,
		"default_branch": "main",
		"description":    "shepherd gitrepo test fixture",
	})
	if err != nil {
		return "", "", fmt.Errorf("marshal create-repo body: %w", err)
	}

	var result struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := giteaAPI(ctx, http.MethodPost, "/api/v1/user/repos", body, &result); err != nil {
		return "", "", fmt.Errorf("create repo %q: %w", name, err)
	}

	branch := result.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	return branch, fmt.Sprintf("%s/%s/%s.git", giteaBaseURL, giteaAdminUser, name), nil
}

// giteaCreateToken mints a personal access token for the admin user with
// full scope, for the `pat` auth kind test.
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

// giteaAPI makes one authenticated (admin basic auth) JSON request against
// the Gitea REST API and decodes a 2xx JSON response into out (which may
// be nil).
func giteaAPI(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, giteaBaseURL+path, reader)
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

// pusher wraps an in-memory go-git clone of a fixture repo so a test can
// push several commits to it (mutating a fixture means pushing a real
// commit, matching what production does per docs/git-provider-design.md
// §4). It exists only to set up test state; the package under test
// (gitrepo) never writes to a repo.
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

// commitAndPush writes files (path -> content, relative to repo root,
// intermediate directories created as needed), commits them, pushes to
// origin, and returns the new commit hash.
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
			Name:  "shepherd gitrepo tests",
			Email: "gitrepo-tests@shepherd.local",
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
// trailing newline).
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

// giteaAddDeployKey registers publicAuthorizedKey as a read-only deploy
// key on owner/repo, for the `ssh` auth kind tests.
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

// fetchSSHHostKey opens a real SSH connection to addr (authenticating
// with signer, e.g. a key just registered as a deploy key) purely to
// observe the server's host key via the handshake's HostKeyCallback, then
// closes the connection. It exists so tests can build a genuinely correct
// known_hosts entry — and, by mutating the captured key, a genuinely wrong
// one — without shelling out to ssh-keyscan.
//
// A short retry loop absorbs the container's SSH listener not yet being
// ready to accept the TCP connection in the first moment after
// wait.ForListeningPort reports the port open.
func fetchSSHHostKey(ctx context.Context, addr string, signer gossh.Signer) (gossh.PublicKey, error) {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
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
			time.Sleep(300 * time.Millisecond)
			continue
		}

		sshConn, _, _, err := gossh.NewClientConn(conn, addr, cfg)
		if err != nil {
			_ = conn.Close() //nolint:errcheck // best-effort close of a connection we're abandoning after a handshake error
			if captured != nil {
				// The handshake's key exchange phase (where the host key
				// callback fires) completes before authentication is
				// attempted, so a captured key is valid even if auth
				// itself failed for an unrelated reason.
				return captured, nil
			}
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		_ = sshConn.Close() //nolint:errcheck // best-effort close of a connection this helper only used to observe the host key
		return captured, nil
	}
	return nil, fmt.Errorf("fetching ssh host key from %s: %w", addr, lastErr)
}
