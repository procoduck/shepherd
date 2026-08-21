package signals

import (
	"errors"
	"fmt"
)

// RolePolicy is one row of the role -> allowed-signals table: what a
// collector role legitimately handles, and why.
type RolePolicy struct {
	// Role is the collector role name, matching internal/agentapi's
	// validRoles ("metrics", "logs", "singleton", "receiver").
	Role string
	// Allowed is the set of signals a pipeline served to this role may
	// carry. Ignored when Unrestricted is true.
	Allowed Set
	// Unrestricted, when true, means this role has no signal restriction —
	// a deliberate table entry, not the default for a role missing from the
	// table entirely (see Enforce's ErrUnknownRole).
	Unrestricted bool
	// Rationale documents why this row allows what it allows. Required
	// reading before changing a row: a policy table with no rationale is a
	// claim, not a policy (per this package's W1 task brief).
	Rationale string
}

// Policies is the role -> allowed-signals policy table, keyed by role name.
// It is DATA: the merge engine (or any other caller enforcing role
// restrictions) is expected to consult this table via Enforce rather than
// re-encoding role/signal rules of its own. Changing what a role allows
// means changing a row here, in one place, with its rationale updated to
// match — not adding a special case somewhere else in the codebase.
//
// Rows, and why each allows what it allows:
//
//   - "metrics": Allowed = {Metrics}. A role=metrics collector is what
//     dashboards, alerting, and metrics-specific fleet tooling are pointed
//     at. A pipeline that also ships logs or traces there does not fail
//     loudly — the collector runs fine — it just means that signal is
//     invisible to whatever downstream system exists to read it, and the
//     mistake looks like "it's collecting something" rather than "it's
//     collecting the wrong thing" until someone goes looking for a specific
//     log line that was never routed anywhere a log system reads. Refusing
//     at config-merge time turns that into a loud, immediate error instead.
//
//   - "logs": Allowed = {Logs}. Mirror image of "metrics", same reasoning:
//     role=logs is the promise that log-shaped pipelines land there and
//     nothing else does.
//
//   - "receiver": Allowed = {Metrics, Logs, Traces}, excluding Profiles.
//     docs/gateway-tier-plan.md W4 defines the receiver tier as terminating
//     OTLP and Faro ingest from external clients (browsers, serverless
//     functions, SDKs) in front of Mimir/Loki/Tempo. OTLP's data model
//     natively carries all three of metrics, logs and traces; Faro (browser
//     RUM) carries logs, traces, and web-vitals-style measurements shaped as
//     metrics. Profiles is deliberately excluded: nothing in the receiver
//     tier's stated scope (W4's summary names OTLP and Faro only, not
//     Pyroscope ingest) terminates profiling data, and — per wiretype.go's
//     otel.any rationale — this schema has no otelcol bridge for profiles
//     at all, so a pipeline that somehow carries Profiles and targets a
//     receiver-role collector is exactly the kind of role/signal mismatch
//     this table exists to catch, not a gap in this row.
//
//   - "singleton": Unrestricted = true. Per internal/agentapi's validRoles
//     comment and docs/gateway-tier-plan.md, singleton is the one-per-fleet
//     role reserved for self-monitoring and other cross-cutting pipelines
//     that legitimately mix signal kinds (e.g. a self-monitoring pipeline
//     that scrapes Alloy's own /metrics AND tails its own log output).
//     Restricting it would make it useless for the role it plays. This is a
//     narrow, explicit carve-out — a row a reviewer chose — never a fallback
//     for a role this table doesn't otherwise describe; see Enforce.
var Policies = map[string]RolePolicy{
	"metrics": {
		Role:    "metrics",
		Allowed: NewSet(Metrics),
		Rationale: "A role=metrics collector is what dashboards, alerting, and metrics-specific " +
			"fleet tooling are pointed at; a pipeline that also ships logs or traces there is " +
			"silently invisible to whatever is supposed to read that signal, not loudly broken.",
	},
	"logs": {
		Role:    "logs",
		Allowed: NewSet(Logs),
		Rationale: "Mirror of \"metrics\": role=logs is the promise that log-shaped pipelines " +
			"land there and nothing else does.",
	},
	"receiver": {
		Role:    "receiver",
		Allowed: NewSet(Metrics, Logs, Traces),
		Rationale: "The receiver tier (gateway-tier-plan.md W4) terminates OTLP and Faro ingest, " +
			"which together cover metrics, logs and traces. Profiles is excluded: nothing in W4's " +
			"stated scope ingests profiling data, and this schema has no otel.any bridge to " +
			"pyroscope.profiles (see wiretype.go), so a pipeline carrying Profiles at a receiver " +
			"collector is a genuine role mismatch, not a gap in this row.",
	},
	"singleton": {
		Role:         "singleton",
		Unrestricted: true,
		Rationale: "singleton is the one-per-fleet role for self-monitoring and other " +
			"cross-cutting pipelines that legitimately mix signal kinds; restricting it would " +
			"make it useless for the role it plays. This is a table row a reviewer chose, not " +
			"the default for a role absent from the table (see ErrUnknownRole).",
	},
}

// ErrUnknownRole is returned by Enforce for a role Policies has no row for.
// An unrecognized role is refused, not waved through: "unrestricted" is a
// deliberate row in this table, never what an absent row defaults to. This
// also means a new role added to internal/agentapi's validRoles without a
// matching row here fails closed, loudly, the first time Enforce is called
// for it — rather than silently allowing everything.
var ErrUnknownRole = errors.New("signals: unknown collector role")

// ErrSignalMismatch is returned by Enforce when sig contains a signal the
// role's policy does not allow.
var ErrSignalMismatch = errors.New("signals: pipeline signal not allowed for role")

// Enforce checks a derived signal set against role's policy row. It returns
// nil when every signal in sig is allowed for role.
//
// Enforce trusts sig as given: it does not know whether sig came from a fully
// PROVEN Signals.Combined or a partial one. A caller that derived sig via
// Derive and got back Signals.HasUnknown() == true or a non-empty
// Signals.Unclassified has NOT proven the pipeline's true signal set, and
// must decide how to treat that (this package's fail-safe recommendation is
// to refuse, or to widen sig to every Signal before calling Enforce) before
// calling Enforce — Enforce itself only ever sees, and only ever checks, the
// signals it is handed.
func Enforce(role string, sig Set) error {
	policy, ok := Policies[role]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRole, role)
	}
	if policy.Unrestricted {
		return nil
	}

	var disallowed []Signal
	for _, s := range sig.Sorted() {
		if !policy.Allowed.Has(s) {
			disallowed = append(disallowed, s)
		}
	}
	if len(disallowed) == 0 {
		return nil
	}
	return fmt.Errorf("%w: role %q allows %s, pipeline carries %s",
		ErrSignalMismatch, role, policy.Allowed, NewSet(disallowed...))
}
