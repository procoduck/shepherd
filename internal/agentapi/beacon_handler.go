package agentapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/auth"
	"shepherd/internal/beacon"
	"shepherd/internal/metrics"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// BeaconWritePath re-exports beacon.WritePath — the path every rendered
// baseline pipeline's prometheus.remote_write points at, and the path
// internal/server mounts this package's BeaconHandler on. Kept as an alias
// here (rather than requiring every caller to import internal/beacon
// directly) so the endpoint's identity reads naturally from either package;
// see beacon.WritePath's doc comment for why the underlying constant lives
// in internal/beacon. Exactly one path is ever served or rendered — the
// same "every emitted endpoint must resolve to a rendered route" discipline
// G7 holds W7's onboarding artifacts to (docs/gateway-tier-plan.md §6).
const BeaconWritePath = beacon.WritePath

// maxBeaconInstanceLabel is the instance_label length bound of
// beacon_inventory (0010) and beacon_instance_reports (0047).
const maxBeaconInstanceLabel = 256

// BeaconHandler is the G5 endpoint: Prometheus remote_write ingest,
// authenticated by the same agent-token Basic Auth every other agentapi
// endpoint already uses (auth.go's verifyBasicAuth), capped in body size and
// per-caller rate (internal/beacon.Limits), and structurally incapable of
// persisting a raw sample value — see internal/beacon's package doc for what
// that guarantee rests on. This type, not a bare http.HandlerFunc closure,
// exists so its dependencies (store, limits, limiter) are visible at
// construction rather than captured invisibly.
type BeaconHandler struct {
	store       *store.Store
	logger      *slog.Logger
	limits      beacon.Limits
	limiter     *beacon.RateLimiter
	verifyAgent AgentVerifier
	// refusals counts (and logs at Debug, rate-limited) writes dropped
	// because a bound token named another org's collector.
	refusals *refusalLog
}

// NewBeaconHandler constructs a BeaconHandler. limits must be non-zero (see
// beacon.NewRateLimiter, which panics on a zero Limits rather than silently
// disabling the rate cap) — pass beacon.DefaultLimits unless a caller has a
// specific reason to diverge. verifyAgent enables the OIDC Bearer branch (the
// same verifier the Connect gate uses); nil leaves the endpoint accepting only
// agent-token Basic auth.
func NewBeaconHandler(st *store.Store, logger *slog.Logger, limits beacon.Limits, verifyAgent AgentVerifier) *BeaconHandler {
	return &BeaconHandler{
		store:       st,
		logger:      logger.With("component", "beacon"),
		limits:      limits,
		limiter:     beacon.NewRateLimiter(limits),
		verifyAgent: verifyAgent,
		refusals:    newRefusalLogAt(refusalLogWindow, refusalLogMax, slog.LevelDebug),
	}
}

// authenticate resolves the reporting principal from the Authorization header,
// mirroring the Connect gate: a `Bearer` OIDC access token yields
// "oidc:<issuer>|<sub>", a `Basic` agent token yields the token UUID. The
// returned string is the beacon inventory identity and the rate-limit key;
// the scope is the org the principal acts for, which limits the collectors
// the write may name — see beaconScope.
func (h *BeaconHandler) authenticate(ctx context.Context, authz string) (string, beaconScope, error) {
	if raw, ok := strings.CutPrefix(authz, "Bearer "); ok {
		if h.verifyAgent == nil {
			return "", beaconScope{}, errUnauthenticated
		}
		claims, err := h.verifyAgent(ctx, raw)
		if err != nil {
			return "", beaconScope{}, errUnauthenticated
		}
		// Strip control characters from the verified claims before they become
		// a stored key, a rate-limit key, and a log field. The issuer is an
		// https URL and the subject comes from a signature-verified JWT, so
		// this is belt-and-braces — but it stops a hostile IdP from smuggling a
		// newline into the beacon key or the logs (CodeQL log-injection).
		principal := "oidc:" + stripControl(claims.Issuer) + "|" + stripControl(claims.Subject)
		scope, err := h.oidcScope(ctx, claims)
		if err != nil {
			return "", beaconScope{}, err
		}
		return principal, scope, nil
	}
	// The beacon keys inventory and its rate limit on the token id. A bound
	// token (migration 0038) may only report collectors its org claimed,
	// within its cluster allowlist — ServeHTTP drops the rest; an unbound
	// token acts for no org and may report nothing.
	tok, err := verifyBasicAuth(ctx, authz, h.store, nil)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnavailable {
			return "", beaconScope{}, errAuthUnavailable
		}
		return "", beaconScope{}, errUnauthenticated
	}
	// The id is a validated UUID, but sanitising here too keeps `principal`
	// provably free of control characters on every path into the logs.
	principal := stripControl(tok.ID.String())
	if !tok.OrgID.Valid {
		return principal, beaconScope{scoped: true, unbound: true}, nil
	}
	return principal, beaconScope{scoped: true, org: tok.OrgID, clusters: allowlist(tok.Clusters)}, nil
}

