package gitrepo

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-git/go-git/v6/plumbing/client"
	xhttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	xssh "github.com/go-git/go-git/v6/plumbing/transport/ssh"
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
			opts = append(opts, client.WithSSHAuth(pk))
		}
	}

	return opts, nil
}
