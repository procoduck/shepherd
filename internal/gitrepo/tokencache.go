package gitrepo

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// tokenRefreshSkew is how long before a cached token's reported expiry
// TokenCache treats it as stale and mints a replacement, so a token is
// never handed to a caller close enough to its real expiry to plausibly
// expire mid-request.
const tokenRefreshSkew = 2 * time.Minute

// TokenMinter mints one fresh short-lived token, returning its value and
// absolute expiry. It must not log the token (docs/git-provider-design.md
// — "never log tokens or secrets").
type TokenMinter func(ctx context.Context) (token string, expiry time.Time, err error)

// TokenCache caches short-lived tokens minted by a token-exchange auth
// strategy (ado_sp, github_app), keyed by credential id, refreshing
// shortly before expiry rather than on every call. Per
// docs/git-provider-design.md §3.2, both token-exchange strategies "share
// ONE cache keyed by credential id": construct a single TokenCache and
// pass it to every AdoSPAuth and GitHubAppAuth value, whatever their kind —
// keys are credential ids, which are unique across all credentials
// regardless of kind, so ado_sp and github_app entries never collide
// sharing one cache.
//
// TokenCache is safe for concurrent use. Concurrent Get calls for the same
// key never mint more than one token at a time: the per-key lock is held
// across the mint call, so late arrivals wait for the in-flight mint and
// reuse its result rather than each minting their own token.
type TokenCache struct {
	mu      sync.Mutex
	entries map[string]*tokenEntry
}

type tokenEntry struct {
	mu     sync.Mutex // serializes minting for this one key
	token  string
	expiry time.Time
}

// NewTokenCache returns an empty TokenCache.
func NewTokenCache() *TokenCache {
	return &TokenCache{entries: make(map[string]*tokenEntry)}
}

// Get returns a cached, still-valid token for key. If none is cached, or
// the cached one is within tokenRefreshSkew of its expiry, it calls mint,
// caches the result under key, and returns that instead.
func (c *TokenCache) Get(ctx context.Context, key string, mint TokenMinter) (string, error) {
	c.mu.Lock()
	e, ok := c.entries[key]
	if !ok {
		e = &tokenEntry{}
		c.entries[key] = e
	}
	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.token != "" && time.Now().Before(e.expiry.Add(-tokenRefreshSkew)) {
		return e.token, nil
	}

	token, expiry, err := mint(ctx)
	if err != nil {
		return "", fmt.Errorf("gitrepo: minting token for credential %s: %w", key, err)
	}
	e.token = token
	e.expiry = expiry
	return e.token, nil
}
