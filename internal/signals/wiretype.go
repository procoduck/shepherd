package signals

// wireTypeTargets is the one wire type in the schema artifact that is
// PLUMBING, not a signal: it is a handle to a set of scrape/tail targets
// (Prometheus service discovery output, a file-tailer's file list, ...), not
// telemetry data itself. A discovery.* component that only speaks targets —
// discovery.kubernetes, discovery.relabel, local.file_match — contributes no
// signal on its own; it only shapes what a downstream metrics/logs/profiles
// component collects. Callers that see a pipeline made entirely of
// targets-only components (a "discovery graph with no scraper attached") get
// back an empty Signals.Combined, which is correct: nothing is actually being
// shipped yet.
const wireTypeTargets = "targets"

// wireTypeSignals classifies every wire type the schema artifact's component
// inputs/outputs ports declare, onto the four-member Signal taxonomy. This is
// the single source of truth for "what kind of telemetry does this wire
// carry" — Derive and Enforce both go through classifyWireType rather than
// switching on wire-type strings themselves.
//
// Coverage is meant to be exhaustive against the pinned artifact:
// TestWireTypesExhaustive in derive_test.go loads the real, shipped
// internal/schema/artifacts/alloy-v1.18.1.json, enumerates every distinct
// "type" string across every component's inputs[] and outputs[] arrays, and
// fails if any of them is not a key in this map — including "targets", which
// is a key with a deliberately empty value, not an absent one. That
// distinction is what makes the test meaningful: a future Alloy bump that
// introduces a brand new wire type (say, a "otel.profiles" once OTel
// standardizes continuous profiling) is a MISSING key, which classifyWireType
// reports as unrecognized (ok=false) rather than silently treating it as
// plumbing. See classifyWireType's doc for how Derive surfaces that case.
//
// Rationale per row:
//
//   - "prom.metrics", "otel.metrics" -> Metrics. Same signal, two protocols:
//     Prometheus's pull-based metric model and OTel's metrics data model both
//     describe numeric time series. A pipeline that only ever touches one or
//     the other is still, at the taxonomy's altitude, a metrics pipeline.
//   - "loki.logs", "otel.logs" -> Logs. Same reasoning: Loki's log stream
//     model and OTel's logs data model are both "logs" to a caller asking
//     "does this collector role see log lines".
//   - "otel.traces" -> Traces. No second protocol in this artifact carries
//     traces, so this is a direct mapping.
//   - "pyroscope.profiles" -> Profiles. Continuous profiling; Pyroscope is
//     the only protocol this artifact has for it.
//   - "otel.any" -> {Metrics, Logs, Traces}, deliberately excluding Profiles.
//     otel.any is the generic "OTel pipeline data" port that OTel collector
//     processors, exporters and connectors (otelcol.processor.*,
//     otelcol.exporter.*, otelcol.connector.*) expose alongside their
//     signal-specific otel.metrics/otel.logs/otel.traces ports, and the task
//     that produced this package flagged it as genuinely ambiguous: an
//     otel.any wire's actual runtime content depends on what's connected to
//     it, which this package cannot see (data flows through references this
//     package does not evaluate — see Derive's doc comment). A fail-safe
//     choice has to lean toward over-counting, not under-counting, so the
//     question is which over-count is honest. "All four taxonomy members"
//     was the naive fail-safe on offer, but the shipped artifact itself
//     answers the ambiguity more precisely than that: querying it (see this
//     package's derive_test.go/TestOtelAnyNeverCarriesProfiles, and the
//     one-off audit in the W1 session notes) shows pyroscope.profiles is
//     NEVER produced or accepted by any component that also has an otel.any
//     port — profiling flows exclusively through the pyroscope.* component
//     family, on its own dedicated wire type, with no otelcol bridge in this
//     artifact. Every component that speaks otel.any also speaks a subset of
//     {otel.metrics, otel.logs, otel.traces} directly, which is the OTel
//     collector's own three-signal data model. Including Profiles in this
//     row would not be "fail-safe" over the evidence available, it would be
//     wrong over it: it claims a routing risk (a profiling pipeline landing
//     on a metrics-only collector because it was misread as otel.any) that
//     the schema shows cannot occur. {Metrics, Logs, Traces} is fail-safe
//     within the domain otel.any is actually ambiguous over, and precise
//     outside it. If a future Alloy version adds an OTel-model profiles
//     bridge, TestOtelAnyNeverCarriesProfiles is what catches the artifact
//     changing under this assumption.
//   - "targets" -> {} (present, empty). See wireTypeTargets's doc.
var wireTypeSignals = map[string]Set{
	"prom.metrics":       NewSet(Metrics),
	"otel.metrics":       NewSet(Metrics),
	"loki.logs":          NewSet(Logs),
	"otel.logs":          NewSet(Logs),
	"otel.traces":        NewSet(Traces),
	"pyroscope.profiles": NewSet(Profiles),
	"otel.any":           NewSet(Metrics, Logs, Traces),
	wireTypeTargets:      {},
}

// classifyWireType returns the signals wireType carries and whether wireType
// is recognized at all. ok is false for any wire type this package has never
// been taught about — the caller (Derive) must treat that as "signal
// unknown", never as "no signal": see wireTypeSignals's doc for why the
// distinction is load-bearing.
func classifyWireType(wireType string) (sig Set, ok bool) {
	sig, ok = wireTypeSignals[wireType]
	return sig, ok
}
