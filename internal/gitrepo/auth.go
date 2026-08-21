package gitrepo

import (
	"context"

	"golang.org/x/crypto/ssh"
)

// Auth supplies per-request git credentials for one repo link.
//
// A strategy that only works over one transport returns ok=false for the
// other: an HTTPS-only strategy (NoneAuth, BasicAuth, PATAuth, and the
// token-exchange strategies AdoSPAuth and GitHubAppAuth) returns ok=false
// from SSH, and SSHAuth returns ok=false from HTTP.
// Token-exchange strategies are expected to cache
// their minted token until shortly before it expires rather than minting
// one per call.
type Auth interface {
	// HTTP returns HTTP Basic credentials for an https:// remote, or
	// ok=false if this strategy does not speak HTTP.
	HTTP(ctx context.Context) (username, password string, ok bool, err error)
	// SSH returns a signer for an ssh:// remote, or ok=false if this
	// strategy does not speak SSH.
	SSH(ctx context.Context) (signer ssh.Signer, ok bool, err error)
}

// NoneAuth is the anonymous strategy: no credentials are sent. It is the
// correct choice for a public repository. A nil Repo.Auth behaves the
// same way.
type NoneAuth struct{}

// HTTP implements Auth.
func (NoneAuth) HTTP(context.Context) (string, string, bool, error) { return "", "", false, nil }

// SSH implements Auth.
func (NoneAuth) SSH(context.Context) (ssh.Signer, bool, error) { return nil, false, nil }

// BasicAuth sends a fixed username/password as HTTP Basic. It covers a
// plain git server that issues ordinary account passwords over HTTPS.
type BasicAuth struct {
	Username string
	Password string
}

// HTTP implements Auth.
func (a BasicAuth) HTTP(context.Context) (string, string, bool, error) {
	return a.Username, a.Password, true, nil
}

// SSH implements Auth.
func (BasicAuth) SSH(context.Context) (ssh.Signer, bool, error) { return nil, false, nil }

// PATAuth sends a personal access token as the password half of HTTP
// Basic. Username is provider-dependent: many hosts (GitHub, GitLab,
// Bitbucket, Gitea) accept any non-empty username; some require a fixed
// literal such as "oauth2" or "x-token-auth".
type PATAuth struct {
	Username string
	Token    string
}

// HTTP implements Auth.
func (a PATAuth) HTTP(context.Context) (string, string, bool, error) {
	return a.Username, a.Token, true, nil
}

// SSH implements Auth.
func (PATAuth) SSH(context.Context) (ssh.Signer, bool, error) { return nil, false, nil }

// sshAuthDetails is implemented by SSH-capable Auth strategies (only
// SSHAuth today, see ssh.go) to supply the login username and a
// mandatory, known_hosts-based host key callback alongside the signer
// returned from Auth.SSH. It is kept separate from Auth itself — which
// shipped in an earlier step and whose shape this task keeps unchanged —
// because HTTP-only and token-exchange strategies have no need for it.
//
// transport.go type-asserts Repo.Auth against this interface only once
// Auth.SSH has reported ok=true, and refuses to build an SSH client at all
// if the assertion fails: there is deliberately no fallback that connects
// over SSH without host key verification (an unverified SSH host key is
// an undetectable MITM — docs/git-provider-design.md §7).
type sshAuthDetails interface {
	// sshUsername is the SSH login user, e.g. "git" for the deploy-key
	// convention most git hosts use.
	sshUsername() string
	// sshHostKeyCallback verifies the remote's host key against a
	// per-credential known_hosts. It must never return a callback that
	// accepts an unknown or mismatched key.
	sshHostKeyCallback() (ssh.HostKeyCallback, error)
}
