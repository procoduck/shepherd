// mockmsft is a minimal mock server for Microsoft Entra ID / Graph and the
// two OAuth-shaped token-exchange endpoints Shepherd's ado_sp and
// github_app git auth strategies talk to (docs/git-provider-design.md §4).
// It never speaks git itself — a real Gitea instance in the compose stack
// does that — so it serves only:
//
//   - GET  /v1.0/me/transitiveMemberOf/microsoft.graph.group → user group memberships
//   - GET  /v1.0/groups?$filter=...                           → group search
//   - POST /{tenant}/oauth2/v2.0/token                        → mock Entra client-credentials
//     token endpoint for the ado_sp strategy
//   - POST /app/installations/{id}/access_tokens              → mock GitHub App installation
//     token endpoint for the github_app strategy; verifies the RS256 app JWT before minting
//   - POST /__fixture                                         → control endpoint for tests
//
// Both token endpoints return a token value the test configured via
// /__fixture — in practice a real Gitea personal access token, so the
// strategy under test ends up presenting genuine Gitea credentials to git
// once the (mocked) token exchange completes. This proves the acquisition
// and plumbing of each strategy without needing real Entra or GitHub.
package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type groupsStore struct {
	mu     sync.RWMutex
	byUser map[string][]map[string]string // token → []group
	all    []map[string]string
}

// entraStore holds the single access token the mock Entra token endpoint
// returns for any tenant/client — e2e only ever needs one at a time, set
// via the "entra_token" fixture kind.
type entraStore struct {
	mu    sync.RWMutex
	token string
}

// githubAppStore holds what the mock GitHub App installation-token
// endpoint needs to verify the caller's JWT and what to return once it
// checks out — set via the "github_app" fixture kind.
type githubAppStore struct {
	mu    sync.RWMutex
	appID string
	pub   *rsa.PublicKey
	token string
}

var (
	groups = &groupsStore{byUser: make(map[string][]map[string]string)}
	entra  = &entraStore{}
	ghApp  = &githubAppStore{}
)

func main() {
	// Health-check mode: just probe the running server and exit.
	if len(os.Args) > 1 && os.Args[1] == "-health-check" {
		resp, err := http.Get("http://localhost:9090/health")
		if err != nil {
			log.Fatal(err)
		}
		resp.Body.Close() //nolint:errcheck // health-check probe exits immediately after
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("unhealthy: %d", resp.StatusCode)
		}
		return
	}

	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = ":9090"
	}

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // http write after header
	})

	// Control endpoint — tests use this to seed state.
	mux.HandleFunc("POST /__fixture", handleFixture)

	// Graph: transitive member groups
	mux.HandleFunc("GET /v1.0/me/transitiveMemberOf/microsoft.graph.group", handleTransitiveMemberOf)
	// Graph: group search
	mux.HandleFunc("GET /v1.0/groups", handleGroupSearch)

	// Entra: client-credentials token endpoint (ado_sp strategy).
	mux.HandleFunc("POST /{tenant}/oauth2/v2.0/token", handleEntraToken)
	// GitHub App: installation access-token endpoint (github_app strategy).
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", handleGitHubAppToken)

	log.Printf("mockmsft listening on %s", listen)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatal(err)
	}
}

type fixtureRequest struct {
	Kind string         `json:"kind"`
	Data map[string]any `json:"data"`
}

