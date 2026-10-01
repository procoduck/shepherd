package agentapi

import (
	"context"

	"shepherd/internal/auth"
)

// ContextWithOIDCPrincipal injects the principal the Bearer branch of the gate
// would have produced, so the enforcement path (resolveOrg) can be exercised
// with an OIDC identity without minting and verifying a real token — that is
// covered separately in internal/auth. Test-only.
func ContextWithOIDCPrincipal(ctx context.Context, claims auth.AgentClaims) context.Context {
	return context.WithValue(ctx, principalKey, Principal{Kind: AuthKindOIDC, Claims: &claims})
}

// SweepOnce runs one lifecycle sweep synchronously, as a tick would. Test-only.
func (sw *Sweeper) SweepOnce(ctx context.Context) { sw.sweep(ctx) }
