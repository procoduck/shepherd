package gitrepo

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// TLSOptions configures TLS certificate verification for one credential's
// HTTPS transport. The zero value verifies against the system trust store
// only — the safe default.
type TLSOptions struct {
	// CABundle is an additional PEM-encoded CA certificate bundle trusted
	// for this credential only. It is appended to a copy of the system
	// pool, never to the process-wide pool, so one credential's private
	// CA can never leak into another repo's verification.
	CABundle []byte
	// InsecureSkipVerify disables TLS certificate verification entirely.
	// Default off; callers must opt in explicitly, and doing so should be
	// treated as an audited change by anything that lets an operator set
	// it (see docs/git-provider-design.md §3.5).
	InsecureSkipVerify bool
}

// tlsConfig builds a *tls.Config private to this call. It is never
// installed globally (see the package doc and §3.5 of the design doc):
// each Repo operation builds its own client, so one credential's relaxed
// verification can never apply to another.
func (t TLSOptions) tlsConfig() (*tls.Config, error) {
	cfg := &tls.Config{
		InsecureSkipVerify: t.InsecureSkipVerify, //nolint:gosec // explicit, per-credential, default-off opt-in — see field doc
	}

	if len(t.CABundle) == 0 {
		return cfg, nil
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(t.CABundle) {
		return nil, fmt.Errorf("gitrepo: ca bundle contains no valid PEM certificates")
	}
	cfg.RootCAs = pool
	return cfg, nil
}
