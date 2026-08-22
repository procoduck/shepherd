package reconcile

import "shepherd/internal/signals"

// Declared is the collector's own stated identity for reconciliation
// purposes: the enforced role internal/store's collectors.role column
// carries — the same value internal/merge's CollectorLabels.Labels["role"]
// is built from and signals.Policies is keyed by. Compare never re-derives
// a "declared signal set" of its own: it asks signals.Policies (via
// signals.Enforce), the exact table internal/merge.WithRoleEnforcement
// consults, so "declared" here and the signals a live merge refusal would
// cite can never silently diverge into two tables saying two different
// things about the same role.
type Declared struct {
	// Role is the collector's enforced role, e.g. "metrics", "logs",
	// "receiver", "singleton" — must be a key in signals.Policies. Compare
	// fails loud (returns an error, not a Finding) for a role that isn't:
	// an unrecognized role is a data-integrity bug, not a source
	// disagreement to report as routine reconciliation drift.
	Role string

	// Attributes is the collector's raw local_attributes
	// (internal/store's collector_instances.local_attributes), carried
	// here purely so a caller can show it as context alongside a Finding.
	// Compare's comparison logic never reads Attributes — Role is the only
	// declared fact checked against Served and Observed.
	Attributes map[string]string
}

// ServedPipeline is one pipeline Shepherd has actually assembled into a
// collector's served config — what internal/merge.Assemble (called WITH
// WithRoleEnforcement) put in serve_cache, not merely what matched the
// collector's label matchers before role enforcement ran. Passing a
// pre-enforcement candidate list defeats the point of the Declared-vs-Served
// check: it would just re-discover what WithRoleEnforcement already refused
// to serve.
type ServedPipeline struct {
	// Name is the pipeline's display name (merge.Pipeline.Name), used only
	// to name the offending pipeline in a Finding's Summary.
	Name string

	// ControllerPath is the beacon controller_path identity this pipeline
	// reports under once a collector actually runs it: "pipe_" +
	// merge.SanitizeName(Name) for anything internal/merge.Assemble
	// declare-wraps (see internal/beacon.ComponentObservation's doc
	// comment), or internal/beacon's own baseline identity for D6's
	// un-wrapped baseline pipeline. Compare does not compute or guess this
	// string itself — the caller does, because the caller is the one that
	// actually knows Alloy's real controller_path convention (verified,
	// once, in internal/beacon against the pinned Alloy source per
	// docs/gateway-tier-plan.md §8 rule 6). Re-deriving that format here
	// would be exactly the second derivation this package's task brief
	// warns against, over a fact this package has no way to verify itself.
	ControllerPath string

	// Signals is signals.Derive's result for this pipeline's own content —
	// the same call internal/merge/enforce.go's enforceRoles makes on the
	// same content, reused here rather than re-derived.
	Signals signals.Signals
}

// ObservedComponent is one controller_path the beacon reported running for
// a collector instance, reconstructed from a beacon_inventory row without
// this package importing internal/store — see the package doc: pure
// library, no database dependency.
type ObservedComponent struct {
	// ControllerPath is the beacon-reported identity —
	// internal/beacon.ComponentObservation.ComponentName's exact value.
	ControllerPath string

	// Healthy mirrors internal/beacon.ComponentObservation.Healthy.
	Healthy bool

	// Stale is true when the caller's own freshness check — against
	// whatever TTL internal/agentapi's Sweeper actually uses; Compare has
	// no clock and computes nothing here — found this row old enough to be
	// due for expiry. A stale row is still POSITIVE evidence the component
	// WAS seen: Compare surfaces it as such (Finding.Stale) and never
	// upgrades it into, and never treats its absence as, a claim about
	// removal. See the package doc's "absence is never disagreement".
	Stale bool
}

// Observed is one collector instance's beacon inventory. An instance that
// has never reported a beacon at all, and one whose entire reported
// inventory has already aged out of beacon_inventory, are represented
// IDENTICALLY: Components is empty either way. Compare cannot — and does
// not try to — tell those two cases apart from Components alone, and
// treats both the same: zero findings from absence.
type Observed struct {
	Components []ObservedComponent
}

// Source identifies one of the three reconciliation inputs. A Finding names
// exactly two — the pair that disagrees — never all three and never one:
// see Finding.Sources.
type Source string

const (
	// SourceDeclared is the collector's own stated role (Declared).
	SourceDeclared Source = "declared"
	// SourceServed is the pipelines Shepherd actually assembled for the
	// collector (ServedPipeline).
	SourceServed Source = "served"
	// SourceObserved is the beacon's reported component inventory
	// (Observed).
	SourceObserved Source = "observed"
)

// Kind classifies the shape of a Finding's contradiction. See the package
// doc for why these are the only two kinds Compare can prove.
type Kind string

const (
	// KindRoleSignalMismatch fires when a pipeline Shepherd actually
	// assembled for this collector (Served) carries a signal the
	// collector's declared role (Declared) does not allow. Compare finding
	// this is itself notable: internal/merge.WithRoleEnforcement (gate G6)
	// should have excluded such a pipeline before it ever reached
	// serve_cache, so a KindRoleSignalMismatch finding means that
	// enforcement did not hold for THIS specific served config — a stale
	// serve_cache, a caller that forgot to pass WithRoleEnforcement, or a
	// genuine regression — not merely that the two sources "look
	// different".
	KindRoleSignalMismatch Kind = "role_signal_mismatch"

	// KindUnservedComponentObserved fires when the beacon (Observed)
	// reports a component identity running that no pipeline Shepherd
	// assembled (Served) claims. This is the shape of finding that catches
	// a BYO collector actually running something of its own — built
	// entirely from Observed's POSITIVE report, so it can never be produced
	// by a served pipeline's mere absence from Observed (see the package
	// doc's "absence is never disagreement").
	KindUnservedComponentObserved Kind = "unserved_component_observed"
)

// Finding is one contradiction Compare proved between exactly two of the
// three reconciliation sources.
type Finding struct {
	// Kind classifies the contradiction's shape.
	Kind Kind
	// Sources names which two inputs disagree. Order matches the Kind's
	// doc comment (Declared-then-Served, or Served-then-Observed) — it is
	// not alphabetical or otherwise arbitrary.
	Sources [2]Source
	// Summary is a human-readable sentence naming both sides' claims —
	// never a bare "inconsistent". Safe to display verbatim.
	Summary string

	// PipelineName is set for KindRoleSignalMismatch: the served pipeline
	// whose signals violated the declared role's policy. Empty for other
	// Kinds.
	PipelineName string

	// ControllerPath and Stale are set for KindUnservedComponentObserved:
	// the beacon-reported identity that matched no served pipeline, and
	// whether that observation is old enough that the caller's sweeper TTL
	// would treat the row as due for expiry. Empty/false for other Kinds.
	ControllerPath string
	Stale          bool
}
