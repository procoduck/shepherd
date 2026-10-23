package agentapi

import (
	"context"
	"crypto/sha256"
	"time"

	"shepherd/internal/auth"
	"shepherd/internal/store/sqlc"
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

// SetAfterRecompute installs a hook that runs between GetConfig's lazy
// recompute and its conditional write, so a spec can land a dirty mark
// exactly mid-recompute. Test-only.
func SetAfterRecompute(s *Service, f func(context.Context)) { s.afterRecompute = f }

// The outage caches' internals, for the bounds specs (outage_cache_test.go). Test-only.

// SetTokenVerdictClock replaces the verdict cache's clock.
func SetTokenVerdictClock(c *TokenVerdicts, now func() time.Time) { c.now = now }

// RememberTokenVerdict records a positive verdict as the gate does after a DB hit.
func RememberTokenVerdict(c *TokenVerdicts, tokenID string, storedHash []byte) {
	c.rememberAs(tokenID, sqlc.AgentToken{TokenHash: storedHash})
}

// VerifyTokenVerdict checks secret against tokenID's cached verdict.
func VerifyTokenVerdict(c *TokenVerdicts, tokenID, secret string) bool {
	_, _, match := c.verify(tokenID, sha256.Sum256([]byte(secret)))
	return match
}

// LastServedCache wraps the unexported last-served map.
type LastServedCache struct{ l *lastServed }

// NewLastServedCache builds a last-served map with explicit bounds.
func NewLastServedCache(ttl time.Duration, maxItems, maxBytes int, now func() time.Time) LastServedCache {
	l := newLastServed(ttl, maxItems, maxBytes)
	if l != nil && now != nil {
		l.now = now
	}
	return LastServedCache{l: l}
}

func (c LastServedCache) Put(key, content, hash string) {
	c.l.put(key, servedConfig{content: content, hash: hash})
}

func (c LastServedCache) Get(key string) (content, hash string, ok bool) {
	cfg, ok := c.l.get(key)
	return cfg.content, cfg.hash, ok
}

func (c LastServedCache) Stats() (entries, bytes int) { return c.l.stats() }

// LastServedLen reports how many configs the Service remembers.
func LastServedLen(s *Service) int { n, _ := s.lastServed.stats(); return n }

// ContextWithTokenPrincipal injects an agent-token principal, as the gate
// would after verifying tokenID. Test-only.
func ContextWithTokenPrincipal(ctx context.Context, tokenID string, b Binding) context.Context {
	return context.WithValue(ctx, principalKey, Principal{Kind: AuthKindToken, TokenID: tokenID, Binding: b})
}

// SetDBWorkFloor replaces dbWorkFloor and returns a func restoring it.
// Services built after the call use it. Test-only.
func SetDBWorkFloor(d time.Duration) (restore func()) {
	prev := dbWorkFloor
	dbWorkFloor = d
	return func() { dbWorkFloor = prev }
}
