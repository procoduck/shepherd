package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// ErrHelmManaged is returned by SaveSettings and DeleteSettings when the Helm
// chart supplied an issuer. The chart wins, so writing the row would store a
// configuration that is never read — a change that appears to succeed and
// then does nothing, which is worse than a refusal.
var ErrHelmManaged = errors.New("auth: OIDC is configured by the Helm chart (oidc.issuer); change it there, not here")

// Discovery is what one OIDC discovery probe found. It exists so an admin can
// confirm they are pointed at the right tenant, realm, or user pool BEFORE
// sign-in depends on it — every field here is something that is either
// invisible or unrecoverable once a wrong provider is live.
type Discovery struct {
	// Issuer is the issuer the discovery document declares, which is not
	// always the URL it was fetched from. go-oidc rejects a mismatch at login
	// time, so surfacing it here turns the single most common OIDC
	// misconfiguration into a message an admin reads while they can still fix
	// it.
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	JWKSURI               string
	// SupportedScopes is the document's scopes_supported, and MissingScopes
	// is the requested scopes it does not list. Advisory only: scopes_supported
	// is optional in the spec and widely under-reported, so this must never be
	// promoted to a hard failure.
	SupportedScopes []string
	MissingScopes   []string
	// SupportsPKCE reports whether S256 appears in
	// code_challenge_methods_supported. LoginHandler always sends a PKCE
	// challenge, so a provider that does not advertise support is worth
	// flagging.
	SupportsPKCE bool
	// IssuerMismatch is set when the document declares an issuer different
	// from the URL it was fetched from — trailing slashes, Auth0 vs Okta URL
	// shapes. go-oidc rejects this at login time with an error no one sees, so
	// the probe is the only place it can be caught while it is still cheap.
	IssuerMismatch string
}

// HelmManaged reports whether the Helm chart / environment owns the OIDC
// configuration, making the UI-managed row read-only.
func (h *Handler) HelmManaged() bool {
	return h.cfg != nil && h.cfg.OIDC.Issuer != ""
}

// SettingsAvailable reports whether UI-managed settings can be read or written
// at all — false when there is no store (local-admin-only test wiring).
func (h *Handler) SettingsAvailable() bool { return h.settings != nil }

// EffectiveSettings returns the configuration currently in force, from
// whichever source won, or (nil, nil) when neither source has one. The client
// secret is populated; callers rendering it to a user must not include it (the
// admin API answers client_secret_set instead).
func (h *Handler) EffectiveSettings(ctx context.Context) (*Settings, error) {
	return h.resolveSettings(ctx)
}

// StatusMessage explains, in one sentence, any state where what an admin sees
// is not what is actually happening. Empty when the configuration is behaving
// as it reads.
func (h *Handler) StatusMessage(s *Settings) string {
	switch {
	case h.HelmManaged():
		return "This provider is configured by the Helm chart (oidc.issuer). Change it in your chart values; it cannot be edited here."
	case s == nil:
		return ""
	case s.Enabled && !h.OIDCEnabled():
		return "Saved and enabled, but no provider is active — OIDC discovery against the issuer is failing. Use Test connection for the exact error."
	case !s.Enabled:
		return "Saved but not enabled. Users cannot sign in through this provider until you enable it."
	default:
		return ""
	}
}

// TestSettings runs OIDC discovery against a candidate configuration without
// storing it. in is normalized first, so the probe runs against exactly the
// values a save would persist rather than against the raw form input.
func (h *Handler) TestSettings(ctx context.Context, in *Settings) (*Discovery, error) {
	candidate := *in
	candidate.Normalize()
	if err := validateIssuer(candidate.Issuer); err != nil {
		return nil, err
	}
	doc, err := fetchDiscovery(ctx, candidate.Issuer)
	if err != nil {
		return nil, err
	}
	// Reported, not enforced: an issuer mismatch is fatal at login time, but a
	// probe that refused to return the document would withhold the one value
	// that tells the admin what to put in the field instead.
	result := &Discovery{
		Issuer:                doc.Issuer,
		AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint:         doc.TokenEndpoint,
		JWKSURI:               doc.JWKSURI,
		SupportedScopes:       doc.ScopesSupported,
		SupportsPKCE:          slices.Contains(doc.CodeChallengeMethodsSupported, "S256"),
	}
	if doc.Issuer != candidate.Issuer {
		result.IssuerMismatch = fmt.Sprintf("The discovery document declares its issuer as %q, which must match the issuer URL exactly. Use that value instead.", doc.Issuer)
	}
	if len(doc.ScopesSupported) > 0 {
		for _, want := range candidate.Scopes {
			if !slices.Contains(doc.ScopesSupported, want) {
				result.MissingScopes = append(result.MissingScopes, want)
			}
		}
	}
	return result, nil
}

// SaveSettings validates, discovers, stores, and activates a configuration.
//
// Discovery runs BEFORE the write whenever the configuration is being enabled.
// Saving first and discovering after would leave the deployment advertising a
// sign-in button that dead-ends, and the admin who could fix it may be relying
// on that same login page. A configuration saved with enabled=false skips the
// probe, so a half-finished provider can still be parked.
func (h *Handler) SaveSettings(ctx context.Context, in *Settings, actor string) (*Settings, error) {
	if h.HelmManaged() {
		return nil, ErrHelmManaged
	}
	if h.settings == nil {
		return nil, ErrNoSettings
	}
	candidate := *in
	candidate.Normalize()
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if candidate.Enabled {
		if _, err := h.TestSettings(ctx, &candidate); err != nil {
			return nil, err
		}
	}
	saved, err := h.settings.Save(ctx, &candidate, actor)
	if err != nil {
		return nil, err
	}
	if err := h.Reload(ctx); err != nil {
		// The row is stored and valid; only activation failed, and the next
		// refresh retries it. Report it so the admin is not told everything is
		// fine, but do not pretend the write did not happen.
		return saved, fmt.Errorf("settings saved, but activating them failed: %w", err)
	}
	return saved, nil
}

// DeleteSettings removes the stored configuration and deactivates OIDC.
func (h *Handler) DeleteSettings(ctx context.Context) error {
	if h.HelmManaged() {
		return ErrHelmManaged
	}
	if h.settings == nil {
		return ErrNoSettings
	}
	if err := h.settings.Delete(ctx); err != nil {
		return err
	}
	return h.Reload(ctx)
}
