package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// AgentClaims is the validated result of a collector's OAuth2 access token.
// See docs/plans/2026-09-16-agent-oidc-auth.md. It carries only what the gate
// and the enforcement step need; org resolution (a Shepherd binding on AppID,
// or the admin cluster-claim) happens in internal/agentapi, not here.
type AgentClaims struct {
	// Issuer and Subject are the verified `iss` and `sub`.
	Issuer  string
	Subject string
	// AppID identifies the calling client — the value of the configured
	// app-identity claim (default `sub`, commonly `azp`/`client_id`). It is
	// the key an agent_identities binding maps to an org.
	AppID string
	// Roles is the native roles claim (`roles` on Entra, `permissions` on
	// Auth0, ...); Scopes is `scp`/`scope` split. Both feed the D0 grant gate
	// and the later role/cluster scope checks.
	Roles  []string
	Scopes []string
	// Clusters, when the token carries the configured clusters claim, is the
	// allowlist of clusters this token may act for (empty = any in the org).
	Clusters []string
}

var (
	// ErrAgentOIDCUnavailable means collector OIDC is not configured (no agent
	// audience). Callers translate it to "unauthenticated" — the feature is
	// simply off, indistinguishable to a caller from a bad token.
	ErrAgentOIDCUnavailable = errors.New("auth: collector OIDC is not configured")
	// ErrAgentGrantMissing means the token verified but does not carry the
	// required collector role or scope (the D0 grant gate).
	ErrAgentGrantMissing = errors.New("auth: token does not carry the required collector grant")
)

// AgentOIDCEnabled reports whether a collector may authenticate with an OIDC
// access token right now. It follows the same late-binding as user OIDC: a
// UI- or chart-configured provider can become live without a restart.
func (h *Handler) AgentOIDCEnabled() bool {
	rt := h.rt.Load()
	return rt != nil && rt.agentVerifier != nil
}

// VerifyAgentToken validates a collector's raw access token and returns its
// claims. It verifies signature (via the provider's JWKS), issuer, `aud`
// equal to the agent audience, and expiry through the go-oidc verifier, then
// enforces the D0 grant gate (a required role or scope) before returning.
// It never requires an org claim — which org the collector belongs to is
// decided in internal/agentapi.
func (h *Handler) VerifyAgentToken(ctx context.Context, raw string) (AgentClaims, error) {
	h.refreshIfStale(ctx)
	rt := h.rt.Load()
	if rt == nil || rt.agentVerifier == nil {
		return AgentClaims{}, ErrAgentOIDCUnavailable
	}

	idToken, err := rt.agentVerifier.Verify(ctx, raw)
	if err != nil {
		return AgentClaims{}, err
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return AgentClaims{}, err
	}

	ac := AgentClaims{
		Issuer:   idToken.Issuer,
		Subject:  idToken.Subject,
		Roles:    claimStrings(claims, rt.agentRolesClaim),
		Scopes:   scopeValues(claims),
		Clusters: claimStrings(claims, rt.agentClustersClaim),
	}
	// AppID defaults to the verified subject when the app claim is unset or is
	// literally `sub`, so a client-credentials token whose sub is the client
	// id works with no extra config.
	if rt.agentAppClaim == "" || rt.agentAppClaim == "sub" {
		ac.AppID = idToken.Subject
	} else {
		ac.AppID = claimString(claims, rt.agentAppClaim)
	}

	// D0 grant gate: holding a valid token for the audience is not enough; the
	// client must carry the explicit collector grant when one is required.
	if rt.agentRequiredRole != "" && !slices.Contains(ac.Roles, rt.agentRequiredRole) {
		return AgentClaims{}, ErrAgentGrantMissing
	}
	if rt.agentRequiredScope != "" && !slices.Contains(ac.Scopes, rt.agentRequiredScope) {
		return AgentClaims{}, ErrAgentGrantMissing
	}
	return ac, nil
}

// scopeValues reads OAuth2 scopes from a token. `scp` (Entra, others) may be a
// space-separated string or an array; `scope` is space-separated. Both are
// accepted, `scp` first, so a required-scope gate works whichever the IdP
// emits.
func scopeValues(claims map[string]any) []string {
	if v, ok := claims["scp"]; ok {
		if s, isStr := v.(string); isStr {
			return strings.Fields(s)
		}
		return claimStrings(claims, "scp")
	}
	if s, ok := claims["scope"].(string); ok {
		return strings.Fields(s)
	}
	return claimStrings(claims, "scope")
}
