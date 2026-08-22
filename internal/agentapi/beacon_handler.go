package agentapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/beacon"
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

// BeaconHandler is the G5 endpoint: Prometheus remote_write ingest,
// authenticated by the same agent-token Basic Auth every other agentapi
// endpoint already uses (auth.go's verifyBasicAuth), capped in body size and
// per-caller rate (internal/beacon.Limits), and structurally incapable of
// persisting a raw sample value — see internal/beacon's package doc for what
// that guarantee rests on. This type, not a bare http.HandlerFunc closure,
// exists so its dependencies (store, limits, limiter) are visible at
// construction rather than captured invisibly.
type BeaconHandler struct {
	store   *store.Store
	logger  *slog.Logger
	limits  beacon.Limits
	limiter *beacon.RateLimiter
}

// NewBeaconHandler constructs a BeaconHandler. limits must be non-zero (see
// beacon.NewRateLimiter, which panics on a zero Limits rather than silently
// disabling the rate cap) — pass beacon.DefaultLimits unless a caller has a
// specific reason to diverge.
func NewBeaconHandler(st *store.Store, logger *slog.Logger, limits beacon.Limits) *BeaconHandler {
	return &BeaconHandler{
		store:   st,
		logger:  logger.With("component", "beacon"),
		limits:  limits,
		limiter: beacon.NewRateLimiter(limits),
	}
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

	tokenIDStr, err := verifyBasicAuth(r.Context(), r.Header.Get("Authorization"), h.store)
	if err != nil {
		// Same response shape agent Basic Auth failures already use
		// elsewhere (auth.go): WWW-Authenticate so a client can tell this is
		// a credentials problem, not a routing one.
		w.Header().Set("WWW-Authenticate", `Basic realm="shepherd-agent"`)
		http.Error(w, "beacon: unauthenticated", http.StatusUnauthorized)
		return
	}

	if !h.limiter.Allow(tokenIDStr) {
		http.Error(w, "beacon: rate limit exceeded for this agent token", http.StatusTooManyRequests)
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
		h.logger.Warn("beacon: decode failed", "err", err, "token_id", tokenIDStr)
		http.Error(w, "beacon: "+err.Error(), status)
		return
	}

	instanceLabel, observations, err := beacon.Project(wr)
	if err != nil {
		h.logger.Warn("beacon: project failed", "err", err, "token_id", tokenIDStr)
		http.Error(w, "beacon: "+err.Error(), http.StatusBadRequest)
		return
	}

	var tokenID pgtype.UUID
	if err := tokenID.Scan(tokenIDStr); err != nil {
		// verifyBasicAuth already parsed and looked up this exact string as a
		// UUID to authenticate the request, so a re-parse failure here would
		// mean this handler's own logic diverged from auth.go's — an
		// internal bug, not a client error.
		h.logger.Error("beacon: authenticated token id did not re-parse as a UUID", "token_id", tokenIDStr, "err", err)
		http.Error(w, "beacon: internal error", http.StatusInternalServerError)
		return
	}

	for _, obs := range observations {
		if _, err := h.store.Queries.UpsertBeaconComponent(r.Context(), sqlc.UpsertBeaconComponentParams{
			TokenID:       tokenID,
			InstanceLabel: instanceLabel,
			ComponentName: obs.ComponentName,
			Healthy:       obs.Healthy,
		}); err != nil {
			h.logger.Error("beacon: storing component observation failed", "err", err,
				"token_id", tokenIDStr, "instance", instanceLabel, "component", obs.ComponentName)
			http.Error(w, "beacon: storing observation failed", http.StatusInternalServerError)
			return
		}
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
