package serve

// Format is the serve format of the config ComputeServed produces: a version
// of WHAT the served bytes contain, independent of the pipelines they are
// computed from. Every serve_cache row records the format it was computed in
// (migration 0033), and both recompute paths stamp their writes with this
// constant through UpsertServeCacheConditional.
//
// A row whose format is lower than this is never served as current: the
// agent path (internal/agentapi's GetConfig) recomputes it before serving,
// exactly as it does a dirty row, and the conditional upsert accepts such a
// row as stale. Bump this whenever a change alters the served bytes for an
// unchanged fleet (a header line, a rendering rule) — every cached row then
// recomputes on its collector's next poll, and the bump survives a rolling
// update, which a "mark every row dirty" migration does not (0032: old pods
// recomputed the marked rows in the old format during the rollout).
//
// History: 0 — any row computed before formats existed, or by a binary that
// does not set the column (a mark resets a row to 0, see 0033); 1 — #296, the
// served header prints each pipeline's real revision.
const Format int16 = 1

// Current reports whether a cached row's format is the one this binary
// computes. A row in an older format is stale however clean its dirty flag.
func Current(format int16) bool { return format >= Format }
