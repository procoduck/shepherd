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
	"errors"
	"fmt"

	"shepherd/internal/beacon"
	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/validate"
)

// Deps bundles ComputeServed's dependencies beyond the collector and its
// pipelines.
type Deps struct {
	// Schema drives role/signal enforcement (merge.WithRoleEnforcement),
	// which is always on: there is no unenforced serve. A nil Schema is a
	// programming error — internal/server loads the registry once at boot and
	// refuses to start without it — so Assemble returns an error rather than
	// serving config that would look enforced and is not.
	Schema *schema.Registry
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
	// AdminLabels is the collector's "Manage labels" (collectors.labels), to
	// merge into matching alongside cluster/role. The caller is responsible
	// for gating this on the collector's org having allow_label_matching
	// enabled (procoduck/shepherd#139) — passing nil/empty here reproduces
	// exactly the pre-#139 {cluster, role}-only behavior.
	AdminLabels map[string]string
	// LocalAttrs is the collector's agent-reported local_attributes, to
	// merge into matching alongside cluster/role and AdminLabels (losing to
	// both — see merge.BuildCollectorLabels' precedence doc). The caller is
	// responsible for gating this on the collector's org having
	// allow_local_attribute_matching enabled — passing nil/empty here
	// reproduces the pre-local_attributes-matching behavior. Unused by any
	// caller yet (LABEL-MATCHING-PLAN.md PR-8 is plumbing only until the
	// gated read lands).
	LocalAttrs map[string]string
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
	// Provenance is how Content was built — contributors with their line
	// spans in Content, exclusions, the baseline's lines (F-CONTRIB, #423).
	// Metadata only: it never changes Content or Hash. Store writes it with
	// them.
	Provenance Provenance
}

// errNilSchema is returned when Deps.Schema is nil: see Deps.Schema.
var errNilSchema = errors.New("serve: schema registry is nil; refusing to compute served config without role enforcement")

// Assemble builds the exact bytes ComputeServed would serve one collector —
// merged pipelines with role enforcement, plus D6's baseline pipeline stamped
// with the collector's id — and their hash, WITHOUT validating them. It is
// ComputeServed minus Stage 1, exported so internal/mgmtapi's Stage 3 dry run
// validates what is actually served (baseline included) instead of a
// re-implementation of it that can drift.
func Assemble(deps Deps, coll Collector, pipelines []merge.Pipeline) (Result, error) {
	return assemble(deps, coll, pipelines, coll.ID, coll.Cluster+"/"+coll.Role)
}

// PlaceholderCollectorID and PlaceholderCollectorName stand in for a
// collector's id and "cluster/role" display name in AssembleForValidation's
// output. The id is a well-formed UUID so the stamped baseline carries the
// same shape of value it is served with.
const (
	PlaceholderCollectorID   = "00000000-0000-0000-0000-000000000000"
	PlaceholderCollectorName = "collector/role"
)

// AssembleForValidation is Assemble with the collector's identity normalised
// out of the bytes: the same pipelines are selected — matching, including a
// git pipeline's RepoLinkCollectorID, uses the real coll — but the header's
// "Collector:" line names PlaceholderCollectorName (PlaceholderCollectorID)
// and the D6 baseline stamps PlaceholderCollectorID as its
// shepherd_collector_id. Those are the only per-collector bytes Assemble
// writes, so collectors whose served configs differ only by identity get
// identical content and Stage 3 (internal/mgmtapi) validates them once
// (#376).
//
// It cannot change a validation verdict: the display name and id reach the
// header only through commentSafe inside a "//" comment line, and the
// baseline only as a %q-quoted string literal (beacon.RenderBaselinePipeline)
// — a comment's text and a string literal's value are never parsed by Stage
// 1 or checked by Stage 2, and both forms keep the same line count, so every
// diagnostic position is the same too. The placeholder is non-empty, so the
// baseline's collector-id rule is rendered exactly as it is when served.
func AssembleForValidation(deps Deps, coll Collector, pipelines []merge.Pipeline) (Result, error) {
	return assemble(deps, coll, pipelines, PlaceholderCollectorID, PlaceholderCollectorName)
}

// assemble is Assemble with the identity written into the bytes (header id
// and display name, baseline collector id) given separately from coll, which
// alone drives pipeline selection.
func assemble(deps Deps, coll Collector, pipelines []merge.Pipeline, stampID, stampName string) (Result, error) {
	if deps.Schema == nil {
		return Result{}, errNilSchema
	}
	cl := merge.BuildCollectorLabels(coll.ID, coll.Cluster, coll.Role, coll.AdminLabels, coll.LocalAttrs)

	assembled, err := merge.Assemble(stampID, stampName, cl, pipelines, merge.WithRoleEnforcement(deps.Schema))
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
	baselineCfg.CollectorID = stampID
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

	return Result{
		Content:     content,
		Hash:        hash,
		Exclusions:  assembled.Exclusions,
		BaselineErr: baselineErr,
		Provenance:  newProvenance(assembled, content, hash),
	}, nil
}

// ComputeServed assembles the merged Alloy config for one collector, appends
// D6's baseline pipeline, hashes the result, and Stage-1-validates the FINAL
// content — baseline included — before returning it.
//
// Stage 1 always runs, unconditionally: there is no Deps field that skips
// it. A merged config that fails to parse must never reach serve_cache.
// Role enforcement is equally unconditional (see Deps.Schema).
func ComputeServed(_ context.Context, deps Deps, coll Collector, pipelines []merge.Pipeline) (Result, error) {
	res, err := Assemble(deps, coll, pipelines)
	if err != nil {
		return Result{}, err
	}
	// Stage 1 on the FINAL served output, baseline included — a broken
	// baseline render must fail the same way a broken user pipeline would,
	// not slip through because it was appended after this check.
	if r1 := validate.Stage1(res.Content); !r1.Valid {
		return Result{}, fmt.Errorf("merged config failed stage-1 validation")
	}
	return res, nil
}
