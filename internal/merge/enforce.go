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
// As of this change that includes internal/agentapi's own call to Assemble:
// wiring the *schema.Registry through internal/agentapi.Service was outside
// this task's declared territory (internal/merge/** and internal/mgmtapi/**
// only — docs/gateway-tier-plan.md §8). See the W1 session notes for the
// consequence: agentapi's lazy serve-cache recompute path is not yet
// enforced, only internal/mgmtapi's eager one is.
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
		sig, err := signals.Derive(p.Contents, reg)
		if err != nil {
			exclusions = append(exclusions, Exclusion{
				PipelineName: p.Name,
				Reason:       sanitizeReason(fmt.Sprintf("signal derivation failed, excluded fail-safe: %v", err)),
			})
			continue
		}

		checkSet := sig.Combined
		var unprovenNote string
		if !sig.Proven() {
			checkSet = signals.NewSet(signals.All...)
			unprovenNote = fmt.Sprintf(" (signal set not provable: unknown components %v, unclassified wire types %v — assumed worst-case)",
				unknownComponentNames(sig.Unknown), sig.Unclassified)
		}

		if enforceErr := signals.Enforce(role, checkSet); enforceErr != nil {
			exclusions = append(exclusions, Exclusion{
				PipelineName: p.Name,
				Reason:       sanitizeReason(enforceErr.Error() + unprovenNote),
			})
			continue
		}
		kept = append(kept, p)
	}
	return kept, exclusions
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

// sanitizeReason collapses any embedded newline to a space. Reasons are
// written verbatim into a "// " comment line in the generated Alloy header;
// a raw newline would break out of the comment and could corrupt the
// generated syntax.
func sanitizeReason(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
}
