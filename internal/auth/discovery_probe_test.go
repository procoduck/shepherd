package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The SSRF fix has two independent halves and this file proves each one
// separately: the address guard stops the request from leaving, and — for a
// request that IS allowed out — a non-200 response body never reaches the
// error a caller is shown.

func TestDiscoveryRefusesPrivateAddresses(t *testing.T) {
	// httptest listens on 127.0.0.1, which is exactly the address class an
	// admin-supplied issuer must not be able to steer the server into.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"INTERNAL-DATA":"metadata-token-abc123"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	_, err := fetchDiscovery(context.Background(), srv.URL, SourceDatabase)
	if err == nil {
		t.Fatal("fetchDiscovery reached a loopback address; the dial guard is not working")
	}
	if !strings.Contains(err.Error(), "private or loopback") {
		t.Fatalf("expected a blocked-address error, got: %v", err)
	}
	if strings.Contains(err.Error(), "INTERNAL-DATA") {
		t.Fatalf("response body leaked into the error: %v", err)
	}
}

func TestDiscoveryErrorNeverCarriesTheResponseBody(t *testing.T) {
	// A response shaped like something worth stealing. go-oidc's own discovery
	// error is fmt.Errorf("%s: %s", resp.Status, body), so surfacing that
	// through TestOidcSettings would hand this straight back to the caller.
	const secret = "metadata-token-abc123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","message":"secrets is forbidden","INTERNAL":"` + secret + `"}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	// The real code path, with the address guard lifted so loopback is
	// reachable — the guard is what the previous test covers.
	_, err := fetchDiscoveryWith(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for a 403 discovery response")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "secrets is forbidden") {
		t.Fatalf("the response body leaked into the error a caller is shown: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("the error should name the status code so the admin can act on it, got: %v", err)
	}
}

func TestDiscoveryRejectsAJSONBodyThatIsNotAProviderDocument(t *testing.T) {
	// A 200 from some unrelated internal JSON API is the other half of the
	// probe-as-oracle problem: it must not be echoed either.
	const secret = "internal-service-inventory"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":["` + secret + `"]}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()

	_, err := fetchDiscoveryWith(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected an error for JSON with no issuer field")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the response body leaked into the error: %v", err)
	}
}

func TestBlockedIPCoversTheRangesThatMatter(t *testing.T) {
	for _, tc := range []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"169.254.169.254", true}, // cloud metadata endpoint
		{"10.0.0.5", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"100.64.0.1", true}, // CGNAT; some managed-k8s pod networks
		{"fd00::1", true},    // IPv6 unique local
		{"fe80::1", true},    // IPv6 link-local
		{"0.0.0.0", true},
		// Public addresses must still be reachable — an over-broad guard would
		// break every real identity provider, which is a worse outage than the
		// hole it closes.
		{"8.8.8.8", false},
		{"20.190.130.1", false}, // login.microsoftonline.com range
		{"2606:4700::1", false},
	} {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", tc.ip)
		}
		if got := blockedIP(ip); got != tc.blocked {
			t.Errorf("blockedIP(%s) = %v, want %v", tc.ip, got, tc.blocked)
		}
	}
}

// The guard is scoped to issuers an app admin supplied, not to one the
// operator declared in the chart. An in-cluster identity provider resolves to
// a private address every time, so guarding SourceHelm made the most ordinary
// self-hosted setup there is impossible to boot.
func TestDiscoveryAllowsAPrivateAddressForADeclaredIssuer(t *testing.T) {
	// issuer is filled in once the server has an address; the document must
	// name the issuer it was served for or validation rejects it.
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, //nolint:errcheck // test fixture
			`{"issuer":%q,"authorization_endpoint":"%s/auth","token_endpoint":"%s/token","jwks_uri":"%s/keys"}`,
			issuer, issuer, issuer, issuer)
	}))
	defer srv.Close()
	issuer = srv.URL

	doc, err := fetchDiscovery(context.Background(), issuer, SourceHelm)
	if err != nil {
		t.Fatalf("a chart-declared issuer on a private address must be reachable, got: %v", err)
	}
	if doc.Issuer != issuer {
		t.Fatalf("issuer mismatch: got %q, want %q", doc.Issuer, issuer)
	}

	// Same address, same server — refused only because of where the issuer
	// came from.
	if _, err := fetchDiscovery(context.Background(), issuer, SourceDatabase); err == nil {
		t.Fatal("an admin-supplied issuer on a loopback address must still be refused")
	}
}
