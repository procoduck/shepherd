// Package beacon implements the D6 beacon: parse, project, discard.
//
// docs/gateway-tier-plan.md D6 serves every collector a small, non-opt-in
// baseline pipeline (prometheus.exporter.self -> prometheus.scrape ->
// prometheus.relabel -> prometheus.remote_write, see RenderBaselinePipeline)
// that reports back to Shepherd over the same Prometheus remote_write wire
// format Alloy already speaks, authenticated by the same agent-token Basic
// Auth collectors already use for remotecfg (internal/agentapi/auth.go).
//
// # The structural no-raw-sample guarantee
//
// Plan §5 is explicit: "W5 must not persist raw samples, ever." This package
// makes that a property of the TYPES, not a promise resting on a comment a
// future change could quietly violate:
//
//   - Decode (decode.go) turns a request body into a *prompb.WriteRequest —
//     the standard wire type, still capable of carrying anything.
//   - Project (project.go) is the ONLY function in this package, or in
//     internal/agentapi's caller, that reads a prompb.WriteRequest. It
//     returns []ComponentObservation, and ComponentObservation has exactly
//     two fields: a name and a bool. There is no float64, no Value, no
//     Sample — nothing a raw metric reading could occupy. See
//     TestComponentObservationHasNoNumericField, which fails the moment a
//     numeric field is added to that struct, so this is a checked property,
//     not an inspected one.
//   - Every downstream consumer — internal/agentapi's HTTP handler, the sqlc
//     UpsertBeaconComponentParams, the beacon_inventory table itself
//     (0010_beacon_inventory.up.sql) — accepts exactly ComponentObservation's
//     shape. A raw sample value has no field to travel in from Decode's
//     output to the database row; "store no raw samples" is true because the
//     pipe is the wrong shape for one to fit through, not because nothing
//     currently tries.
//
// # Caps
//
// Two independent caps, both enforced and both red-run proven (see this
// package's and internal/agentapi's tests): a body-size cap (decode.go,
// enforced twice — once by internal/agentapi wrapping the request body in
// http.MaxBytesReader, and independently again inside DecodeWriteRequest, so
// a caller that forgets the HTTP-layer wrap still cannot get an oversized
// body decoded) and a per-instance rate limit (limits.go). "A cap that is
// configured but not enforced is the failure this repo keeps removing" —
// docs/gateway-tier-plan.md's W5 brief.
package beacon
