package wizard

import (
	"errors"
	"fmt"
	"sync"

	"shepherd/internal/schema"
	"shepherd/internal/signals"
	"shepherd/internal/version"
)

// pinnedRegistry lazily builds the *schema.Registry every wizard's role
// check is derived against, from the same embedded artifact + fleet-pinned
// version every other production caller uses (internal/server,
// internal/mgmtapi/router.go: schema.New(schema.Embedded,
// version.AlloySchemaVersion)). Built once: schema.Registry already caches
// its merged payload internally, so a second instance would only duplicate
// that cache for no benefit.
var (
	pinnedRegistryOnce sync.Once
	pinnedReg          *schema.Registry
	pinnedRegErr       error
)

func pinnedRegistry() (*schema.Registry, error) {
	pinnedRegistryOnce.Do(func() {
		pinnedReg, pinnedRegErr = schema.New(schema.Embedded, version.AlloySchemaVersion)
	})
	if pinnedRegErr != nil {
		// Fails loudly rather than silently skipping the check: a wizard
		// committing without ever having proven its role is exactly the
		// "control disabled by its own wiring" failure class
		// docs/gateway-tier-plan.md §8 rule 3 warns against.
		return nil, fmt.Errorf("wizard: load pinned schema %s: %w", version.AlloySchemaVersion, pinnedRegErr)
	}
	return pinnedReg, nil
}

// checkRole derives contents' signal set against the pinned schema artifact
// and checks it against role's policy row (internal/signals.Enforce). It is
// the mechanism that makes gate G6 hold for wizard-generated pipelines: a
// wizard whose generated output does not match its declared collector role
// is refused here, before Commit's caller ever sees success.
//
// checkRole itself has no opinion about WHO calls it; roleEnforced.Commit is
// the only caller in this package, and Register makes that wrapper the only
// way onto the default registry — see wizard.go. checkRole is kept as its
// own function so it has its own, narrower test surface (role_test.go)
// independent of any specific wizard's template output.
func checkRole(role, contents string) error {
	reg, err := pinnedRegistry()
	if err != nil {
		return err
	}
	sig, err := signals.Derive(contents, reg)
	if err != nil {
		return fmt.Errorf("wizard: derive signals from generated pipeline: %w", err)
	}

	// Same fail-safe posture as internal/merge/enforce.go's enforceRoles: an
	// unproven signal set (an unrecognized top-level component, or an
	// unclassified wire type — see signals.Signals.Proven) is a FLOOR on
	// what the pipeline carries, not the true set. Checking the floor
	// against a restrictive role's allow-list would let an under-counted
	// pipeline through, so an unproven set is widened to every signal
	// before the check — never silently treated as "nothing more".
	checkSet := sig.Combined
	if !sig.Proven() {
		checkSet = signals.NewSet(signals.All...)
	}

	if err := signals.Enforce(role, checkSet); err != nil {
		return fmt.Errorf("generated pipeline does not match its declared role %q: %w", role, err)
	}
	return nil
}

// roleEnforced wraps a registered Wizard so every Commit call is checked
// against Role's declared collector role before the caller — including
// internal/mgmtapi's WizardService, which calls wiz.Commit(state) directly —
// ever sees a result. This is what turns "a wizard cannot generate a
// pipeline that lands on the wrong collector role"
// (docs/gateway-tier-plan.md W8) into a property of the registry rather than
// something each wizard's Commit must remember to do for itself: even a
// future wizard that forgets to self-check is still covered, because
// Register (wizard.go) is the only path onto defaultRegistry and it always
// wraps.
type roleEnforced struct {
	Wizard
}

// Commit calls the wrapped wizard's own Commit, then refuses the result if
// its generated contents carry a signal the declared role does not allow. A
// refusal here discards the whole result rather than returning a partial
// one: an operator seeing a role mismatch needs to fix wizard input, not
// receive a pipeline this package already knows is wrong.
func (r roleEnforced) Commit(state map[string]any) (CommitResult, error) {
	result, err := r.Wizard.Commit(state)
	if err != nil {
		return CommitResult{}, err
	}
	role := r.Role(state)
	if checkErr := checkRole(role, result.Contents); checkErr != nil {
		// Unparseable output is not a role mismatch, and reporting it as one
		// destroys the only useful thing about it. RenderWizard is a PREVIEW:
		// an operator whose input produced broken Alloy needs Stage 1's
		// line-level diagnostics, and turning that into a failed RPC replaced
		// them with "does not match its declared role" — which is also untrue,
		// since nothing was determined about the role at all.
		//
		// This is not a hole in gate G6, but be precise about why, because the
		// obvious reason is wrong: CommitWizard does NOT Stage-1 validate
		// before saving (only RenderWizard, the preview, does), so unparseable
		// wizard output can reach the database. What it cannot do is reach a
		// COLLECTOR: merge's enforceRoles excludes a pipeline whose signals
		// cannot be derived, fail-safe, and recomputeServeCache Stage-1 checks
		// the assembled output before writing serve_cache. So the guarantee is
		// "cannot be served", not "cannot be stored" — a weaker claim than the
		// earlier wording here made, and the accurate one.
		if errors.Is(checkErr, signals.ErrParse) {
			result.Role = role
			return result, nil
		}
		return CommitResult{}, fmt.Errorf("wizard %q: %w", r.Kind(), checkErr)
	}
	result.Role = role
	return result, nil
}
