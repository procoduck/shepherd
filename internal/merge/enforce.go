package merge

import (
	"fmt"
	"strings"

	"shepherd/internal/schema"
	"shepherd/internal/signals"
)

// Exclusion records one pipeline that matched a collector's labels but was
// left out of the assembled config because its derived signals are not
// allowed for the collector's role. This is the visible record gate G6
// (docs/gateway-tier-plan.md §8 rule 2) requires: excluding the offending
// pipeline is correct (fail-safe), but the exclusion must never be silent.
type Exclusion struct {
	// PipelineName is the excluded pipeline's Name.
	PipelineName string
	// Reason is a human-readable explanation, safe to embed verbatim in a
	// "// " header comment line (never contains a raw newline — see
	// buildHeader).
	Reason string
	// Role is the collector role the pipeline was checked against.
	Role string
	// Disallowed is the set of the pipeline's signals Role does not allow.
	// Empty when the exclusion is not a plain mismatch (signal derivation
	// failed, or the role is unknown) — Reason then carries the why. When the
	// signal set could not be proven, this is the worst-case set the check
	// assumed, not a proof the pipeline carries every one of them.
	//
	// Role and Disallowed exist for callers that surface an exclusion to a
	// person (the pipeline editor, a collector's reconciliation tab); the
	// served header prints only PipelineName and Reason, so they never change
	// served bytes.
	Disallowed signals.Set
	// Unproven is set when the pipeline's signal set could not be proven (an
	// unknown component or an unclassified wire type), so Disallowed is the
	// worst case assumed rather than what the pipeline was shown to carry:
	// "signal set not provable: unknown components [...], unclassified wire
	// types [...] — assumed worst-case". Reason already ends with it in
	// parentheses; it is carried separately so operator-facing prose built
	// from Disallowed never drops it.
	Unproven string
}

// AssembleOption configures optional Assemble behavior. The zero value (no
// options) reproduces Assemble's pre-W1 behavior exactly: pipelines are
// selected by label/matcher only, with no signal/role check.
type AssembleOption func(*assembleConfig)

type assembleConfig struct {
	registry *schema.Registry
	// enforcementRequested records that WithRoleEnforcement was passed at all,
	// separately from whether it carried a usable registry. Without this, a
	// caller that asks for enforcement but hands over a nil registry gets
	// silence — the control disabled by the very call that requested it.
	enforcementRequested bool
}

// WithRoleEnforcement turns on signal/role enforcement (gate G6,
// docs/gateway-tier-plan.md W1): among the pipelines that already matched
// the collector's labels, one whose derived signal set (via signals.Derive
// against reg) is not allowed for the collector's role
// (CollectorLabels.Labels["role"]) is excluded from the assembled config
// rather than emitted into it.
//
// This is fail-safe, not fail-stop: a single bad pipeline is dropped, never
// the whole assembly. Every exclusion — the pipeline name and why — is
// recorded in the generated header comment and returned in
// AssembleResult.Exclusions, so callers can surface it instead of it being
// silently missing from a collector's config.
//
// Callers that do not pass this option keep the old, unenforced behavior.
//
// BOTH serving paths pass it: internal/mgmtapi's eager recompute and
// internal/agentapi.Service.recomputeServeCache, the lazy path taken when
// serve_cache is dirty. That was not true when this option was first written
// — the agent path was left unwired because the *schema.Registry had not been
// threaded through internal/agentapi.Service — and this comment used to say
// so. It was closed in the same session, and G6 is proven on the agent path
// specifically (internal/agentapi/service_test.go, "does not serve a metrics
// pipeline to a logs collector through the dirty-window path"), because
// enforcing one of two paths that produce the same served config is not
// enforcement.
//
// The stale wording survived until a review caught it, which is worth a note
// of its own: a comment that DENIES a control now wired misleads exactly as
// badly as one claiming a control that is not.
// Passing a nil registry is a wiring error, not a way to opt out: Assemble
// fails loudly rather than serving unenforced config that looks enforced. To
// genuinely opt out, do not pass the option.
func WithRoleEnforcement(reg *schema.Registry) AssembleOption {
	return func(c *assembleConfig) {
		c.registry = reg
		c.enforcementRequested = true
	}
}

