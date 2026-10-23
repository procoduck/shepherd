package agentapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"shepherd/internal/store/sqlc"
)

// Riding out a database outage (#374).
//
// Every collector request needs Postgres: the gate looks the agent token up,
// GetConfig upserts the collector and reads serve_cache. Without the pieces in
// this file a DB blip answered every poll with Unauthenticated (gate) or
// Internal (handlers), and Alloy — which exits when its start-up
// RegisterCollector fails — could not start until the DB was back.
//
// Two bounded in-process caches let a replica keep answering collectors it
// served recently, and ONLY while the database is unreachable:
//
//   - TokenVerdicts: positive agent-token verdicts (valid, not revoked), keyed
//     by token id, holding the stored sha256 — never the secret. Normal
//     requests always go to the DB, so a revocation takes effect on the next
//     request; the cache is read only when the lookup fails with an
//     outage-class error.
//   - lastServed: the last config this replica served per (principal,
//     cluster, role), with its hash, so a poll during an outage gets the same
//     content and hash semantics it would have got from serve_cache.
//
// Both entries expire agent.outage_cache_ttl after the last successful
// DB-backed request that wrote them. A token revoked on another replica (or
// with the CLI) can therefore be accepted by this replica for at most that
// TTL, and only while the DB — where the revocation lives — is unreachable.

const (
	// DefaultTokenVerdictMax caps the remembered token verdicts. One entry is
	// a token id, a 32-byte hash and a timestamp; tokens are per cluster or
	// per fleet, so this is far above any real token count.
	DefaultTokenVerdictMax = 10000
	// lastServedMaxEntries and lastServedMaxBytes cap the last-served map.
	// Served configs run to tens or a few hundred KiB; past either cap the
	// least recently written entry goes first, and a collector whose entry
	// was evicted simply gets Unavailable during an outage (Alloy keeps
	// running what it has).
	lastServedMaxEntries = 10000
	lastServedMaxBytes   = 64 << 20
)

// dbWorkFloor bounds one request's server-side work. The work runs on a
// context detached from the caller's (serverCtx), so a deadline that expires
// is the server's own — a database too slow to answer, an outage — and never
// one the caller chose with Connect-Timeout-Ms or grpc-timeout. It is also
// how long a disconnected caller's request can keep holding a pool
// connection.
//
// It does not cap validation: the lazy recompute on this path runs only
// serve.ComputeServed's in-process Stage 1 — never `alloy validate` (Stage 2)
// or the Stage 3 dry-run, which run on the management API's write path under
// validate.timeout / validate.stage3_timeout. The spec "never runs alloy on
// the poll path" guards that: if a future change adds an alloy run here,
// this bound must grow with the validation budget. A var only so a spec can
// shrink it (export_test.go).
var dbWorkFloor = 15 * time.Second

// serverCtx detaches ctx from the caller's cancellation and deadline (keeping
// its values, e.g. the principal) and applies timeout. Without this, a
// caller could pick a timeout that expires mid-request, have the failure
// classified as an outage, and be answered from the outage caches with the
// rest of the request — the org check — skipped (review of #385).
func serverCtx(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// callerGone returns the Connect error for a caller whose own context is
// done (cancelled or past its deadline), or nil. A request whose caller is
// gone never takes the outage path.
func callerGone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return connect.NewError(connect.CodeOf(err), err)
	}
	return nil
}

// errDatabaseUnavailable is the client-facing message for an outage. The
// underlying driver error is logged, never sent: it can name hosts and ports.
var errDatabaseUnavailable = errors.New("shepherd's database is unavailable; retry later")

// isOutage reports whether err means the database could not be reached or
// could not serve the request — a connection, network or timeout failure, or
// a server-side "shutting down / not accepting connections" error — as opposed
// to a logic error (a constraint violation, a missing row, a bad query). Only
// outage-class errors map to Unavailable and enable the outage caches.
func isOutage(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"): // class 08: connection_exception
			return true
		case pgErr.Code == "57P01", // admin_shutdown
			pgErr.Code == "57P02", // crash_shutdown
			pgErr.Code == "57P03", // cannot_connect_now (starting up, recovery)
			pgErr.Code == "53300", // too_many_connections
			pgErr.Code == "25006": // read_only_sql_transaction: a write hit a standby mid-failover
			return true
		}
		return false
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}
	if pgconn.Timeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// pgconn marks an error SafeToRetry when the query never reached the
	// server — a connection that broke before the send.
	return pgconn.SafeToRetry(err)
}

