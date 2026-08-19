package gitsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
)

// giteaCreateRepo creates a repository owned by the admin user via the
// Gitea REST API, with an initial commit (auto_init) so it has a real
// default branch to clone. It returns the branch name and a clone URL
// built from giteaBaseURL — never trusting the API response's clone_url,
// which reflects Gitea's configured ROOT_URL rather than the dynamic
// testcontainers host:port this suite actually talks to.
func giteaCreateRepo(ctx context.Context, name string) (defaultBranch, cloneURL string, err error) {
	body, err := json.Marshal(map[string]any{
		"name":           name,
		"private":        false,
		"auto_init":      true,
		"default_branch": "main",
		"description":    "shepherd gitsync test fixture",
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
// full scope, for the `pat` credential kind these specs use.
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
// push commits to it — mutating a fixture means pushing a real commit,
// matching what production does. It exists only to set up test state; the
// reconciler under test never writes to a repo.
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
// intermediate directories created as needed), commits them, and pushes to
// origin.
func (p *pusher) commitAndPush(ctx context.Context, files map[string]string, message string) error {
	wt, err := p.repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}

	for path, content := range files {
		if err := billyutil.WriteFile(p.fs, path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		if _, err := wt.Add(path); err != nil {
			return fmt.Errorf("staging %s: %w", path, err)
		}
	}

	if _, err := wt.Commit(message, &git.CommitOptions{
		// A spec may deliberately re-push byte-identical content (to prove
		// the reconciler treats a changed commit tip with unchanged
		// content as a no-op), which would otherwise make go-git refuse
		// the commit as empty.
		AllowEmptyCommits: true,
		Author: &object.Signature{
			Name:  "shepherd gitsync tests",
			Email: "gitsync-tests@shepherd.local",
			When:  time.Now(),
		},
	}); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if err := p.repo.PushContext(ctx, &git.PushOptions{
		ClientOptions: []client.Option{client.WithHTTPAuth(p.auth)},
	}); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}
