package auth

import (
	"context"
	"log/slog"

	"golang.org/x/oauth2"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/store"
)

// Unexported claim readers, exposed so the provider-shape matrix they exist
// for (array / single string / space-separated groups claims) can be driven
// directly rather than through a live token exchange.
var (
	ClaimString  = claimString
	ClaimStrings = claimStrings
)

// ResolveGroups exposes the group-resolution decision — Graph vs the ID token
// claim, and the fallback between them — which is what actually decides a
// session's authorization and had no direct coverage.
func (h *Handler) ResolveGroups(ctx context.Context, s Settings, claims map[string]any, accessToken, subject string) []string {
	return h.resolveGroups(ctx, s, claims, accessToken, subject)
}

// NewSettingsTestHandler builds a Handler wired to a real settings store but
// with no runtime, so precedence and persistence can be exercised without
// reaching a live identity provider.
func NewSettingsTestHandler(cfg *config.Config, st *store.Store, enc *crypto.Encryptor) *Handler {
	return &Handler{store: st, cfg: cfg, logger: slog.Default(), settings: NewSettingsStore(st, enc)}
}

// NewGroupsTestHandler builds a Handler with nothing but a logger — enough for
// ResolveGroups, which touches no store and no runtime.
func NewGroupsTestHandler() *Handler {
	return &Handler{logger: slog.New(slog.DiscardHandler)}
}

// NewOIDCTestHandler builds a Handler around a static oauth2 config so the
// PKCE spec can drive LoginHandler without live OIDC discovery — only the
// redirect construction is exercised, which never touches the provider.
//
// The runtime is installed directly (rather than through Reload) for the same
// reason: Reload's only path to a runtime is discovery, which is precisely
// what this helper exists to avoid.
func NewOIDCTestHandler(cfg *config.Config, o *oauth2.Config) *Handler {
	h := &Handler{cfg: cfg, logger: slog.Default()}
	h.rt.Store(&oidcRuntime{
		settings: Settings{Enabled: true, ClientID: o.ClientID, Issuer: "https://issuer.test", Source: SourceHelm},
		oauth2:   o,
	})
	return h
}
