package gitrepo

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

const (
	// githubAppDefaultAPIBase is used when GitHubAppAuth.APIBase is empty.
	githubAppDefaultAPIBase = "https://api.github.com"
	// githubAppUsername is GitHub's fixed username for basic-auth requests
	// authenticated with an installation access token.
	githubAppUsername = "x-access-token"
	// githubAppJWTLifetime bounds exp-iat. GitHub rejects app JWTs with a
	// lifetime over 10 minutes; this stays comfortably under that ceiling.
	githubAppJWTLifetime = 9 * time.Minute
	// githubAppClockSkew backdates iat so a small amount of clock drift
	// between this process and GitHub's servers doesn't make a
	// just-issued JWT look like it isn't valid yet.
	githubAppClockSkew = 60 * time.Second
)

// GitHubAppAuth authenticates as a GitHub App installation: it signs a
// short-lived RS256 JWT with the app's private key (iss = AppID, lifetime
// <= githubAppJWTLifetime), exchanges it for an installation access token
// via POST {api_base}/app/installations/{installation_id}/access_tokens,
// and sends that token as the HTTP Basic password with username
// "x-access-token" — GitHub's convention for installation tokens
// (docs/git-provider-design.md §3.2). The installation token is cached in
// Cache, keyed by CredentialID, refreshing shortly before its ~1 hour
// expiry.
type GitHubAppAuth struct {
	// CredentialID keys the shared TokenCache. It MUST be unique per
	// stored credential and stable across calls for the same credential.
	CredentialID string
	// AppID is the GitHub App's id, sent as the JWT "iss" claim.
	AppID string
	// InstallationID is the installation to mint a token for.
	InstallationID string
	// PrivateKeyPEM is the app's RSA private key (PKCS#1 or PKCS#8 PEM —
	// both forms GitHub's app settings page offers for download).
	PrivateKeyPEM []byte
	// APIBase is the GitHub API base URL. Empty defaults to
	// https://api.github.com; set to https://<host>/api/v3 for GitHub
	// Enterprise Server.
	APIBase string
	// Cache is the shared token-exchange cache (see TokenCache). Required
	// — both ado_sp and github_app credentials should share one Cache
	// instance, keyed by their distinct credential ids.
	Cache *TokenCache
	// HTTPClient issues the installation-token request. Nil uses
	// http.DefaultClient; tests point this at an httptest server.
	HTTPClient *http.Client
}

// HTTP implements Auth.
func (a GitHubAppAuth) HTTP(ctx context.Context) (string, string, bool, error) {
	token, err := a.Cache.Get(ctx, a.CredentialID, a.mint)
	if err != nil {
		return "", "", false, err
	}
	return githubAppUsername, token, true, nil
}

// SSH implements Auth: GitHub Apps are an HTTPS-only strategy.
func (GitHubAppAuth) SSH(context.Context) (gossh.Signer, bool, error) { return nil, false, nil }

func (a GitHubAppAuth) apiBase() string {
	if a.APIBase != "" {
		return strings.TrimRight(a.APIBase, "/")
	}
	return githubAppDefaultAPIBase
}

func (a GitHubAppAuth) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return http.DefaultClient
}

// mint signs a fresh app JWT and exchanges it for an installation access
// token. It satisfies TokenMinter.
func (a GitHubAppAuth) mint(ctx context.Context) (string, time.Time, error) {
	jwt, err := signGitHubAppJWT(a.AppID, a.PrivateKeyPEM, time.Now())
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gitrepo: github app: signing jwt: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", a.apiBase(), a.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gitrepo: github app: building installation token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gitrepo: github app: requesting installation token: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // close error on a body we already fully read/discarded is not actionable

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048)) //nolint:errcheck // best-effort diagnostic
		return "", time.Time{}, fmt.Errorf("gitrepo: github app: installation token request: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", time.Time{}, fmt.Errorf("gitrepo: github app: decoding installation token response: %w", err)
	}
	if result.Token == "" {
		return "", time.Time{}, fmt.Errorf("gitrepo: github app: installation token response had no token")
	}
	return result.Token, result.ExpiresAt, nil
}

// signGitHubAppJWT builds and RS256-signs a GitHub App JWT: header
// {"alg":"RS256","typ":"JWT"}, claims {"iss":appID,"iat","exp"}. iat is
// backdated by githubAppClockSkew to tolerate clock drift, per GitHub's
// own guidance; exp-iat never exceeds githubAppJWTLifetime, comfortably
// under GitHub's 10 minute maximum.
func signGitHubAppJWT(appID string, keyPEM []byte, now time.Time) (string, error) {
	key, err := parseRSAPrivateKey(keyPEM)
	if err != nil {
		return "", fmt.Errorf("parsing private key: %w", err)
	}

	iat := now.Add(-githubAppClockSkew)
	exp := iat.Add(githubAppJWTLifetime)

	headerJSON, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", fmt.Errorf("encoding jwt header: %w", err)
	}
	claimsJSON, err := json.Marshal(map[string]any{
		"iss": appID,
		"iat": iat.Unix(),
		"exp": exp.Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("encoding jwt claims: %w", err)
	}

	signingInput := base64URLEncode(headerJSON) + "." + base64URLEncode(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing jwt: %w", err)
	}

	return signingInput + "." + base64URLEncode(sig), nil
}

func base64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseRSAPrivateKey accepts PKCS#1 ("RSA PRIVATE KEY") or PKCS#8
// ("PRIVATE KEY") PEM-encoded RSA private keys — the two formats GitHub's
// app settings page offers when generating a private key.
func parseRSAPrivateKey(keyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a PKCS#1 or PKCS#8 RSA private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return rsaKey, nil
}
