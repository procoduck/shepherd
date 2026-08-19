package gitrepo

import (
	"context"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"shepherd/internal/ado"
)

// AdoSPAuth authenticates against Azure DevOps using an Entra service
// principal's client-credentials token as the HTTP Basic password. The
// token is minted by internal/ado.TokenProvider — the Entra
// client-credentials exchange this strategy reuses rather than
// reimplementing (docs/git-provider-design.md §3.2) — and cached in Cache,
// keyed by CredentialID, refreshing shortly before expiry rather than on
// every git operation.
type AdoSPAuth struct {
	// CredentialID keys the shared TokenCache. It MUST be unique per
	// stored credential and stable across calls for the same credential,
	// so unrelated credentials never share a cached token.
	CredentialID string
	// TenantID, ClientID, ClientSecret identify the Entra service
	// principal.
	TenantID     string
	ClientID     string
	ClientSecret string
	// TokenURL overrides the Entra token endpoint. Empty uses the real
	// login.microsoftonline.com endpoint for TenantID; tests point this at
	// an httptest server standing in for Entra.
	TokenURL string
	// Cache is the shared token-exchange cache (see TokenCache). Required
	// — both ado_sp and github_app credentials should share one Cache
	// instance, keyed by their distinct credential ids.
	Cache *TokenCache
}

// HTTP implements Auth: the minted token is sent as the HTTP Basic
// password with an empty username, matching how git over HTTPS accepts an
// OAuth bearer token through the basic-auth channel.
func (a AdoSPAuth) HTTP(ctx context.Context) (string, string, bool, error) {
	token, err := a.Cache.Get(ctx, a.CredentialID, a.mint)
	if err != nil {
		return "", "", false, err
	}
	return "", token, true, nil
}

// SSH implements Auth: Azure DevOps service principals are an HTTPS-only
// strategy.
func (AdoSPAuth) SSH(context.Context) (gossh.Signer, bool, error) { return nil, false, nil }

func (a AdoSPAuth) mint(ctx context.Context) (string, time.Time, error) {
	var provider *ado.TokenProvider
	if a.TokenURL != "" {
		provider = ado.NewTokenProviderWithTokenURL(a.TokenURL, a.ClientID, a.ClientSecret)
	} else {
		provider = ado.NewTokenProvider(a.TenantID, a.ClientID, a.ClientSecret)
	}

	tok, err := provider.Token(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	return tok.AccessToken, tok.Expiry, nil
}