// unavailable is the Connect error for an outage-class failure.
func unavailable() *connect.Error {
	return connect.NewError(connect.CodeUnavailable, errDatabaseUnavailable)
}

// TokenVerdicts remembers positive agent-token verdicts for use during a
// database outage only (see the file comment). The zero value and a nil
// pointer are both "disabled": nothing is remembered, nothing verifies.
type TokenVerdicts struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]tokenVerdict
	now     func() time.Time
}

// tokenVerdict is one DB-verified token: its stored hash and the binding
// read with it (org, allowlists, may-claim), so the outage path rebuilds the
// exact principal the database last produced — never a broader one.
type tokenVerdict struct {
	hash       [sha256.Size]byte
	token      sqlc.AgentToken // ID and binding fields only; TokenHash is hash
	verifiedAt time.Time
}

// NewTokenVerdicts returns a verdict cache whose entries live ttl past their
// last DB-backed verification, holding at most maxEntries. ttl <= 0 disables
// it (returns nil).
func NewTokenVerdicts(ttl time.Duration, maxEntries int) *TokenVerdicts {
	if ttl <= 0 || maxEntries <= 0 {
		return nil
	}
	return &TokenVerdicts{ttl: ttl, max: maxEntries, entries: make(map[string]tokenVerdict), now: time.Now}
}

// remember records that tok (a live row the database just returned) verified
// just now, keyed by its canonical id.
func (c *TokenVerdicts) remember(tok sqlc.AgentToken) {
	c.rememberAs(tok.ID.String(), tok)
}

func (c *TokenVerdicts) rememberAs(tokenID string, tok sqlc.AgentToken) {
	if c == nil || len(tok.TokenHash) != sha256.Size {
		return
	}
	var h [sha256.Size]byte
	copy(h[:], tok.TokenHash)
	kept := sqlc.AgentToken{
		ID: tok.ID, OrgID: tok.OrgID, MayClaim: tok.MayClaim, TokenHash: h[:],
		Clusters: slices.Clone(tok.Clusters), Roles: slices.Clone(tok.Roles),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, ok := c.entries[tokenID]; !ok && len(c.entries) >= c.max {
		c.evictLocked(now)
	}
	c.entries[tokenID] = tokenVerdict{hash: h, token: kept, verifiedAt: now}
}

// evictLocked makes room for one entry: every expired entry goes, and if
// none had expired, the least recently verified one.
func (c *TokenVerdicts) evictLocked(now time.Time) {
	var oldestID string
	var oldest time.Time
	for id, v := range c.entries {
		if now.Sub(v.verifiedAt) >= c.ttl {
			delete(c.entries, id)
			continue
		}
		if oldestID == "" || v.verifiedAt.Before(oldest) {
			oldestID, oldest = id, v.verifiedAt
		}
	}
	if len(c.entries) >= c.max && oldestID != "" {
		delete(c.entries, oldestID)
	}
}

// verify looks tokenID up: known reports an unexpired positive verdict, and
// match whether its stored hash equals secretHash (constant-time compare);
// tok is the row rebuilt from the verdict (id, hash and binding as last read
// from the database). Known but not matching is a wrong secret for a real
// token — a bad credential, outage or not.
func (c *TokenVerdicts) verify(tokenID string, secretHash [sha256.Size]byte) (tok sqlc.AgentToken, known, match bool) {
	if c == nil {
		return sqlc.AgentToken{}, false, false
	}
	c.mu.Lock()
	v, ok := c.entries[tokenID]
	if ok && c.now().Sub(v.verifiedAt) >= c.ttl {
		delete(c.entries, tokenID)
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return sqlc.AgentToken{}, false, false
	}
	if subtle.ConstantTimeCompare(secretHash[:], v.hash[:]) != 1 {
		return sqlc.AgentToken{}, true, false
	}
	out := v.token
	out.Clusters, out.Roles = slices.Clone(out.Clusters), slices.Clone(out.Roles)
	out.TokenHash = slices.Clone(out.TokenHash)
	return out, true, true
}

// Evict forgets tokenID's verdict. AdminService.RevokeAgentToken and
// BindAgentToken call it on this replica (internal/server wires it); the gate
// calls it when the DB says the token is unknown or revoked. tokenID is the canonical (lower-case) UUID.
func (c *TokenVerdicts) Evict(tokenID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, tokenID)
	c.mu.Unlock()
}

