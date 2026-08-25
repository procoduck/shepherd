package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

// OIDC discovery is the one place in Shepherd where an authenticated user
// chooses a URL the SERVER then fetches. That makes it a server-side request
// forgery primitive unless it is deliberately constrained, and app admin is an
// application role — not cluster-admin — so "the caller is already privileged"
// is not an answer. The three constraints below are what make it safe.
//
// Constraints 1 and 4 apply to every issuer. Constraints 2 and 3 apply only to
// an issuer an app admin supplied through the UI — see discoveryClientFor for
// why an issuer declared in the deployment's own configuration is a different
// question, and what it would cost to treat them the same.
//
//  1. discoveryTimeout bounds the request. go-oidc uses http.DefaultClient,
//     which has NO timeout; a host that accepts the connection and never
//     replies would otherwise hang the caller forever. That matters more here
//     than in most places: Reload holds reloadMu across the fetch, and three
//     UNAUTHENTICATED routes (/auth/methods, /auth/login, /auth/callback) can
//     reach it, so one hung fetch would pile up handler goroutines. It also
//     runs before ListenAndServe at startup, where a hang means the process
//     never becomes ready.
//
//  2. dialGuard (admin-supplied issuers only) rejects loopback, private,
//     link-local, and other non-public destinations AFTER DNS resolution. Checking the hostname instead would
//     be defeated by a name that resolves to 127.0.0.1 or 169.254.169.254 —
//     the cloud metadata endpoint — and by DNS rebinding, since the name is
//     resolved again by the dialer. Control sees the address actually being
//     connected to, which is the only check that cannot be lied to.
//
//  3. Redirects are restricted to https (admin-supplied issuers only). The dial guard already blocks a
//     redirect INTO the private ranges, but an https -> http downgrade would
//     put the discovery document, and therefore the JWKS location, on a
//     channel an on-path attacker can rewrite.
//
// The fourth constraint is not in the client at all: fetchDiscovery never puts
// the response BODY in an error. go-oidc's own discovery error is
// fmt.Errorf("%s: %s", resp.Status, body) — it embeds the whole body, which
// TestOidcSettings would then hand straight back to the caller. That is the
// difference between "an admin can probe a URL" and "an admin can read it".
const discoveryTimeout = 10 * time.Second

// discoveryMaxBytes caps the discovery document read. Well under any real
// provider's metadata (a few KB) and far under what a hostile endpoint would
// need to be worth streaming.
const discoveryMaxBytes = 1 << 20

// errBlockedAddress is returned by the dial guard. It names the address rather
// than the URL because a redirect chain means those can differ, and the
// address is what the operator needs to see.
var errBlockedAddress = errors.New("address is not a public internet address")

// dialGuard rejects a connection whose resolved address is not publicly
// routable. Returned as net.Dialer.Control, so it runs after resolution and
// before connect, for every hop of a redirect chain.
func dialGuard(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parsing dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("dial address %q is not an IP", host)
	}
	if blockedIP(ip) {
		return fmt.Errorf("%w: %s", errBlockedAddress, ip)
	}
	return nil
}

// blockedIP reports whether ip is somewhere Shepherd must not be steered into
// reaching on an admin's behalf: its own host, the pod network, the service
// network, or the cloud metadata endpoint.
func blockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	// IPv4-mapped IPv6 needs no special case: every helper above unwraps via
	// To4 first, so ::ffff:127.0.0.1 is already caught as loopback.
	if v4 := ip.To4(); v4 != nil {
		for _, block := range extraBlockedV4 {
			if block.Contains(v4) {
				return true
			}
		}
	}
	return false
}

// extraBlockedV4 are IPv4 ranges Go's net helpers do not classify.
// 100.64.0.0/10 is carrier-grade NAT, where several managed Kubernetes
// offerings put pod IPs; 192.0.0.0/24 is IETF protocol assignments. Both are
// reachable from inside a cluster and neither is a place a real OIDC issuer
// lives.
//
//nolint:gochecknoglobals // parsed-once constant table, read-only after init
var extraBlockedV4 = func() []*net.IPNet {
	out := make([]*net.IPNet, 0, 2)
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24"} {
		if _, block, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, block)
		}
	}
	return out
}()

// newDiscoveryClient builds the constrained HTTP client described above.
// guardAddresses selects the dial guard: it is on for any issuer an app admin
// supplied through the UI, and off for one declared in the deployment's own
// configuration — see discoveryClientFor.
func newDiscoveryClient(guardAddresses bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if guardAddresses {
		dialer.Control = dialGuard
	}
	return &http.Client{
		Timeout: discoveryTimeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			MaxIdleConnsPerHost:   2,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			// The https requirement follows the same split: a redirect chain
			// on an admin-supplied issuer must not be downgradable, but an
			// operator who declared a plain-http in-cluster issuer has already
			// chosen that, and refusing its redirects would be refusing their
			// own configuration.
			if guardAddresses && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing to follow a redirect to %s: discovery must stay on https", req.URL.Scheme)
			}
			return nil
		},
	}
}

// The clients are shared: each holds a connection pool, and building one per
// probe would discard it. They are stateless and safe for concurrent use.
//
//nolint:gochecknoglobals // shared HTTP clients with connection pools, immutable after init
var (
	// discoveryClient is the guarded client, used for every issuer an app
	// admin can influence.
	discoveryClient = newDiscoveryClient(true)
	// declaredIssuerClient keeps the timeouts, the body-size cap and the
	// no-body-in-errors rule, and drops only the address guard.
	declaredIssuerClient = newDiscoveryClient(false)
)

