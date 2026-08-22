// Package reconcile implements W6's three-way reconciliation
// (docs/gateway-tier-plan.md §4): declared (the collector's own stated
// role) vs served (the signals of the pipelines Shepherd actually assembled
// for it) vs observed (the beacon's component inventory for it). It returns
// typed findings for every contradiction it can prove, never a bare
// "inconsistent" verdict.
//
// This package is a pure library: no database, no HTTP, no clock. Compare
// takes its three inputs as plain arguments and is fully exercised by
// table-driven tests with no cluster or fixture database (see
// reconcile_test.go). Wiring — reading a collector's role, its currently
// served pipelines, and its beacon_inventory rows, and calling Compare with
// them — is the caller's job (internal/mgmtapi or internal/agentapi), not
// this package's; see the W6 session report for what that wiring needs.
//
// # Reuse, not re-derivation
//
// Compare does not re-implement signal classification or role policy. It
// calls signals.Enforce against the exact signals.Policies table
// internal/merge's WithRoleEnforcement already consults (gate G6), and it
// expects ServedPipeline.Signals to already be signals.Derive's output —
// the same call internal/merge/enforce.go's enforceRoles makes on the same
// pipeline content. Two different callers asking "what does this pipeline
// carry" always get the answer from the one function that computes it.
//
// # Why only two Finding Kinds, not three
//
// A pairwise reconciliation over three sources has three possible pairs,
// but Observed carries only component identity and health — never a
// signal, never config text (D6). Compare can therefore prove:
//
//   - Declared vs Served (KindRoleSignalMismatch): does a pipeline Shepherd
//     actually assembled for this collector carry a signal its declared
//     role's policy disallows?
//   - Served vs Observed (KindUnservedComponentObserved): does the beacon
//     report a component identity that no served pipeline claims — i.e.
//     something running that Shepherd never put there?
//
// A direct Declared-vs-Observed check would require classifying a bare
// observed component identity into a signal, which is exactly the "invent
// richer observed data than exists" this package's task brief forbids —
// Observed's own identity string is not, by itself, proof of what signal it
// carries. Where a Finding's Summary reads like "declared role X, but
// beacon observed Y", the two SOURCES that actually disagree (Finding.Sources)
// are still Served and Observed; the declared role is included as
// human-readable context, not as a third leg of that particular check.
//
// # Absence is never disagreement
//
// The single most important correctness property here: a collector that
// has never reported a beacon — or whose entire reported inventory has
// aged out of beacon_inventory (internal/agentapi's Sweeper) — is UNKNOWN,
// not "running nothing". Observed represents both cases identically (an
// empty Components slice), and Compare never manufactures a Finding from an
// empty or partial Observed: every Finding this package emits is built from
// a POSITIVE fact one of the three sources actually asserted, never from
// one being silently absent. See TestNoInventoryNeverContradicts and
// TestCompare_StaleObservationStillCountsButIsFlagged.
package reconcile
