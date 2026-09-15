package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// The first OIDC login on the kind dev stack (2026-09-15) reached the
// provider on a Service address, exchanged the code, and then failed with
// "fetching keys ... address is not a public internet address": discovery
// used the per-source client (a chart-declared issuer is allowed to be
// private), but the go-oidc Provider built from that document — and so every
// JWKS fetch for the rest of its life — used the guarded client regardless of
// source. The provider must be built against the same client that was allowed
// to fetch the discovery document.
func TestProviderKeyFetchFollowsTheIssuerSource(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fixture
				"issuer":                                srv.URL,
				"authorization_endpoint":                srv.URL + "/authorize",
				"token_endpoint":                        srv.URL + "/token",
				"jwks_uri":                              srv.URL + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			_, _ = w.Write([]byte(`{"keys":[]}`)) //nolint:errcheck // test fixture
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// A token that parses far enough for the verifier to go and fetch keys:
	// valid header and claims, garbage signature. The JWKS above is empty, so
	// verification fails either way — what differs is WHERE it fails.
	b64 := func(v any) string {
		raw, _ := json.Marshal(v) //nolint:errcheck // test fixture
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	token := b64(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." +
		b64(map[string]any{"iss": srv.URL, "aud": "cid", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}) +
		".c2ln"

	// Discovery is fetched with the guard lifted in both cases (an
	// admin-supplied loopback issuer would already be refused there, which
	// TestDiscoveryRefusesPrivateAddresses covers); what this test isolates is
	// the client the PROVIDER keeps for its key fetches.
	verifyWith := func(t *testing.T, source string) error {
		t.Helper()
		doc, err := fetchDiscoveryWith(context.Background(), declaredIssuerClient, srv.URL)
		if err != nil {
			t.Fatalf("discovery: %v", err)
		}
		provider, err := newProviderFromDiscovery(srv.URL, doc, discoveryClientFor(source))
		if err != nil {
			t.Fatalf("provider: %v", err)
		}
		_, err = provider.Verifier(&oidc.Config{ClientID: "cid"}).Verify(context.Background(), token)
		return err
	}

	t.Run("a chart-declared loopback issuer can fetch its keys", func(t *testing.T) {
		err := verifyWith(t, SourceHelm)
		if err == nil {
			t.Fatal("expected verification to fail on the empty JWKS")
		}
		if strings.Contains(err.Error(), errBlockedAddress.Error()) {
			t.Fatalf("the key fetch was blocked by the address guard although the issuer is chart-declared: %v", err)
		}
	})

	t.Run("an admin-supplied loopback issuer is still guarded at the key fetch", func(t *testing.T) {
		err := verifyWith(t, SourceDatabase)
		if err == nil || !strings.Contains(err.Error(), errBlockedAddress.Error()) {
			t.Fatalf("expected the address guard to block the key fetch, got: %v", err)
		}
	})
}