// beaconScope is what a beacon writer acts for, resolved as the agent API
// resolves it (Service.callerScope). A scoped writer may only report
// collectors whose cluster org claimed and, when clusters is non-nil, whose
// cluster is on that allowlist (nil = any, allowlistPermits' semantics). An
// unscoped writer — an OIDC caller with neither a binding nor an asserted
// org, whose org the agent API takes from the cluster's claim — is not
// checked. A scoped writer with an invalid org covers no collector at all:
// an unbound token (unbound set, counted as token_unbound), or a mode-2
// caller asserting an org that does not exist.
type beaconScope struct {
	scoped   bool
	unbound  bool
	org      pgtype.UUID
	clusters []string
}

// permits reports whether the scope may report a collector owned by owner in
// cluster.
func (sc beaconScope) permits(owner pgtype.UUID, cluster string) bool {
	if !sc.scoped {
		return true
	}
	if !owner.Valid || owner != sc.org {
		return false
	}
	return sc.clusters == nil || slices.Contains(sc.clusters, cluster)
}

// oidcScope resolves an OIDC caller's org with the agent API's precedence
// (Service.resolveOrg): its agent_identities binding first, then a mode-2
// asserted org (the IdP's org claim, by name), else none. A lookup the
// database could not answer is errAuthUnavailable — answered 503, which
// remote_write retries — rather than accepting a write the org check might
// have dropped.
func (h *BeaconHandler) oidcScope(ctx context.Context, claims auth.AgentClaims) (beaconScope, error) {
	binding, err := h.store.Queries.GetAgentIdentityByAppID(ctx, sqlc.GetAgentIdentityByAppIDParams{
		Issuer: claims.Issuer,
		AppID:  claims.AppID,
	})
	switch {
	case err == nil:
		return beaconScope{scoped: true, org: binding.OrgID, clusters: allowlist(binding.Clusters)}, nil
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return beaconScope{}, errAuthUnavailable
	}
	if claims.Org == "" {
		return beaconScope{}, nil
	}
	org, err := h.store.Queries.GetOrgByName(ctx, claims.Org)
	switch {
	case err == nil:
		// Mode 2 has no Shepherd-side allowlist; the token's own clusters
		// claim is the only one (resolveClaimOrg). Empty means any.
		var clusters []string
		if len(claims.Clusters) > 0 {
			clusters = claims.Clusters
		}
		return beaconScope{scoped: true, org: org.ID, clusters: clusters}, nil
	case errors.Is(err, pgx.ErrNoRows):
		// The agent API refuses this caller (claim_denied); here it may
		// report nothing.
		return beaconScope{scoped: true}, nil
	default:
		return beaconScope{}, errAuthUnavailable
	}
}

var (
	errUnauthenticated = errors.New("beacon: unauthenticated")
	// errAuthUnavailable: the token could not be looked up because the
	// database is unreachable (#374). Answered 503, which remote_write
	// retries, rather than 401, which it treats as permanent.
	errAuthUnavailable = errors.New("beacon: database unavailable")
)

