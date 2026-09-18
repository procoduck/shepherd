// Package serve implements ComputeServed, the single merge -> append-baseline
// -> hash -> Stage-1-validate pipeline shared by the lazy recompute path
// (internal/agentapi.Service.GetConfig, when serve_cache is dirty) and the
// eager one (internal/mgmtapi's recompute after a pipeline write). Both
// paths must produce the SAME served config for the same collector and
// pipeline set — docs/gateway-tier-plan.md §10 recorded this exact trap from
// W1: "two paths produce the same served config; enforcing one of them is
// not enforcement" — so this package exists to make that true by
// construction rather than by two implementations staying in sync by hand.
package serve

import (
	"context"
	"fmt"

	"shepherd/internal/beacon"
	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
)

// Deps bundles ComputeServed's dependencies beyond the collector and its
// pipelines.
type Deps struct {
	// Schema drives role/signal enforcement (merge.WithRoleEnforcement) when
	// enforcement applies — see EnforceRoles.
	Schema *schema.Registry
	// EnforceRoles controls what a nil Schema means, because the two former
	// call sites disagreed on purpose:
	//   - false (internal/agentapi's prior behavior): WithRoleEnforcement is
	//     applied only when Schema is non-nil. A nil Schema (a corrupt or
	//     unloaded embedded schema artifact) degrades to unenforced config
	//     rather than refusing to serve anything at all.
	//   - true (internal/mgmtapi's prior behavior): WithRoleEnforcement(Schema)
	//     is applied unconditionally, nil Schema included. merge.Assemble
	//     then refuses outright ("role enforcement was requested but the
	//     schema registry is nil") rather than silently serving unenforced
	//     config that would look enforced — see its own doc comment.
	// Getting this backwards for either caller is exactly the kind of
	// two-path drift docs/gateway-tier-plan.md §10 warns about, so it is a
	// field here, never a package-level default.
	EnforceRoles bool
	// BeaconBaseline configures D6's baseline pipeline, appended to the
	// merged config independent of role/matchers. The zero value
	// (RemoteWriteURL == "") is beacon.AppendBaseline's documented no-op.
	BeaconBaseline beacon.BaselineConfig
}

// Collector is the minimal collector identity ComputeServed needs to select
// and label matching pipelines.
type Collector struct {
	ID      string
	Cluster string
	Role    string
}

// Result is what a caller stores in serve_cache.
type Result struct {
	// Content is the final served config: merged pipelines plus D6's
	// baseline pipeline (when configured), Stage-1-validated.
	Content string
	// Hash is sha256hex(Content) — always merge.HashContent(Content), even
	// when appending the baseline changed Content after merge.Assemble
	// computed its own hash.
	Hash string
	// Exclusions lists every pipeline that matched the collector's labels
	// but was left out of Content because of role/signal enforcement — see
	// merge.AssembleResult.Exclusions. Callers log these; ComputeServed does
	// not, to stay logger-independent.
	Exclusions []merge.Exclusion
	// BaselineErr is set when beacon.AppendBaseline failed to render D6's
	// baseline pipeline. Content is then the merged output WITHOUT the
	// baseline (degrade, don't fail the whole collector over a baseline
	// render bug) — callers should log BaselineErr at their own level.
	BaselineErr error
}

// ComputeServed assembles the merged Alloy config for one collector, appends
// D6's baseline pipeline, hashes the result, and Stage-1-validates the FINAL
// content — baseline included — before returning it.
//
// Stage 1 always runs, unconditionally: there is no Deps field that skips
// it. That is the one behavioral difference from the two implementations
// this replaces — agentapi's former recomputeServeCache only ran it "if
// s.validator != nil", which no test and no production wiring ever left
// nil-and-serving, but which meant the gate had a hole shaped exactly like
// that. A merged config that fails to parse must never reach serve_cache,
// role enforcement configured or not.
func ComputeServed(_ context.Context, deps Deps, coll Collector, pipelines []merge.Pipeline) (Result, error) {
	cl := merge.CollectorLabels{
		CollectorID: coll.ID,
		Labels:      map[string]string{"role": coll.Role, "cluster": coll.Cluster},
	}

	var opts []merge.AssembleOption
	if deps.EnforceRoles || deps.Schema != nil {
		opts = append(opts, merge.WithRoleEnforcement(deps.Schema))
	}
	assembled, err := merge.Assemble(coll.ID, coll.Cluster+"/"+coll.Role, cl, pipelines, "prod", "", opts...)
	if err != nil {
		return Result{}, fmt.Errorf("assembling config: %w", err)
	}

	// D6: every collector gets the baseline pipeline appended, independent
	// of role/matchers — see beacon.AppendBaseline's doc comment for why
	// this is a plain append rather than another entry in pipelines (role
	// enforcement above would reject it for e.g. role=logs collectors; D6
	// says "not opt-in", not "opt-in for roles that happen to allow
	// Metrics"). A no-op when deps.BeaconBaseline is the zero value.
	content := assembled.Content
	var baselineErr error
	// #110: stamp this collector's id into its baseline so the beacon writes it
	// produces can be attributed back to it for reconciliation. BeaconBaseline
	// is otherwise identical across collectors; CollectorID is the one
	// per-collector field, set here rather than by the caller so both serve
	// paths get it.
	baselineCfg := deps.BeaconBaseline
	baselineCfg.CollectorID = coll.ID
	if appended, appendErr := beacon.AppendBaseline(content, baselineCfg); appendErr != nil {
		// Render failure is a static-config bug (a bad Label, say), not a
		// per-collector condition — degrade to serving without the baseline
		// rather than taking the collector's config offline over it. The
		// caller decides how loudly to report baselineErr.
		baselineErr = appendErr
	} else {
		content = appended
	}

	hash := assembled.Hash
	if content != assembled.Content {
		hash = merge.HashContent(content)
	}

	// Stage 1 on the FINAL served output, baseline included — a broken
	// baseline render must fail the same way a broken user pipeline would,
	// not slip through because it was appended after this check.
	if r1 := validate.Stage1(content); !r1.Valid {
		return Result{}, fmt.Errorf("merged config failed stage-1 validation")
	}

	return Result{
		Content:     content,
		Hash:        hash,
		Exclusions:  assembled.Exclusions,
		BaselineErr: baselineErr,
	}, nil
}
