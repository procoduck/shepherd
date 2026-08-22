package reconcile

import (
	"fmt"

	"shepherd/internal/signals"
)

// Compare performs W6's three-way reconciliation for one collector
// instance: declared (its role) vs served (the pipelines Shepherd actually
// assembled for it) vs observed (what its beacon reported actually
// running). It returns every contradiction it can prove, never a
// "everything agrees" summary — a caller that wants a clean-bill-of-health
// signal checks len(findings) == 0 itself.
//
// Compare is pure: no I/O, no clock, no database — every fact it reasons
// about is in its three arguments. It returns a non-nil error only for an
// unrecognized declared.Role (a data-integrity bug upstream of
// reconciliation, not a source disagreement); every genuine three-way
// contradiction comes back as a Finding, never an error.
func Compare(declared Declared, served []ServedPipeline, observed Observed) ([]Finding, error) {
	// Validate the declared role by going through signals.Enforce itself
	// (never by indexing signals.Policies directly — Enforce is the table's
	// documented entry point) with an empty check set: an empty Set is
	// trivially within any real role's Allowed list, so this call can only
	// ever fail on signals.ErrUnknownRole, never on ErrSignalMismatch. That
	// isolates "is this role even real" from "does served content violate
	// it" cleanly, using the exact same lookup internal/merge relies on.
	if err := signals.Enforce(declared.Role, signals.Set{}); err != nil {
		return nil, fmt.Errorf("reconcile: declared role: %w", err)
	}

	var findings []Finding
	findings = append(findings, declaredVsServed(declared, served)...)
	findings = append(findings, servedVsObserved(declared, served, observed)...)
	return findings, nil
}

// declaredVsServed checks every served pipeline's signals against the
// declared role's policy — the reconciliation-time re-check of gate G6,
// independent of whether internal/merge.WithRoleEnforcement actually ran
// when serve_cache was last computed. A pipeline that fails this check is
// currently being served in violation of its collector's own role, right
// now, regardless of why.
func declaredVsServed(declared Declared, served []ServedPipeline) []Finding {
	var findings []Finding
	for _, sp := range served {
		checkSet := sp.Signals.Combined
		proven := sp.Signals.Proven()
		if !proven {
			// Mirror internal/merge/enforce.go's enforceRoles exactly: an
			// unproven signal set is a floor, not the true set, so checking
			// the floor against a restrictive role would let an
			// under-counted pipeline through undetected. Same fail-safe
			// stance, reused rather than re-derived.
			checkSet = signals.NewSet(signals.All...)
		}

		err := signals.Enforce(declared.Role, checkSet)
		if err == nil {
			continue
		}
		// declared.Role was already validated in Compare, so the only
		// failure Enforce can return here is ErrSignalMismatch.
		summary := fmt.Sprintf("pipeline %q: %s", sp.Name, err.Error())
		if !proven {
			summary += fmt.Sprintf(" (signal set not provable for %q: assumed worst-case, per internal/merge's fail-safe convention)", sp.Name)
		}
		findings = append(findings, Finding{
			Kind:         KindRoleSignalMismatch,
			Sources:      [2]Source{SourceDeclared, SourceServed},
			Summary:      summary,
			PipelineName: sp.Name,
		})
	}
	return findings
}

// servedVsObserved reports every observed component identity that no
// served pipeline claims. It is built entirely from Observed's own POSITIVE
// entries: the loop only ever ranges over observed.Components, so a served
// pipeline that Observed simply does not mention can never produce a
// Finding here — see the package doc's "absence is never disagreement".
// declared is passed through only to give the Finding's Summary
// human-readable context; it plays no role in the identity comparison
// itself (see the package doc's "why only two Finding Kinds").
func servedVsObserved(declared Declared, served []ServedPipeline, observed Observed) []Finding {
	expected := make(map[string]string, len(served)) // controllerPath -> pipeline name
	for _, sp := range served {
		expected[sp.ControllerPath] = sp.Name
	}

	var findings []Finding
	reported := make(map[string]bool, len(observed.Components)) // de-dupe repeated rows
	for _, oc := range observed.Components {
		if _, ok := expected[oc.ControllerPath]; ok {
			continue
		}
		if reported[oc.ControllerPath] {
			continue
		}
		reported[oc.ControllerPath] = true

		staleNote := ""
		if oc.Stale {
			staleNote = " (stale: last report is old enough to be pending sweep expiry — still evidence it WAS running, not proof it still is)"
		}
		findings = append(findings, Finding{
			Kind:    KindUnservedComponentObserved,
			Sources: [2]Source{SourceServed, SourceObserved},
			Summary: fmt.Sprintf(
				"declared role %q; beacon observed component %q running (healthy=%t) that Shepherd never served to this collector%s",
				declared.Role, oc.ControllerPath, oc.Healthy, staleNote,
			),
			ControllerPath: oc.ControllerPath,
			Stale:          oc.Stale,
		})
	}
	return findings
}
