package mgmtapi

import (
	"sync"

	"golang.org/x/time/rate"
)

// saRateLimiter is a per-service-account request rate limiter, keyed on the
// credential's id. It is the R6 condition for letting a machine caller reach
// the management API (docs/gateway-tier-plan.md §7): a compromised or
// runaway credential cannot pin the API, and it is enforced in the auth gate
// so a limited request never reaches an interceptor or the handler.
//
// The limit is per credential, not global: one noisy service account cannot
// starve another, and a human session (which the gate never keys) is never
// limited here at all. A rate of 0 disables the limiter entirely (the map is
// never consulted), so an operator can turn it off without a code change.
//
// Limiters are created lazily on first sight of an id and kept for the process
// lifetime. The set is bounded by the number of distinct service accounts,
// which is small and operator-controlled (each is minted by an admin), so a
// plain map with no eviction is the right amount of machinery — a revoked
// credential simply stops authenticating upstream of here and its idle limiter
// costs a few words until restart.
type saRateLimiter struct {
	rate  rate.Limit
	burst int
	mu    sync.Mutex
	byID  map[string]*rate.Limiter
}

// newSARateLimiter builds a limiter allowing r sustained requests per second
// per service account with a bucket depth of burst. When r <= 0 it returns a
// disabled limiter whose allow() is always true.
func newSARateLimiter(r float64, burst int) *saRateLimiter {
	if r <= 0 {
		return &saRateLimiter{rate: 0}
	}
	if burst < 1 {
		burst = 1
	}
	return &saRateLimiter{
		rate:  rate.Limit(r),
		burst: burst,
		byID:  make(map[string]*rate.Limiter),
	}
}

// allow reports whether a request from the service account id may proceed now,
// consuming one token from its bucket. A disabled limiter always allows.
func (l *saRateLimiter) allow(id string) bool {
	if l.rate == 0 {
		return true
	}
	l.mu.Lock()
	lim, ok := l.byID[id]
	if !ok {
		lim = rate.NewLimiter(l.rate, l.burst)
		l.byID[id] = lim
	}
	l.mu.Unlock()
	return lim.Allow()
}