// discoveryClientFor picks the client for an issuer that arrived from source.
//
// The address guard exists because an app admin choosing a URL the SERVER
// fetches is an SSRF primitive: app admin is an application role, not
// cluster-admin, so "the caller is already privileged" is not an answer. That
// reasoning applies precisely to SourceDatabase — an issuer typed into
// Admin -> Single sign-on.
//
// It does not apply to SourceHelm. That issuer comes from the deployment's own
// configuration, written by whoever installs the chart, who by definition
// already decides what the process does and what it can reach. Guarding it
// buys nothing and costs a great deal: an in-cluster identity provider — a
// Keycloak or Dex on a Service address, which is one of the most ordinary
// self-hosted setups there is — resolves to a private address every time, so
// the guard made that configuration impossible to boot.
func discoveryClientFor(source string) *http.Client {
	if source == SourceHelm {
		return declaredIssuerClient
	}
	return discoveryClient
}

// discoveryDocument is the subset of OpenID Provider Metadata Shepherd reads.
type discoveryDocument struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	UserInfoEndpoint              string   `json:"userinfo_endpoint"`
	JWKSURI                       string   `json:"jwks_uri"`
	ScopesSupported               []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	IDTokenSigningAlgValues       []string `json:"id_token_signing_alg_values_supported"`
}

// fetchDiscovery retrieves and validates {issuer}/.well-known/openid-configuration.
//
// Every error it returns is safe to show the caller: it reports the status
// code, the URL, or a parse failure, and never the response body. That is the
// whole reason this exists instead of calling oidc.NewProvider and reporting
// its error — see the note on discoveryTimeout.
func fetchDiscovery(ctx context.Context, issuer, source string) (*discoveryDocument, error) {
	return fetchDiscoveryWith(ctx, discoveryClientFor(source), issuer)
}

// fetchDiscoveryWith is fetchDiscovery with the client injected, so the
// no-body-in-errors guarantee can be tested against a real non-200 response.
// The address guard makes httptest (which listens on loopback) unreachable
// through the production client by design, so the only way to exercise the
// status path is to hand in a client without it.
func fetchDiscoveryWith(ctx context.Context, client *http.Client, issuer string) (*discoveryDocument, error) {
	wellKnown := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, fmt.Errorf("building the discovery request for %s: %w", issuer, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			return nil, fmt.Errorf("refusing to fetch %s: it resolves to a private or loopback address, which an identity provider never does", wellKnown)
		}
		// url.Error's message contains the URL and the transport error, never
		// a response body.
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return nil, fmt.Errorf("fetching %s timed out after %s", wellKnown, discoveryTimeout)
		}
		return nil, fmt.Errorf("fetching %s failed: %w", wellKnown, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close on a read-only request

	if resp.StatusCode != http.StatusOK {
		// Status text only. The body is deliberately discarded: it is the
		// exfiltration channel this function exists to close.
		return nil, fmt.Errorf("fetching %s returned %s — check the issuer URL", wellKnown, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, discoveryMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("reading the discovery document from %s: %w", wellKnown, err)
	}
	var doc discoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("%s did not return a valid OpenID discovery document (expected JSON metadata)", wellKnown)
	}
	if doc.Issuer == "" {
		return nil, fmt.Errorf("%s returned JSON with no \"issuer\" field, so it is not an OpenID discovery document", wellKnown)
	}
	if doc.JWKSURI == "" {
		return nil, fmt.Errorf("%s returned a discovery document with no \"jwks_uri\", so ID tokens could not be verified", wellKnown)
	}
	return &doc, nil
}

// newProviderFromDiscovery builds a go-oidc Provider from an already-fetched
// document, rather than letting go-oidc fetch it a second time.
//
// Two things come with doing it this way, and both matter. The provider is
// built against the CONSTRAINED client (ProviderConfig.NewProvider takes it
// from the context), so the JWKS fetches it performs for the rest of its life
// are subject to the same address guard — a hostile discovery document cannot
// point jwks_uri at an internal host. And the issuer-match check that
// oidc.NewProvider performs has to be done here explicitly, because
// ProviderConfig.NewProvider does not do it; skipping it would accept a
// document whose declared issuer differs from the URL it came from, which is
// exactly what go-oidc rejects at verification time with an error nobody sees.
func newProviderFromDiscovery(issuer string, doc *discoveryDocument) (*oidc.Provider, error) {
	if doc.Issuer != issuer {
		return nil, fmt.Errorf("the discovery document at %s declares its issuer as %q; these must match exactly (a trailing slash is the usual cause). Use %q as the issuer URL", issuer, doc.Issuer, doc.Issuer)
	}
	cfg := &oidc.ProviderConfig{
		IssuerURL:   doc.Issuer,
		AuthURL:     doc.AuthorizationEndpoint,
		TokenURL:    doc.TokenEndpoint,
		UserInfoURL: doc.UserInfoEndpoint,
		JWKSURL:     doc.JWKSURI,
		Algorithms:  doc.IDTokenSigningAlgValues,
	}
	// context.Background, not a request context: ProviderConfig.NewProvider
	// uses the context only to pick up the HTTP client, and the provider it
	// returns outlives any one request.
	return cfg.NewProvider(oidc.ClientContext(context.Background(), discoveryClient)), nil
}
