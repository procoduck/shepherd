package auth

import (
	"golang.org/x/oauth2"

	"shepherd/internal/config"
)

// NewOIDCTestHandler builds a Handler around a static oauth2 config so the
// PKCE spec can drive LoginHandler without live OIDC discovery — only the
// redirect construction is exercised, which never touches the provider.
func NewOIDCTestHandler(cfg *config.Config, o *oauth2.Config) *Handler {
	return &Handler{cfg: cfg, oauth2: o}
}