// Len is the number of remembered verdicts, expired ones included until they
// are next touched.
func (c *TokenVerdicts) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// servedConfig is what one GetConfig resolved to: the content and its hash.
type servedConfig struct {
	content string
	hash    string
}

// lastServed is the bounded map of the last config this replica served per
// (principal, cluster, role). The key is built only from the authenticated
// principal and the cluster/role the request names (servedKey) — the same
// inputs that select the serve_cache row, plus who asked — so one principal
// can never be handed a config served to another. Nil is disabled.
type lastServed struct {
	mu       sync.Mutex
	ttl      time.Duration
	maxItems int
	maxBytes int
	bytes    int
	entries  map[string]lastServedEntry
	now      func() time.Time
}

type lastServedEntry struct {
	cfg       servedConfig
	servedAt  time.Time
	sizeBytes int
}

func newLastServed(ttl time.Duration, maxItems, maxBytes int) *lastServed {
	if ttl <= 0 || maxItems <= 0 || maxBytes <= 0 {
		return nil
	}
	return &lastServed{ttl: ttl, maxItems: maxItems, maxBytes: maxBytes, entries: make(map[string]lastServedEntry), now: time.Now}
}

// servedKey keys lastServed: the principal's identity, then the collector the
// request names. For an OIDC caller the identity is every claim the org
// resolution reads (resolveOrg: issuer + app id for the binding lookup, the
// asserted org and its clusters allowlist for mode 2) plus subject, roles and
// scopes — so a token whose claims changed never matches an entry recorded
// under the old ones. The parts are JSON-encoded, so no two distinct
// identities or collectors share a key.
//
// An OIDC caller's org also depends on a server-side agent_identities
// binding the claims do not carry, so its key includes ident — that binding
// (or its absence) as this replica last read it from the database. With
// ident nil (never read, expired, or forgotten after an AdminService change)
// there is no key: nothing is remembered or served from memory for it.
func servedKey(p Principal, cluster, role string, ident *identityEntry) string {
	var who any
	switch p.Kind {
	case AuthKindOIDC:
		if p.Claims == nil || ident == nil {
			return ""
		}
		c := p.Claims
		identOrg := ""
		if ident.bound && ident.binding.OrgID.Valid {
			identOrg = ident.binding.OrgID.String()
		}
		who = struct {
			Kind, Issuer, Subject, AppID, Org string
			Clusters, Roles, Scopes           []string
			Bound                             bool
			BindingOrg                        string
			BindingClusters, BindingRoles     []string
		}{
			"oidc", c.Issuer, c.Subject, c.AppID, c.Org, sortedCopy(c.Clusters), sortedCopy(c.Roles), sortedCopy(c.Scopes),
			ident.bound, identOrg, sortedCopy(allowlist(ident.binding.Clusters)), sortedCopy(allowlist(ident.binding.Roles)),
		}
	case AuthKindToken:
		if p.TokenID == "" {
			return ""
		}
		// The token's binding (#386) is part of who it is: an entry recorded
		// under one org/allowlist/may-claim is never served under another.
		b := p.Binding
		org := ""
		if b.OrgID.Valid {
			org = b.OrgID.String()
		}
		who = struct {
			Kind, TokenID, Org string
			Clusters, Roles    []string
			MayClaim           bool
		}{"token", strings.ToLower(p.TokenID), org, sortedCopy(allowlist(b.Clusters)), sortedCopy(allowlist(b.Roles)), b.MayClaim}
	default:
		return ""
	}
	b, err := json.Marshal([]any{who, cluster, role})
	if err != nil {
		return ""
	}
	return string(b)
}

// identityEntry is an OIDC caller's agent_identities binding as last read
// from the database: bound=false records "no binding" (a real answer, not a
// miss).
type identityEntry struct {
	bound   bool
	binding Binding
	readAt  time.Time
}

