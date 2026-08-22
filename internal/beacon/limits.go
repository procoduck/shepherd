package beacon

import (
	"sync"

	"golang.org/x/time/rate"
)

// Limits bounds one beacon write: a body-size cap (enforced by
// DecodeWriteRequest, doubled by internal/agentapi's http.MaxBytesReader
// wrap — see doc.go) and a per-instance rate limit (enforced by
// RateLimiter). Both are required, non-zero values — see DefaultLimits for
// the values internal/agentapi's server wiring uses; a caller that
// constructs a zero Limits gets a limiter that allows nothing (Allow always
// false) and a decoder that rejects every body, which fails loudly rather
// than silently serving uncapped.
type Limits struct {
	// MaxBodyBytes bounds one write request body, pre-decompression.
	MaxBodyBytes int64
	// RatePerSecond is the sustained requests/sec allowed per rate-limit
	// key (see RateLimiter.Allow).
	RatePerSecond float64
	// Burst is the maximum burst above RatePerSecond a single key may take.
	Burst int
}

// DefaultLimits are the values internal/agentapi's server wiring uses in
// production. The baseline pipeline (render.go) scrapes every 60s, so one
// request/minute sustained is the expected steady state; Burst=5 and a
// RatePerSecond well above 1/60 tolerate a fleet of many instances sharing
// one agent token (the rate-limit key — see RateLimiter) restarting or
// catching up together without every instance but the first getting
// throttled, while still bounding a single misbehaving/malicious sender.
var DefaultLimits = Limits{
	MaxBodyBytes:  1 << 20, // 1 MiB — a running-components-only payload is a few KB; this is generous headroom, not a target.
	RatePerSecond: 2,
	Burst:         10,
}

// RateLimiter enforces Limits.RatePerSecond/Burst per key. The key
// internal/agentapi's handler uses is the authenticated agent token ID
// (verifyBasicAuth's return value) — "per instance" in the sense the plan's
// G5 gate means it: per authenticated caller, which is the identity the
// SAME Basic Auth mechanism already establishes for every other agent
// endpoint (D6: "auth is free"). A token shared by many collector replicas
// shares one bucket; see DefaultLimits' Burst for why that is tolerated
// rather than fought.
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rate     rate.Limit
	burst    int
}

// NewRateLimiter creates a RateLimiter from lim. Panics if RatePerSecond<=0
// or Burst<=0 — a zero-value limit is a configuration bug, not a policy of
// "unlimited", and must fail at construction rather than silently letting
// every request through (the same "opt-in control needs a loud failure mode"
// lesson recorded in docs/gateway-tier-plan.md §10 for W1's
// WithRoleEnforcement).
func NewRateLimiter(lim Limits) *RateLimiter {
	if lim.RatePerSecond <= 0 || lim.Burst <= 0 {
		panic("beacon: NewRateLimiter requires RatePerSecond>0 and Burst>0 — a zero limit is a config bug, not \"unlimited\"")
	}
	return &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rate.Limit(lim.RatePerSecond),
		burst:    lim.Burst,
	}
}

// Allow reports whether a request under key is within its rate limit,
// creating that key's bucket on first use. The bucket map is bounded by the
// number of distinct agent tokens that have ever written a beacon sample —
// an admin-issued, small, slowly-growing set (internal/store/queries/agent_tokens.sql
// has no bulk-creation path) — not an attacker-controlled key space, so no
// eviction policy is needed: this is not the same shape as a per-IP limiter
// facing the open internet.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	lim, ok := l.limiters[key]
	if !ok {
		lim = rate.NewLimiter(l.rate, l.burst)
		l.limiters[key] = lim
	}
	l.mu.Unlock()
	return lim.Allow()
}