// enforceRoles filters selected (already label/matcher-matched) pipelines by
// signals.Enforce against the collector's role, returning the pipelines that
// pass and a record of every exclusion, in selected's order.
//
// Unproven signal sets (Signals.Proven() == false — an unrecognized
// top-level component, or, only if internal/signals' wire-type table has
// drifted from the pinned schema artifact, an unclassified wire type) are
// treated as carrying every signal, never as carrying only what WAS proven.
// Combined is a floor in that case, not the true set; checking the floor
// against a restrictive role's allow-list would let an under-counted
// pipeline through. This only bites roles with a real allow-list
// (metrics/logs/receiver) — "singleton" is Unrestricted and short-circuits
// in signals.Enforce regardless of the checked set, so an unrecognized
// component newer than the pinned schema cannot break self-monitoring
// pipelines just by existing. See docs/gateway-tier-plan.md §5's W1 note:
// "must not silently downgrade a mismatch to a warning" — treating unproven
// as safe-by-default would be exactly that downgrade in disguise.
func enforceRoles(selected []Pipeline, cl CollectorLabels, reg *schema.Registry) ([]Pipeline, []Exclusion) {
	role := cl.Labels["role"]
	kept := make([]Pipeline, 0, len(selected))
	var exclusions []Exclusion

	for _, p := range selected {
		if ex, excluded := RoleExclusion(p, role, reg); excluded {
			exclusions = append(exclusions, ex)
			continue
		}
		kept = append(kept, p)
	}
	return kept, exclusions
}

// RoleExclusion reports whether role enforcement excludes p from a collector
// of the given role, and if so the Exclusion it records. It is the one
// per-pipeline check enforceRoles runs, exported so the surfaces that warn an
// operator about an exclusion (the pipeline's matched-collector preview, a
// collector's reconciliation) ask the same question the served config was
// assembled with, rather than re-deriving the policy a second way.
func RoleExclusion(p Pipeline, role string, reg *schema.Registry) (Exclusion, bool) {
	_, ex, excluded := RoleCheck(p, role, reg)
	return ex, excluded
}

// RoleCheck is RoleExclusion that also returns the signal set it derived, for
// a caller that needs the signals of a pipeline that IS served (the
// reconciliation tab) — so it does not derive them a second time. sig is the
// zero value when derivation failed (the pipeline is then excluded).
func RoleCheck(p Pipeline, role string, reg *schema.Registry) (signals.Signals, Exclusion, bool) {
	sig, err := signals.Derive(p.Contents, reg)
	if err != nil {
		return signals.Signals{}, Exclusion{
			PipelineName: p.Name,
			Role:         role,
			Reason:       commentSafe(fmt.Sprintf("signal derivation failed, excluded fail-safe: %v", err)),
		}, true
	}

	checkSet := sig.Combined
	var unproven string
	if !sig.Proven() {
		checkSet = signals.NewSet(signals.All...)
		unproven = fmt.Sprintf("signal set not provable: unknown components %v, unclassified wire types %v — assumed worst-case",
			unknownComponentNames(sig.Unknown), sig.Unclassified)
	}

	enforceErr := signals.Enforce(role, checkSet)
	if enforceErr == nil {
		return sig, Exclusion{}, false
	}
	reason := enforceErr.Error()
	if unproven != "" {
		reason += " (" + unproven + ")"
	}
	return sig, Exclusion{
		PipelineName: p.Name,
		Role:         role,
		Reason:       commentSafe(reason),
		Disallowed:   signals.Disallowed(role, checkSet),
		Unproven:     commentSafe(unproven),
	}, true
}

// unknownComponentNames extracts just the component names from a
// []signals.UnknownComponent, for compact display in an exclusion reason.
func unknownComponentNames(u []signals.UnknownComponent) []string {
	names := make([]string, len(u))
	for i, c := range u {
		names[i] = c.Component
	}
	return names
}

// commentSafe collapses any embedded line break to a space. Everything the
// header interpolates — exclusion reasons, pipeline names, the collector
// display name — is written verbatim into a "// " comment line of the
// generated Alloy config; a raw newline would end the comment and turn the
// remainder into syntax, which Stage 1 then rejects for the whole assembled
// output. Names come from the database and the API only requires them to be
// non-empty, so the header is the last line of defence.
func commentSafe(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}