// stripControl removes control runes (including CR and LF) so a value derived
// from a token cannot inject into a log line or a stored key.
func stripControl(s string) string {
	// Explicit CR/LF replacement first (the recognised log-injection barrier),
	// then drop any remaining control runes.
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// ServeHTTP implements the G5 gate in one call path:
//  1. reject an unauthenticated write (401) — half of G5, using the SAME
//     Basic Auth verification every other agent endpoint uses;
//  2. reject a write over the per-caller rate limit (429) — the OTHER
//     caller-identity-scoped cap, keyed on the same authenticated token id;
//  3. reject a body over the size cap (413), enforced twice: once here via
//     http.MaxBytesReader (so an oversized body never fully buffers into
//     memory before being rejected) and again inside
//     beacon.DecodeWriteRequest (so the library-level cap holds even if a
//     future caller forgets this wrap — see internal/beacon/doc.go);
//  4. decode, project (parse, project, discard — beacon.Project never
//     returns anything that can carry a raw sample value), and store.
func (h *BeaconHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "beacon: method not allowed", http.StatusMethodNotAllowed)
		return
	}

	principal, scope, err := h.authenticate(r.Context(), r.Header.Get("Authorization"))
	if errors.Is(err, errAuthUnavailable) {
		http.Error(w, "beacon: database unavailable, retry later", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		// Same response shape agent auth failures already use elsewhere
		// (auth.go): WWW-Authenticate so a client can tell this is a
		// credentials problem, not a routing one. Both schemes are advertised.
		w.Header().Set("WWW-Authenticate", `Bearer realm="shepherd-agent", Basic realm="shepherd-agent"`)
		http.Error(w, "beacon: unauthenticated", http.StatusUnauthorized)
		return
	}

	if !h.limiter.Allow(principal) {
		http.Error(w, "beacon: rate limit exceeded for this collector", http.StatusTooManyRequests)
		return
	}

	// http.MaxBytesReader is the HTTP-layer half of the size cap: Read
	// itself starts returning an error once the limit is crossed, so an
	// oversized body is never fully read into memory. beacon.DecodeWriteRequest
	// re-enforces the same bound independently below.
	// Defense in depth, and honestly labelled as such: removing this line
	// alone does not turn any test red, because DecodeWriteRequest reads
	// through its own io.LimitReader and still answers 413. Memory stays
	// bounded without it. It stays because bounding at the HTTP layer is
	// the right shape and costs nothing — but do not mistake it for the
	// enforced cap; that is the one inside DecodeWriteRequest, which has a
	// named red-run-proven test.
	r.Body = http.MaxBytesReader(w, r.Body, h.limits.MaxBodyBytes)

	wr, err := beacon.DecodeWriteRequest(r.Header, r.Body, h.limits.MaxBodyBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, beacon.ErrBodyTooLarge) || isMaxBytesError(err) {
			status = http.StatusRequestEntityTooLarge
		}
		h.logger.Warn("beacon: decode failed", "err", err, "principal", principal)
		http.Error(w, "beacon: "+err.Error(), status)
		return
	}

	instanceLabel, collectorID, observations, err := beacon.Project(wr)
	if errors.Is(err, beacon.ErrMixedCollectorIDs) {
		// One batch is one Alloy's: a batch naming several collectors cannot
		// be attributed to any of them (#389). Dropped and counted, still 204.
		metrics.BeaconDroppedTotal.WithLabelValues(metrics.BeaconDroppedCollectorIDMixed).Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		h.logger.Warn("beacon: project failed", "err", err, "principal", principal)
		http.Error(w, "beacon: "+err.Error(), http.StatusBadRequest)
		return
	}

	// An unbound token acts for no org, exactly as the agent API refuses it
	// (token_unbound): every write is dropped and counted, still 204.
	if scope.unbound {
		h.refusals.record(h.logger, "BeaconWrite", metrics.AgentRefusedTokenUnbound, instanceLabel, "principal", principal)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// #110: the baseline stamps the collector id (shepherd_collector_id) onto
	// every beacon series so the reconciliation surface can attribute this write
	// to a collector. A write with no id, or one that is not a UUID, cannot be
	// attributed or checked against the writer's org, so it is dropped and
	// counted by reason (#389) — never stored under the writer's principal.
	// Still 204: remote_write would retry anything else, and a retry cannot
	// supply the label.
	if collectorID == "" {
		metrics.BeaconDroppedTotal.WithLabelValues(metrics.BeaconDroppedCollectorIDMissing).Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var collectorUUID pgtype.UUID
	if collectorUUID.Scan(collectorID) != nil || !collectorUUID.Valid {
		metrics.BeaconDroppedTotal.WithLabelValues(metrics.BeaconDroppedCollectorIDInvalid).Inc()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// The collector id is a label the writer chose. A scoped principal (a
	// bound token, an OIDC identity with a binding, or a mode-2 OIDC caller
	// with an asserted org) may only report a collector whose cluster its org
	// claimed; a write naming any other
	// collector (another org's, an unclaimed one, or none known) is dropped
	// and counted, never stored — so one org cannot write health into another
	// org's reconciliation view. The answer is the same 204, so the call
	// reveals nothing about other orgs' collectors.
	if scope.scoped {
		owner, err := h.store.Queries.GetCollectorOwner(r.Context(), collectorUUID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// An unknown collector is in no org: denied below.
		case err != nil:
			// An outage, not a denial: 503, which remote_write retries.
			h.logger.Warn("beacon: looking up collector owner failed", "err", err, "principal", principal)
			http.Error(w, "beacon: database unavailable, retry later", http.StatusServiceUnavailable)
			return
		}
		if !scope.permits(owner.OrgID, owner.ClusterName) {
			h.refusals.record(h.logger, "BeaconWrite", metrics.AgentRefusedBeaconDenied, instanceLabel,
				"collector_id", collectorUUID.String(), "principal", principal)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	for _, obs := range observations {
		if _, err := h.store.Queries.UpsertBeaconComponent(r.Context(), sqlc.UpsertBeaconComponentParams{
			Principal:     principal,
			InstanceLabel: instanceLabel,
			ComponentName: obs.ComponentName,
			Healthy:       obs.Healthy,
			CollectorID:   collectorUUID,
		}); err != nil {
			h.logger.Error("beacon: storing component observation failed", "err", err,
				"principal", principal, "instance", instanceLabel, "component", obs.ComponentName)
			http.Error(w, "beacon: storing observation failed", http.StatusInternalServerError)
			return
		}
	}

	// Stamp "this instance reported now" on its rows (0046), after the
	// upserts so a row this write inserted is stamped too — also when the
	// write carried no pipeline series at all. Once a collector's only
	// pipeline is disabled its writes refresh no row, so without this stamp
	// the dropped row stays the instance's newest and reconciliation keeps
	// reading it as running (reconcile.ObserveBeacon). One throttled UPDATE
	// per write, whatever the number of pipelines. Every refused write
	// returned above, so it stamps nothing.
	if err := h.store.Queries.RecordBeaconInstanceReport(r.Context(), sqlc.RecordBeaconInstanceReportParams{
		CollectorID: collectorUUID, InstanceLabel: instanceLabel,
	}); err != nil {
		h.logger.Error("beacon: recording instance report failed", "err", err,
			"principal", principal, "instance", instanceLabel)
		http.Error(w, "beacon: storing observation failed", http.StatusInternalServerError)
		return
	}
	// And in the instance's last-report record (0047), which the sweeper
	// keeps with the instance lifecycle rather than with the rows: once the
	// rows of a collector whose only pipeline was dropped are swept, this is
	// what still says the instance reports and runs none. The 0046 stamp
	// above stays for older replicas mid-upgrade. A label past the table's
	// length bound (the same as beacon_inventory's) is no instance a row
	// could be stored for either: no record, rather than a 500 that
	// remote_write would retry forever.
	if utf8.RuneCountInString(instanceLabel) > maxBeaconInstanceLabel {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := h.store.Queries.RecordBeaconInstanceLastReport(r.Context(), sqlc.RecordBeaconInstanceLastReportParams{
		CollectorID: collectorUUID, InstanceLabel: instanceLabel,
	}); err != nil {
		h.logger.Error("beacon: recording instance last report failed", "err", err,
			"principal", principal, "instance", instanceLabel)
		http.Error(w, "beacon: storing observation failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// isMaxBytesError reports whether err came from http.MaxBytesReader's own
// enforcement (its error is unexported and version-dependent in message
// text across Go releases, so this checks by interface rather than string
// matching — see http.MaxBytesError, added in Go 1.19+, which this repo's
// pinned Go version (go.mod) supports).
func isMaxBytesError(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
