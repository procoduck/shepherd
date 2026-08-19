// Package ado mints Azure DevOps Entra client-credentials tokens for one
// service principal.
//
// This is all that remains of the package after F9 step 4
// (docs/git-provider-design.md §3.2 and §5 step 4): the ADO Git REST
// client (ListFiles, DownloadFile, GetLatestCommit) is gone now that
// internal/gitsync's reconciler reads repositories through internal/gitrepo
// instead. internal/gitrepo's ado_sp Auth strategy (AdoSPAuth) is the sole
// caller of TokenProvider, and caches the token it mints.
package ado

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// entraScope is the Azure DevOps resource scope for the Entra
// client-credentials grant (docs/git-provider-design.md §3.2).
const entraScope = "499b84ac-1321-427f-aa17-267ca6975798/.default"

// TokenProvider mints Entra client-credentials tokens for one Azure DevOps
// service principal. internal/gitrepo's ado_sp Auth strategy calls Token
// and caches the result — TokenProvider itself does not cache.
type TokenProvider struct {
	cc clientcredentials.Config
}

// NewTokenProvider builds a TokenProvider that mints tokens from the real
// Entra client-credentials endpoint for tenantID.
func NewTokenProvider(tenantID, clientID, clientSecret string) *TokenProvider {
	return newTokenProvider(entraTokenURL(tenantID), clientID, clientSecret)
}

// NewTokenProviderWithTokenURL builds a TokenProvider against an explicit
// token endpoint, overriding the real Entra endpoint. Intended for tests
// (a mock Entra token endpoint); production callers should use
// NewTokenProvider.
func NewTokenProviderWithTokenURL(tokenURL, clientID, clientSecret string) *TokenProvider {
	return newTokenProvider(tokenURL, clientID, clientSecret)
}

func newTokenProvider(tokenURL, clientID, clientSecret string) *TokenProvider {
	return &TokenProvider{cc: clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		Scopes:       []string{entraScope},
	}}
}

func entraTokenURL(tenantID string) string {
	return fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
}

// Token mints a fresh Entra client-credentials token; it does not cache.
// Every production caller needs caching (a fresh token on every git
// operation would hammer Entra), so callers should mint through
// internal/gitrepo.TokenCache rather than calling Token directly per
// request — see internal/gitrepo's AdoSPAuth.
func (p *TokenProvider) Token(ctx context.Context) (*oauth2.Token, error) {
	tok, err := p.cc.TokenSource(ctx).Token()
	if err != nil {
		return nil, fmt.Errorf("ado: minting entra token: %w", err)
	}
	return tok, nil
}
