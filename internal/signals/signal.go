// Package signals derives, from raw Alloy pipeline text and the schema
// artifact, which observability signals (metrics, logs, traces, profiles) a
// pipeline actually carries, and enforces which of those a collector role is
// allowed to serve.
//
// This package is pure and deterministic: it does no I/O beyond reading the
// schema.Registry it is handed, and it never mutates anything. It is the
// foundation W1 of docs/gateway-tier-plan.md asks for; wiring its Enforce
// function into internal/merge's refusal path (gate G6) is a later step, not
// this package's job — see the plan's §8 sub-agent territory rules.
package signals

import "strings"

// Signal is one member of the pipeline signal taxonomy. The taxonomy is
// deliberately smaller than the schema's wire-type vocabulary: many wire
// types (prom.metrics, otel.metrics) are the same signal carried by a
// different protocol, and callers reasoning about "does this pipeline ship
// logs" want the signal, not the wire.
type Signal string

const (
	// Metrics covers prom.metrics and otel.metrics.
	Metrics Signal = "metrics"
	// Logs covers loki.logs and otel.logs.
	Logs Signal = "logs"
	// Traces covers otel.traces.
	Traces Signal = "traces"
	// Profiles covers pyroscope.profiles.
	Profiles Signal = "profiles"
)

// All lists every taxonomy member in a stable display order. Every function
// in this package that produces an ordered view of a Set (Sorted, String)
// derives that order from All, so output is deterministic across runs and
// across map-iteration-order-sensitive Go versions.
var All = []Signal{Metrics, Logs, Traces, Profiles}

// Set is a de-duplicated, unordered collection of Signals. The zero value
// (nil) is a valid empty set and safe to read from (Has, Sorted, Empty all
// work on it); use NewSet or Add to build a non-nil one.
type Set map[Signal]struct{}

// NewSet builds a Set from the given signals, de-duplicating.
func NewSet(sigs ...Signal) Set {
	s := make(Set, len(sigs))
	for _, sig := range sigs {
		s[sig] = struct{}{}
	}
	return s
}

// Has reports whether sig is a member of s. Safe on a nil Set.
func (s Set) Has(sig Signal) bool {
	_, ok := s[sig]
	return ok
}

// Add returns a Set containing s's members plus sig. s is mutated in place
// when non-nil; when s is nil a new Set is allocated and returned, so callers
// must use the return value: set = set.Add(sig).
func (s Set) Add(sig Signal) Set {
	if s == nil {
		s = Set{}
	}
	s[sig] = struct{}{}
	return s
}

// Union returns a new Set containing every member of s and other. Neither
// input is mutated.
func (s Set) Union(other Set) Set {
	out := make(Set, len(s)+len(other))
	for k := range s {
		out[k] = struct{}{}
	}
	for k := range other {
		out[k] = struct{}{}
	}
	return out
}

// Sorted returns s's members in All's fixed display order.
func (s Set) Sorted() []Signal {
	out := make([]Signal, 0, len(s))
	for _, sig := range All {
		if s.Has(sig) {
			out = append(out, sig)
		}
	}
	return out
}

// Empty reports whether s has no members. Safe on a nil Set.
func (s Set) Empty() bool {
	return len(s) == 0
}

// Equal reports whether s and other have exactly the same members.
func (s Set) Equal(other Set) bool {
	if len(s) != len(other) {
		return false
	}
	for k := range s {
		if !other.Has(k) {
			return false
		}
	}
	return true
}

// String renders s in All's fixed order, e.g. "[metrics,logs]". Useful in
// error messages and test failure output; not meant to be parsed back.
func (s Set) String() string {
	sorted := s.Sorted()
	parts := make([]string, len(sorted))
	for i, sig := range sorted {
		parts[i] = string(sig)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
