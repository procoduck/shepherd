package auth

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Local sign-in throttle tuning (D4, W3-2). Two independent buckets guard
// LocalLoginHandler: one keyed by the submitted login, one keyed by the
// source IP. Neither alone is enough — a per-login-only limiter lets an
// attacker spread guesses across many source addresses, a per-IP-only
// limiter lets one behind a shared address (the Helm ingress puts every
// client behind the same IP unless server.trusted_proxies names it — see
// clientIP) lock everyone else out with a single misbehaving client.
//
// The per-login bucket is the tight one: burst loginPerLoginBurst attempts
// land immediately (an operator mistyping a password a few times must not
// see a 429), the 11th within the window does not. The per-IP bucket is
// deliberately generous for the same reason the risk is accepted rather
// than fixed here — behind the ingress every client shares one IP, so a
// tight per-IP limit would throttle unrelated users together; it exists to
// catch a single client hammering many logins from one address, not to
// replace the per-login bucket.
const (
	loginPerLoginRate  = rate.Limit(10.0 / 60.0) // ~10/min steady state
	loginPerLoginBurst = 10
	loginPerIPRate     = rate.Limit(1.0) // ~60/min steady state
	loginPerIPBurst    = 60

	// loginThrottleRetryAfterSeconds is reported on every 429: the time to
	// refill one token in the tighter (per-login) bucket. It is a constant
	// rather than computed per request because computing an exact wait would
	// require consuming a reservation from a limiter that may not be the one
	// that actually tripped (IP vs login), and a slightly conservative fixed
	// value is a better contract for a client than a value that depends on
	// which bucket happened to deny it.
	loginThrottleRetryAfterSeconds = 6

	// loginThrottleIdleEvict bounds how long a key's limiter is kept once it
	// stops being used, so a map fed by an attacker cycling through usernames
	// or source IPs does not grow without bound.
	loginThrottleIdleEvict = 10 * time.Minute
)

// loginThrottle is in-process, per-key rate limiting keyed by an arbitrary
// string (a login name or a source IP, distinguished by prefix at the call
// site). State is per-replica: with more than one replica each limits only
// its own share of traffic, a generous-but-real floor rather than an exact
// one — see D4, which chose this over a DB-backed counter precisely to avoid
// a write on every login attempt.
//
// The zero value is ready to use: allow lazily creates its maps on first
// call, so a Handler needs no explicit constructor call to wire this in.
type loginThrottle struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	lastSeen map[string]time.Time
}

// allow reports whether an attempt keyed by key may proceed, creating the
// key's limiter (rate r, burst tokens) on first use.
//
// Eviction is opportunistic rather than a background goroutine: every call
// sweeps entries idle past evictAfter before consulting its own key. That
// keeps the throttle's lifecycle tied to login traffic, with nothing to
// start or stop alongside the Handler.
func (t *loginThrottle) allow(key string, r rate.Limit, burst int, evictAfter time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.limiters == nil {
		t.limiters = make(map[string]*rate.Limiter)
		t.lastSeen = make(map[string]time.Time)
	}

	now := time.Now()
	for k, seen := range t.lastSeen {
		if now.Sub(seen) > evictAfter {
			delete(t.lastSeen, k)
			delete(t.limiters, k)
		}
	}

	lim, ok := t.limiters[key]
	if !ok {
		lim = rate.NewLimiter(r, burst)
		t.limiters[key] = lim
	}
	t.lastSeen[key] = now
	return lim.Allow()
}
