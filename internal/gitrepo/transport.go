package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport"
	xhttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	xssh "github.com/go-git/go-git/v6/plumbing/transport/ssh"
	"github.com/go-git/go-git/v6/plumbing/transport/ssh/knownhosts"
	gossh "golang.org/x/crypto/ssh"
)

// clientOptions builds a fresh, per-credential go-git transport for one
// operation.
//
// Design constraint (docs/git-provider-design.md §3.5, verified against
// go-git v6): the documented custom-TLS mechanism is
// transport.Register("https", ...), a GLOBAL registration that cannot
// express per-credential CAs or per-credential skip-verify — every repo
// would share whatever the last registration set. This package never
// calls transport.Register. Instead it builds a brand-new *http.Client
// (with its own TLS config and its own counting RoundTripper) on every
// call and hands it to go-git through the v6 ClientOptions
// ([]client.Option on CloneOptions/ListOptions), which go-git resolves
// into a private, call-scoped client.Client — the per-call transport
// option the design doc asks us to prefer over the global registration.
func (r Repo) clientOptions(ctx context.Context, counter *byteCounter) ([]client.Option, error) {
	tlsCfg, err := r.TLS.tlsConfig()
	if err != nil {
		return nil, err
	}

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.TLSClientConfig = tlsCfg
	transport.Proxy = http.ProxyFromEnvironment // honour HTTPS_PROXY/NO_PROXY, per §3.5

	httpClient := &http.Client{
		Transport: &countingTransport{next: transport, counter: counter},
	}
	if r.SameHostRedirects {
		httpClient.CheckRedirect = sameHostOnly
	}

	opts := []client.Option{client.WithHTTPClient(httpClient)}

	if r.Auth != nil {
		username, password, ok, err := r.Auth.HTTP(ctx)
		if err != nil {
			return nil, fmt.Errorf("gitrepo: resolving http credentials: %w", err)
		}
		if ok {
			opts = append(opts, client.WithHTTPAuth(&xhttp.BasicAuth{Username: username, Password: password}))
		}

		signer, ok, err := r.Auth.SSH(ctx)
		if err != nil {
			return nil, fmt.Errorf("gitrepo: resolving ssh credentials: %w", err)
		}
		if ok {
			// Host key verification is mandatory and lives outside the
			// Auth interface proper (see sshAuthDetails in auth.go): an
			// SSH-capable strategy that doesn't also implement it is a
			// bug in this package, not a caller misconfiguration, so this
			// refuses to connect rather than falling back to an unverified
			// host key (docs/git-provider-design.md §7 — there is no
			// accept-any-host-key mode).
			details, ok := r.Auth.(sshAuthDetails)
			if !ok {
				return nil, fmt.Errorf("gitrepo: ssh auth strategy %T does not implement host key verification", r.Auth)
			}
			hostKeyCallback, err := details.sshHostKeyCallback()
			if err != nil {
				return nil, fmt.Errorf("gitrepo: resolving ssh host key verification: %w", err)
			}

			pk := &xssh.PublicKeys{User: details.sshUsername(), Signer: signer}
			pk.HostKeyCallback = hostKeyCallback
			opts = append(opts, client.WithSSHAuth(sshClientConfig{pk: pk, hostKeyCallback: hostKeyCallback}))
		}
	}

	return opts, nil
}

// ErrRedirectRefused is an HTTP redirect to another scheme, host or port,
// refused because Repo.SameHostRedirects is set.
var ErrRedirectRefused = errors.New("gitrepo: redirect to another host refused")

// sameHostOnly is an http.Client.CheckRedirect that refuses a hop off the
// first request's scheme and host:port.
func sameHostOnly(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || !strings.EqualFold(req.URL.Host, first.Host) {
		return fmt.Errorf("%w: %s://%s to %s://%s", ErrRedirectRefused, first.Scheme, first.Host, req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// sshClientConfig wraps an *xssh.PublicKeys auth into go-git's
// client.SSHAuth interface (a bare ClientConfig(ctx, req) method), adding
// one thing PublicKeys.ClientConfig alone does not provide:
// gossh.ClientConfig.HostKeyAlgorithms.
//
// Root cause (ledger F9-a): xssh.Transport.connect only derives
// HostKeyAlgorithms from the caller-supplied HostKeyCallback when the
// caller already set them. When they are left zero — as
// PublicKeys.ClientConfig always leaves them — go-git falls back to
// scanning the OS-default known_hosts locations (~/.ssh/known_hosts et
// al.) purely to compute algorithms, even though HostKeyCallback (already
// wired via SSHAuth.KnownHosts) is fully capable of verifying the
// handshake on its own. On a machine — or a deliberately HOME-isolated
// test run — with no such file, that fallback fails outright with
// "unable to find any valid known_hosts file", before our own
// known_hosts-backed HostKeyCallback ever gets a chance to run.
//
// The fix derives HostKeyAlgorithms from the exact same callback used for
// verification, via knownhosts.HostKeyAlgorithms (which probes the
// callback with a placeholder key and reads the accepted types back out
// of the resulting key-mismatch error) — so there is no dependency on
// $HOME or any on-disk known_hosts file anywhere in this path.
type sshClientConfig struct {
	pk              *xssh.PublicKeys
	hostKeyCallback gossh.HostKeyCallback
}

// ClientConfig implements go-git's client.SSHAuth.
func (s sshClientConfig) ClientConfig(ctx context.Context, req *transport.Request) (*gossh.ClientConfig, error) {
	cfg, err := s.pk.ClientConfig(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(cfg.HostKeyAlgorithms) == 0 {
		cfg.HostKeyAlgorithms = knownhosts.HostKeyAlgorithms(s.hostKeyCallback, sshHostWithPort(req))
	}
	return cfg, nil
}

// sshHostWithPort mirrors go-git's own (unexported) ssh.Transport
// hostname/port resolution closely enough for known_hosts lookups: the
// URL's host, plus its port or the standard SSH port 22 if none was
// given.
func sshHostWithPort(req *transport.Request) string {
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = strconv.Itoa(xssh.DefaultPort)
	}
	return net.JoinHostPort(host, port)
}
