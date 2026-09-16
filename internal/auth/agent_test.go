package auth_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shepherd/internal/auth"
	"shepherd/internal/config"
)

// testIssuer is a minimal OIDC issuer: it serves a discovery document and a
// JWKS built from a real RSA key, and signs RS256 tokens. It exists so
// VerifyAgentToken can be driven through the real go-oidc provider + JWKS
// fetch path without a live IdP or a new signing dependency (tokens are signed
// by hand with crypto/rsa, the same shape the repo's other OIDC tests use).
type testIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	ti := &testIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fixture
			"issuer":                                ti.srv.URL,
			"authorization_endpoint":                ti.srv.URL + "/authorize",
			"token_endpoint":                        ti.srv.URL + "/token",
			"jwks_uri":                              ti.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		eb := make([]byte, 4)
		binary.BigEndian.PutUint32(eb, uint32(key.E)) //nolint:gosec // small exponent
		e := base64.RawURLEncoding.EncodeToString(eb[1:])
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // test fixture
			"keys": []map[string]any{{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test", "n": n, "e": e}},
		})
	})
	ti.srv = httptest.NewServer(mux)
	t.Cleanup(ti.srv.Close)
	return ti
}

func (ti *testIssuer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	b64 := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signing := b64(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test"}) + "." + b64(claims)
	h := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ti.key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// handlerFor builds a Handler wired to the test issuer via chart config
// (SourceHelm → unguarded client, so the httptest loopback issuer is allowed)
// and reloads it so the agent verifier is live.
func handlerFor(t *testing.T, ti *testIssuer, oc config.OIDCConfig) *auth.Handler {
	t.Helper()
	oc.Issuer = ti.srv.URL
	oc.ClientID = "shepherd-login"
	cfg := &config.Config{OIDC: oc}
	h := auth.NewChartOIDCTestHandler(cfg)
	if err := h.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !h.AgentOIDCEnabled() {
		t.Fatal("agent OIDC should be enabled after reload")
	}
	return h
}

func baseClaims(ti *testIssuer, aud string) map[string]any {
	return map[string]any{
		"iss": ti.srv.URL,
		"sub": "alloy-prod-eu",
		"aud": aud,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Add(-time.Minute).Unix(),
	}
}

func TestVerifyAgentToken(t *testing.T) {
	ti := newTestIssuer(t)
	ctx := context.Background()

	t.Run("valid token returns claims", func(t *testing.T) {
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors"})
		claims := baseClaims(ti, "shepherd-collectors")
		claims["roles"] = []any{"Collector.Poll"}
		claims["shepherd_clusters"] = []any{"prod-eu-1", "staging-eu-1"}
		ac, err := h.VerifyAgentToken(ctx, ti.sign(t, claims))
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if ac.Subject != "alloy-prod-eu" || ac.AppID != "alloy-prod-eu" {
			t.Errorf("subject/app id: %+v", ac)
		}
		if len(ac.Roles) != 1 || ac.Roles[0] != "Collector.Poll" {
			t.Errorf("roles: %+v", ac.Roles)
		}
	})

	t.Run("clusters claim is read when configured", func(t *testing.T) {
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors", AgentClustersClaim: "shepherd_clusters"})
		claims := baseClaims(ti, "shepherd-collectors")
		claims["shepherd_clusters"] = []any{"prod-eu-1"}
		ac, err := h.VerifyAgentToken(ctx, ti.sign(t, claims))
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if len(ac.Clusters) != 1 || ac.Clusters[0] != "prod-eu-1" {
			t.Errorf("clusters: %+v", ac.Clusters)
		}
	})

	t.Run("wrong audience is refused", func(t *testing.T) {
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors"})
		_, err := h.VerifyAgentToken(ctx, ti.sign(t, baseClaims(ti, "some-other-api")))
		if err == nil {
			t.Fatal("a token for another audience must be refused")
		}
	})

	t.Run("a user login token is refused as an agent token", func(t *testing.T) {
		// aud == ClientID is the user login shape; the agent verifier must not
		// accept it.
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors"})
		_, err := h.VerifyAgentToken(ctx, ti.sign(t, baseClaims(ti, "shepherd-login")))
		if err == nil {
			t.Fatal("a login-audience token must be refused for agent auth")
		}
	})

	t.Run("expired token is refused", func(t *testing.T) {
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors"})
		claims := baseClaims(ti, "shepherd-collectors")
		claims["exp"] = time.Now().Add(-time.Hour).Unix()
		_, err := h.VerifyAgentToken(ctx, ti.sign(t, claims))
		if err == nil {
			t.Fatal("an expired token must be refused")
		}
	})

	t.Run("a token signed by a different key is refused", func(t *testing.T) {
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors"})
		other := newTestIssuer(t)
		claims := baseClaims(ti, "shepherd-collectors") // right issuer/aud...
		_, err := h.VerifyAgentToken(ctx, other.sign(t, claims))
		if err == nil {
			t.Fatal("a bad signature must be refused")
		}
	})

	t.Run("D0 grant gate: required role must be present", func(t *testing.T) {
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors", AgentRequiredRole: "Collector.Poll"})

		missing := baseClaims(ti, "shepherd-collectors")
		missing["roles"] = []any{"SomethingElse"}
		if _, err := h.VerifyAgentToken(ctx, ti.sign(t, missing)); !errors.Is(err, auth.ErrAgentGrantMissing) {
			t.Fatalf("a token without the required role must be refused, got %v", err)
		}

		present := baseClaims(ti, "shepherd-collectors")
		present["roles"] = []any{"Collector.Poll", "Other"}
		if _, err := h.VerifyAgentToken(ctx, ti.sign(t, present)); err != nil {
			t.Fatalf("a token with the required role must pass: %v", err)
		}
	})

	t.Run("D0 grant gate: required scope from scp", func(t *testing.T) {
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors", AgentRequiredScope: "collector.poll"})

		missing := baseClaims(ti, "shepherd-collectors")
		missing["scp"] = "other.scope"
		if _, err := h.VerifyAgentToken(ctx, ti.sign(t, missing)); !errors.Is(err, auth.ErrAgentGrantMissing) {
			t.Fatalf("missing scope must be refused, got %v", err)
		}

		present := baseClaims(ti, "shepherd-collectors")
		present["scp"] = "openid collector.poll"
		if _, err := h.VerifyAgentToken(ctx, ti.sign(t, present)); err != nil {
			t.Fatalf("present scope (space-delimited scp) must pass: %v", err)
		}
	})

	t.Run("azp app claim overrides sub for the binding key", func(t *testing.T) {
		h := handlerFor(t, ti, config.OIDCConfig{AgentAudience: "shepherd-collectors", AgentAppClaim: "azp"})
		claims := baseClaims(ti, "shepherd-collectors")
		claims["azp"] = "client-1234"
		ac, err := h.VerifyAgentToken(ctx, ti.sign(t, claims))
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if ac.AppID != "client-1234" {
			t.Errorf("app id should come from azp, got %q", ac.AppID)
		}
	})

	t.Run("disabled when no audience is configured", func(t *testing.T) {
		cfg := &config.Config{OIDC: config.OIDCConfig{Issuer: ti.srv.URL, ClientID: "shepherd-login"}}
		h := auth.NewChartOIDCTestHandler(cfg)
		if err := h.Reload(ctx); err != nil {
			t.Fatalf("reload: %v", err)
		}
		if h.AgentOIDCEnabled() {
			t.Fatal("agent OIDC must be off with no audience")
		}
		if _, err := h.VerifyAgentToken(ctx, ti.sign(t, baseClaims(ti, "shepherd-collectors"))); !errors.Is(err, auth.ErrAgentOIDCUnavailable) {
			t.Fatalf("want ErrAgentOIDCUnavailable, got %v", err)
		}
	})
}