// identityCache remembers, per (issuer, app id), the agent_identities binding
// the last database-backed request read, for the outage cache TTL, so the
// OIDC last-served key (servedKey) can name the binding during an outage.
// AdminService's create/delete of an identity forgets it on this replica
// (Service.ForgetAgentIdentity); another replica keeps the old binding for at
// most the TTL, and only uses it while the database is unreachable. Nil is
// disabled.
type identityCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]identityEntry
	now     func() time.Time
}

func newIdentityCache(ttl time.Duration, maxEntries int) *identityCache {
	if ttl <= 0 || maxEntries <= 0 {
		return nil
	}
	return &identityCache{ttl: ttl, max: maxEntries, entries: make(map[string]identityEntry), now: time.Now}
}

func identityKey(issuer, appID string) string { return issuer + "\x00" + appID }

// observe records what the database returned for (issuer, appID): b, or no
// binding when b is nil.
func (c *identityCache) observe(issuer, appID string, b *Binding) {
	if c == nil {
		return
	}
	e := identityEntry{}
	if b != nil {
		e.bound = true
		e.binding = Binding{OrgID: b.OrgID, Clusters: slices.Clone(b.Clusters), Roles: slices.Clone(b.Roles), MayClaim: b.MayClaim}
	}
	k := identityKey(issuer, appID)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	e.readAt = now
	if _, ok := c.entries[k]; !ok && len(c.entries) >= c.max {
		var oldestK string
		var oldest time.Time
		for key, v := range c.entries {
			if now.Sub(v.readAt) >= c.ttl {
				delete(c.entries, key)
				continue
			}
			if oldestK == "" || v.readAt.Before(oldest) {
				oldestK, oldest = key, v.readAt
			}
		}
		if len(c.entries) >= c.max && oldestK != "" {
			delete(c.entries, oldestK)
		}
	}
	c.entries[k] = e
}

// lookup returns the unexpired entry for (issuer, appID), or nil.
func (c *identityCache) lookup(issuer, appID string) *identityEntry {
	if c == nil {
		return nil
	}
	k := identityKey(issuer, appID)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok {
		return nil
	}
	if c.now().Sub(e.readAt) >= c.ttl {
		delete(c.entries, k)
		return nil
	}
	return &e
}

func (c *identityCache) forget(issuer, appID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, identityKey(issuer, appID))
	c.mu.Unlock()
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func (l *lastServed) put(key string, cfg servedConfig) {
	if l == nil || key == "" {
		return
	}
	size := len(key) + len(cfg.content) + len(cfg.hash)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.deleteLocked(key)
	if size > l.maxBytes {
		return
	}
	now := l.now()
	for len(l.entries) >= l.maxItems || l.bytes+size > l.maxBytes {
		if !l.evictOldestLocked() {
			break
		}
	}
	l.entries[key] = lastServedEntry{cfg: cfg, servedAt: now, sizeBytes: size}
	l.bytes += size
}

func (l *lastServed) get(key string) (servedConfig, bool) {
	if l == nil || key == "" {
		return servedConfig{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return servedConfig{}, false
	}
	if l.now().Sub(e.servedAt) >= l.ttl {
		l.deleteLocked(key)
		return servedConfig{}, false
	}
	return e.cfg, true
}

// forget drops key — a request for it was refused or failed for a reason
// other than an outage, so what was served before must not be re-served.
func (l *lastServed) forget(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.deleteLocked(key)
	l.mu.Unlock()
}

func (l *lastServed) deleteLocked(key string) {
	if e, ok := l.entries[key]; ok {
		l.bytes -= e.sizeBytes
		delete(l.entries, key)
	}
}

// evictOldestLocked removes the least recently served entry; false when empty.
func (l *lastServed) evictOldestLocked() bool {
	var oldestKey string
	var oldest time.Time
	for k, e := range l.entries {
		if oldestKey == "" || e.servedAt.Before(oldest) {
			oldestKey, oldest = k, e.servedAt
		}
	}
	if oldestKey == "" {
		return false
	}
	l.deleteLocked(oldestKey)
	return true
}

func (l *lastServed) stats() (entries, bytes int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries), l.bytes
}