func handleFixture(w http.ResponseWriter, r *http.Request) {
	var req fixtureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch req.Kind {
	case "groups":
		gs := toGroupList(req.Data["groups"])
		groups.mu.Lock()
		groups.all = gs
		if user, ok := req.Data["user"].(string); ok {
			groups.byUser[user] = gs
		}
		groups.mu.Unlock()

	case "entra_token":
		token, _ := req.Data["token"].(string) //nolint:errcheck // fixture helper accepts missing values
		entra.mu.Lock()
		entra.token = token
		entra.mu.Unlock()

	case "github_app":
		appID, _ := req.Data["app_id"].(string)          //nolint:errcheck // fixture helper accepts missing values
		pubPEM, _ := req.Data["public_key_pem"].(string) //nolint:errcheck // fixture helper accepts missing values
		token, _ := req.Data["token"].(string)           //nolint:errcheck // fixture helper accepts missing values
		pub, err := parseRSAPublicKey(pubPEM)
		if err != nil {
			http.Error(w, "invalid public_key_pem: "+err.Error(), http.StatusBadRequest)
			return
		}
		ghApp.mu.Lock()
		ghApp.appID = appID
		ghApp.pub = pub
		ghApp.token = token
		ghApp.mu.Unlock()

	default:
		http.Error(w, "unknown fixture kind", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleTransitiveMemberOf(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")

	groups.mu.RLock()
	gs, ok := groups.byUser[token]
	if !ok {
		gs = groups.all
	}
	groups.mu.RUnlock()

	respondJSON(w, map[string]any{"value": gs, "@odata.count": len(gs)})
}

func handleGroupSearch(w http.ResponseWriter, r *http.Request) {
	groups.mu.RLock()
	gs := groups.all
	groups.mu.RUnlock()

	q := r.URL.Query().Get("$filter")
	var filtered []map[string]string
	for _, g := range gs {
		if q == "" || strings.Contains(strings.ToLower(g["displayName"]), strings.ToLower(q)) {
			filtered = append(filtered, g)
		}
	}
	respondJSON(w, map[string]any{"value": filtered})
}

// handleEntraToken mimics the Entra client-credentials grant
// (POST /{tenant}/oauth2/v2.0/token) that internal/ado.TokenProvider talks
// to for the ado_sp strategy. It does not validate client_id/client_secret
// — the e2e suite's ado_sp coverage is about proving the mint→use plumbing
// with a single pre-seeded token, not re-testing Entra's own auth — but it
// does require grant_type=client_credentials, matching the real endpoint's
// contract closely enough to catch a request built the wrong shape.
func handleEntraToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parsing form: "+err.Error(), http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		http.Error(w, "unsupported grant_type", http.StatusBadRequest)
		return
	}

	entra.mu.RLock()
	token := entra.token
	entra.mu.RUnlock()
	if token == "" {
		http.Error(w, "no entra_token fixture seeded", http.StatusBadRequest)
		return
	}

	respondJSON(w, map[string]any{
		"token_type":     "Bearer",
		"expires_in":     3600,
		"ext_expires_in": 3600,
		"access_token":   token,
	})
}

// handleGitHubAppToken mimics GitHub's installation access-token endpoint
// (POST /app/installations/{id}/access_tokens). Unlike handleEntraToken,
// it actually verifies the bearer JWT — RS256 signature, issuer, and
// lifetime — against the key and app id seeded via the "github_app"
// fixture, per docs/git-provider-design.md §4 ("the JWT assertion is the
// part worth testing"). A bad JWT gets a 401 with a diagnostic body, not a
// token.
func handleGitHubAppToken(w http.ResponseWriter, r *http.Request) {
	authz := r.Header.Get("Authorization")
	jwt := strings.TrimPrefix(authz, "Bearer ")
	if jwt == "" || jwt == authz {
		http.Error(w, "missing bearer JWT", http.StatusUnauthorized)
		return
	}

	ghApp.mu.RLock()
	appID, pub, token := ghApp.appID, ghApp.pub, ghApp.token
	ghApp.mu.RUnlock()
	if pub == nil {
		http.Error(w, "no github_app fixture seeded", http.StatusBadRequest)
		return
	}

	if err := verifyGitHubAppJWT(jwt, appID, pub); err != nil {
		http.Error(w, "invalid app jwt: "+err.Error(), http.StatusUnauthorized)
		return
	}

	// GitHub's real endpoint returns 201 Created on success (its own docs
	// spell this out), and internal/gitrepo/github_app.go's mint checks
	// for exactly that status — a plain 200 here would make every
	// github_app case fail with a wrong-status error, not the token flow
	// it's meant to exercise.
	respondJSONStatus(w, http.StatusCreated, map[string]any{
		"token":      token,
		"expires_at": time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
	})
}

// verifyGitHubAppJWT checks that jwt is a well-formed RS256 JWT, signed by
// pub, whose "iss" claim equals wantAppID, and whose iat/exp window is
// sane (exp after iat, lifetime <= 10 minutes — GitHub's own ceiling —
// and now falls within [iat, exp] allowing a minute of clock skew either
// side). It mirrors the construction in internal/gitrepo/github_app.go's
// signGitHubAppJWT — this is that function's counterpart, verifying
// exactly what it signs.
func verifyGitHubAppJWT(jwt, wantAppID string, pub *rsa.PublicKey) error {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return errors.New("not a three-part JWT")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("decoding header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return fmt.Errorf("parsing header: %w", err)
	}
	if header.Alg != "RS256" {
		return fmt.Errorf("unsupported alg %q, want RS256", header.Alg)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("decoding signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("decoding claims: %w", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return fmt.Errorf("parsing claims: %w", err)
	}
	if claims.Iss != wantAppID {
		return fmt.Errorf("iss %q does not match configured app id %q", claims.Iss, wantAppID)
	}

	iat := time.Unix(claims.Iat, 0)
	exp := time.Unix(claims.Exp, 0)
	const skew = 60 * time.Second
	const maxLifetime = 10 * time.Minute
	if !exp.After(iat) {
		return errors.New("exp is not after iat")
	}
	if exp.Sub(iat) > maxLifetime {
		return fmt.Errorf("lifetime %s exceeds GitHub's %s ceiling", exp.Sub(iat), maxLifetime)
	}
	now := time.Now()
	if now.Before(iat.Add(-skew)) {
		return errors.New("token not yet valid (iat in the future)")
	}
	if now.After(exp.Add(skew)) {
		return errors.New("token expired")
	}
	return nil
}

func parseRSAPublicKey(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	// PKIX ("PUBLIC KEY") is what x509.MarshalPKIXPublicKey produces; also
	// accept a bare PKCS1 ("RSA PUBLIC KEY") block for convenience.
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("PKIX key is not RSA")
		}
		return rsaKey, nil
	}
	return x509.ParsePKCS1PublicKey(block.Bytes)
}

func respondJSON(w http.ResponseWriter, v any) {
	respondJSONStatus(w, http.StatusOK, v)
}

func respondJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // http write after header
}

func toGroupList(v any) []map[string]string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]string, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			g := make(map[string]string)
			for k, val := range m {
				if s, ok := val.(string); ok {
					g[k] = s
				}
			}
			out = append(out, g)
		}
	}
	return out
}
