package mgmtapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/wizard"
)

// Re-rendering wizard pipelines from their stored state (#262).
//
// A wizard renders a destination's URL and auth into the pipeline's stored
// contents when it commits (wizard.RenderWriter), so a later change to the
// destination reaches nothing on its own. UpdateDestination therefore
// re-runs the wizard of every pipeline that names the destination, and
// `shepherd admin rerender-destinations` does the same once for pipelines
// committed before #260 (the `sys.env("SHEPHERD_DEST_<NAME>_URL")` writer).
//
// Both paths write through the same gate a pipeline edit passes:
// Stages 1-2 on every re-rendered pipeline (UpdatePipeline's
// validateSaveInput), Stage 3 over the merged config with every enabled
// re-rendered pipeline swapped in at once (stage3CheckSet), then — inside the
// caller's transaction — the row update, a revision, a pipeline.rerender
// audit row, and the org's serve cache marked dirty.

// destinationRename is a destination's name change: wizard state names a
// destination, so a renamed destination's pipelines get their state
// rewritten to the new name along with their contents.
type destinationRename struct{ from, to string }

// wizardRerender is one pipeline whose re-render differs from what is
// stored: row is the pipeline as it will be written.
type wizardRerender struct {
	row          sqlc.Pipeline
	stateChanged bool
}

// rerenderFailure is one re-render that could not be stored: usually one
// pipeline, every enabled re-rendered pipeline when Stage 3 refused the
// merged config. handEdited marks the hand-edit refusal (errHandEdited),
// whose remedies differ from a render or validation failure's.
type rerenderFailure struct {
	pipelines []string
	// ids are the pipelines' ids, index-aligned with pipelines — carried
	// in the refusal's pipelinesDetail so the UI can link each one.
	ids        []string
	reason     string
	handEdited bool
}

// failureFor is a failure of the one pipeline p.
func failureFor(p sqlc.Pipeline, reason string) *rerenderFailure {
	return &rerenderFailure{pipelines: []string{p.Name}, ids: []string{p.ID.String()}, reason: reason}
}

// names is the failure's pipelines, each quoted (%q).
func (f rerenderFailure) names() string {
	q := make([]string, len(f.pipelines))
	for i, n := range f.pipelines {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}

// The remedies a refusal names (M4, 2026-10-08 walkthrough). Only actions
// the product offers: the pipeline page's revision history (Restore), its
// "Detach from wizard", and Delete. Running a wizard again creates a NEW
// pipeline, so "re-run its wizard" is never one of them.
const (
	// remedyHandEdited: restoring the last wizard-generated revision puts
	// back the text the wizard wrote, which a re-render may replace.
	remedyHandEdited = "on the pipeline's page, restore its last wizard-generated revision, detach it from the wizard, or delete it"
	// remedyDestinationUpdate: the destination change itself is what the
	// wizard could not take, so changing it is the first way out.
	remedyDestinationUpdate = "change the destination so it can be regenerated, or, on the pipeline's page, detach it from the wizard or delete it"
	// remedyLegacyRerender: `shepherd admin rerender-destinations`.
	remedyLegacyRerender = "fix what the error names, or, on the pipeline's page in Shepherd, detach it from the wizard or delete it; then run this command again"
)

// describe is the failure as one clause of a refusal, ending in what to do
// about it: remedy for a render or validation failure, remedyHandEdited
// for a hand edit.
func (f rerenderFailure) describe(remedy string) string {
	if f.handEdited {
		remedy = remedyHandEdited
	}
	return fmt.Sprintf("%s: %s — %s", f.names(), f.reason, remedy)
}

// pipelineRefs is the refusal's pipelines (deduplicated), for
// pipelinesDetail.
func (f rerenderFailures) pipelineRefs() []pipelineRef {
	seen := map[string]bool{}
	var refs []pipelineRef
	for _, x := range f {
		for i, name := range x.pipelines {
			if i < len(x.ids) && !seen[x.ids[i]] {
				seen[x.ids[i]] = true
				refs = append(refs, pipelineRef{id: x.ids[i], name: name})
			}
		}
	}
	return refs
}

// rerenderFailures is the error UpdateDestination refuses with: every
// pipeline that would not survive the change, why, and what to do.
type rerenderFailures []rerenderFailure

func (f rerenderFailures) Error() string {
	parts := make([]string, len(f))
	for i, x := range f {
		parts[i] = x.describe(remedyDestinationUpdate)
	}
	return fmt.Sprintf("%d wizard pipeline(s) using it cannot be regenerated: %s", len(f), strings.Join(parts, "; "))
}

// legacyDestinationWriterMarker is the writer every wizard emitted before
// #260: a URL read from an environment variable nothing set, and no auth.
const legacyDestinationWriterMarker = `sys.env("SHEPHERD_DEST_`

// isDestNameKey reports whether a wizard_state key names a destination —
// the `<signal>_dest_name` fields every wizard uses (metrics_dest_name,
// logs_dest_name). Mirrors ListWizardPipelinesReferencingDestination's LIKE.
func isDestNameKey(k string) bool { return strings.HasSuffix(k, "_dest_name") }

// renderWizardPipeline re-runs p's wizard from its stored state against
// dests, renaming the destination first if rename is set. It returns
// changed=false when the result is byte-identical to what is stored, so an
// edit that does not touch the rendered writer (a tenant_id, say) writes no
// revision. A non-nil failure means the re-render was refused: the wizard
// could not render it, or the result fails Stages 1-2.
func (s *PipelineService) renderWizardPipeline(ctx context.Context, p sqlc.Pipeline, dests wizard.Destinations, rename *destinationRename) (out wizardRerender, changed bool, failure *rerenderFailure) {
	fail := func(format string, args ...any) (wizardRerender, bool, *rerenderFailure) {
		return wizardRerender{}, false, failureFor(p, fmt.Sprintf(format, args...))
	}
	wiz, err := wizard.Default().Get(p.WizardKind.String)
	if err != nil {
		return fail("%v", err)
	}
	state := map[string]any{}
	if err := json.Unmarshal(p.WizardState, &state); err != nil {
		return fail("stored wizard state is not a JSON object: %v", err)
	}
	stateChanged := false
	if rename != nil {
		for k, v := range state {
			if name, ok := v.(string); ok && isDestNameKey(k) && name == rename.from {
				state[k] = rename.to
				stateChanged = true
			}
		}
	}
	result, err := wiz.Commit(state, dests)
	if err != nil {
		return fail("%v", err)
	}
	if result.Contents == p.Contents && !stateChanged {
		return wizardRerender{}, false, nil
	}
	// The same Stage 1/2 gate UpdatePipeline (validateSaveInput) and
	// CommitWizard run before they write.
	if r := s.validator.ValidatePipeline(ctx, p.Name, result.Contents); !r.Valid {
		return fail("%s", summarizeDiagnostics(r.Diagnostics))
	}
	row := p
	row.Contents = result.Contents
	if stateChanged {
		stateJSON, err := wizard.MarshalState(state)
		if err != nil {
			return fail("%v", err)
		}
		row.WizardState = stateJSON
	}
	return wizardRerender{row: row, stateChanged: stateChanged}, true, nil
}

// summarizeDiagnostics renders Stage 1/2 diagnostics as one line for an
// error message (the pipeline's own editor shows them in full).
func summarizeDiagnostics(diags []validate.Diagnostic) string {
	if len(diags) == 0 {
		return "failed validation"
	}
	parts := make([]string, 0, len(diags))
	for _, d := range diags {
		parts = append(parts, fmt.Sprintf("stage %d, line %d: %s", d.Stage, d.Line, d.Message))
	}
	return strings.Join(parts, "; ")
}

// renderFingerprint is the wizard render fingerprint of contents
// (pipelines.wizard_render_sha256, 0030): hex sha256 of the exact bytes.
// Every wizard write stores it — CommitWizard, applyWizardRerenders (a
// destination update and `shepherd admin rerender-destinations`).
func renderFingerprint(contents string) pgtype.Text {
	sum := sha256.Sum256([]byte(contents))
	return pgtype.Text{String: hex.EncodeToString(sum[:]), Valid: true}
}

// fingerprintAfterEdit is the fingerprint a NON-wizard write of contents to
// p stores: p's own, kept only while contents still hash to it (a
// matchers-only or rename save is not a hand edit of the text), NULL as soon
// as the text changes. UpdatePipeline (the editor) and RestoreRevision use
// it; gitsync writes only git pipelines, which never have one.
func fingerprintAfterEdit(p sqlc.Pipeline, contents string) pgtype.Text {
	if p.WizardRenderSha256.Valid && p.WizardRenderSha256 == renderFingerprint(contents) {
		return p.WizardRenderSha256
	}
	return pgtype.Text{}
}

// fingerprintAfterRestore is the fingerprint RestoreRevision stores when it
// restores rv to p: rv's own when a wizard wrote rv (0031; it hashes rv's
// contents by construction, checked anyway), else fingerprintAfterEdit.
func fingerprintAfterRestore(p sqlc.Pipeline, rv sqlc.PipelineRevision) pgtype.Text {
	if p.WizardKind.Valid && rv.WizardRenderSha256.Valid && rv.WizardRenderSha256 == renderFingerprint(rv.Contents) {
		return rv.WizardRenderSha256
	}
	return fingerprintAfterEdit(p, rv.Contents)
}

// isWizardChangeNote reports a revision note only wizard writes use:
// CommitWizard's "created" and applyWizardRerenders' "re-rendered: …". The
// UI marks revisions by these notes (web/src/lib/wizardRevisions.ts), so
// RestoreRevision refuses them as a caller's note.
func isWizardChangeNote(note string) bool {
	n := strings.TrimSpace(note)
	return n == "created" || strings.HasPrefix(n, "re-rendered:")
}

// errHandEdited is the reason a hand-edited wizard pipeline is refused;
// describe adds remedyHandEdited. "Detach from wizard" keeps the edit and
// stops the pipeline following destination changes; restoring the last
// wizard-generated revision drops the edit and lets the change through.
const errHandEdited = "its contents were edited by hand after its wizard generated them, and " +
	"regenerating it would discard that edit"

// handEdited reports a wizard pipeline whose stored contents are not what
// its wizard renders from its stored state against before — the org's
// destinations as they stood when the stored text was last generated
// (maintainer decision 2026-10-01: refuse and name it, never overwrite a
// hand edit). The comparison is exact bytes: a wizard's render is a pure
// function of (state, destinations), CommitWizard stores it verbatim and no
// write path normalises stored contents, so there is nothing to normalise.
// A pipeline still carrying the pre-#260 `sys.env(...)` writer is exempt:
// its text came from the old renderer, and converting it is the point. A
// render error against before (its wizard, or another destination it names,
// already broken) is reported as such.
//
// The render fingerprint (0030) decides first: a pipeline with one was
// hand-edited exactly when its contents no longer hash to it — no re-render,
// so a later change to the wizard's template does not make every older
// pipeline look edited. Only a pipeline with no fingerprint (written before
// 0030, or since cleared by an editor write) falls back to the fresh-render
// comparison below.
func handEdited(p sqlc.Pipeline, before wizard.Destinations) *rerenderFailure {
	if strings.Contains(p.Contents, legacyDestinationWriterMarker) {
		return nil
	}
	if p.WizardRenderSha256.Valid {
		if p.WizardRenderSha256 == renderFingerprint(p.Contents) {
			return nil
		}
		f := failureFor(p, errHandEdited)
		f.handEdited = true
		return f
	}
	wiz, err := wizard.Default().Get(p.WizardKind.String)
	if err != nil {
		return failureFor(p, err.Error())
	}
	state := map[string]any{}
	if err := json.Unmarshal(p.WizardState, &state); err != nil {
		return failureFor(p, fmt.Sprintf("stored wizard state is not a JSON object: %v", err))
	}
	result, err := wiz.Commit(state, before)
	if err != nil {
		return failureFor(p, fmt.Sprintf("it no longer renders from its wizard state: %v", err))
	}
	if result.Contents != p.Contents {
		f := failureFor(p, errHandEdited)
		f.handEdited = true
		return f
	}
	return nil
}

// planWizardRerenders re-renders pipelines (all in orgID) and runs Stage 3
// over the merged config with every enabled one swapped in together. It
// returns the pipelines whose stored form changes and every pipeline that
// was refused; a Stage 3 refusal is reported against all the enabled
// re-rendered pipelines, since the merged config is what failed.
//
// before, when non-nil, is the org's destinations as they stood before the
// change: every pipeline is first checked for a hand edit against it
// (handEdited) and refused if it has one. The legacy-writer CLI passes nil —
// it only touches pre-#260 pipelines, which are exempt anyway.
func (s *PipelineService) planWizardRerenders(ctx context.Context, orgID pgtype.UUID, pipelines []sqlc.Pipeline, before, dests wizard.Destinations, rename *destinationRename) ([]wizardRerender, rerenderFailures) {
	var (
		changes  []wizardRerender
		failures rerenderFailures
	)
	for i := range pipelines {
		if before != nil {
			if failure := handEdited(pipelines[i], before); failure != nil {
				failures = append(failures, *failure)
				continue
			}
		}
		out, changed, failure := s.renderWizardPipeline(ctx, pipelines[i], dests, rename)
		switch {
		case failure != nil:
			failures = append(failures, *failure)
		case changed:
			changes = append(changes, out)
		}
	}
	var enabled []sqlc.Pipeline
	var enabledNames, enabledIDs []string
	for i := range changes {
		if changes[i].row.Enabled {
			enabled = append(enabled, changes[i].row)
			enabledNames = append(enabledNames, changes[i].row.Name)
			enabledIDs = append(enabledIDs, changes[i].row.ID.String())
		}
	}
	if len(enabled) > 0 {
		if err := s.stage3CheckSet(ctx, orgID, enabled, ""); err != nil {
			failures = append(failures, rerenderFailure{pipelines: enabledNames, ids: enabledIDs, reason: err.Error()})
			changes = nil
		}
	}
	return changes, failures
}

// rerenderAudit is the detail of a pipeline.rerender audit row.
type rerenderAudit struct {
	Reason          string `json:"reason"`
	DestinationID   string `json:"destination_id,omitempty"`
	DestinationName string `json:"destination_name,omitempty"`
	RenamedFrom     string `json:"renamed_from,omitempty"`
	Revision        int32  `json:"revision"`
}

// applyWizardRerenders writes changes through q (the caller's transaction):
// the row, a revision, a pipeline.rerender audit row, and — when any of them
// is enabled — the org's serve cache marked dirty. It reports whether the
// cache was marked, so the caller can kick the eager recompute after commit.
func applyWizardRerenders(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, changes []wizardRerender, actor, actorType, note string, detail rerenderAudit) (dirtied bool, err error) {
	anyEnabled := false
	for i := range changes {
		c := changes[i].row
		var state []byte
		if changes[i].stateChanged {
			state = c.WizardState
		}
		updated, err := q.UpdatePipeline(ctx, sqlc.UpdatePipelineParams{
			ID: c.ID, Name: c.Name, Contents: c.Contents, Matchers: c.Matchers,
			WizardState: state, UpdatedBy: actor,
			// A wizard write: record what it rendered (0030).
			WizardRenderSha256: renderFingerprint(c.Contents),
		})
		if err != nil {
			return false, fmt.Errorf("updating pipeline %q: %w", c.Name, err)
		}
		rev, err := createPipelineRevisionQ(ctx, q, updated, note, actor)
		if err != nil {
			return false, fmt.Errorf("recording a revision of pipeline %q: %w", c.Name, err)
		}
		d := detail
		d.Revision = rev
		if err := auditInsert(ctx, q, actor, actorType, orgID, "pipeline.rerender", "pipeline", c.ID.String(), d); err != nil {
			return false, fmt.Errorf("auditing the re-render of pipeline %q: %w", c.Name, err)
		}
		anyEnabled = anyEnabled || updated.Enabled
	}
	if anyEnabled {
		if err := q.MarkServeCacheDirtyByOrg(ctx, orgID); err != nil {
			return false, fmt.Errorf("marking the serve cache dirty: %w", err)
		}
	}
	return anyEnabled, nil
}

// LegacyRerenderResult is one org's outcome of
// RerenderLegacyDestinationWriters.
type LegacyRerenderResult struct {
	OrgID, OrgName string
	// Rerendered names the pipelines rewritten (or, on a dry run, that
	// would be).
	Rerendered []string
	// Failed is one `"pipeline": reason — what to do` line per pipeline left as it was.
	// A Stage 3 refusal lists every enabled pipeline of the org and leaves
	// the whole org untouched.
	Failed []string
}

// LegacyRerenderActor is the audit actor of the one-time re-render.
const LegacyRerenderActor = "system:rerender-destinations"

// RerenderLegacyDestinationWriters is the one-time upgrade path for wizard
// pipelines committed before #260, whose writers still read their URL from
// `sys.env("SHEPHERD_DEST_<NAME>_URL")` — a variable nothing sets — and carry
// no auth. For every org it re-renders each such pipeline from its stored
// wizard state against the org's destinations, through the same gate as a
// destination update (planWizardRerenders), and writes the ones that pass in
// one transaction per org (applyWizardRerenders: revision, pipeline.rerender
// audit row as LegacyRerenderActor, serve cache marked dirty). A pipeline the
// wizard cannot render (its destination was deleted, say) or that fails
// Stages 1-2 is reported and left as it was; a Stage 3 refusal leaves the
// whole org as it was. Idempotent: a re-rendered pipeline no longer carries
// the legacy writer, so a second run finds nothing.
//
// It writes nothing when dryRun is set. Run by `shepherd admin
// rerender-destinations`; collectors pick the new config up on their next
// poll (the agent API recomputes a dirty serve cache lazily).
func RerenderLegacyDestinationWriters(ctx context.Context, st *store.Store, v *validate.Validator, reg *schema.Registry, logger *slog.Logger, dryRun bool) ([]LegacyRerenderResult, error) {
	ps := NewPipelineService(st, v, reg, logger)
	orgs, err := st.Queries.ListOrgs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing orgs: %w", err)
	}
	var results []LegacyRerenderResult
	for i := range orgs {
		res, err := ps.rerenderLegacyOrg(ctx, orgs[i], dryRun)
		if err != nil {
			return results, fmt.Errorf("org %q: %w", orgs[i].Name, err)
		}
		if len(res.Rerendered) > 0 || len(res.Failed) > 0 {
			results = append(results, res)
		}
	}
	return results, nil
}

func (s *PipelineService) rerenderLegacyOrg(ctx context.Context, org sqlc.Org, dryRun bool) (LegacyRerenderResult, error) {
	res := LegacyRerenderResult{OrgID: org.ID.String(), OrgName: org.Name}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed
	txQ := s.store.Queries.WithTx(tx)

	all, err := txQ.ListWizardPipelinesByOrgForUpdate(ctx, org.ID)
	if err != nil {
		return res, fmt.Errorf("listing wizard pipelines: %w", err)
	}
	var legacy []sqlc.Pipeline
	for i := range all {
		if strings.Contains(all[i].Contents, legacyDestinationWriterMarker) {
			legacy = append(legacy, all[i])
		}
	}
	if len(legacy) == 0 {
		return res, nil
	}
	dests, err := wizardDestinations(ctx, txQ, org.ID)
	if err != nil {
		for i := range legacy {
			res.Failed = append(res.Failed, failureFor(legacy[i], err.Error()).describe(remedyLegacyRerender))
		}
		return res, nil
	}
	changes, failures := s.planWizardRerenders(ctx, org.ID, legacy, nil, dests, nil)
	for _, f := range failures {
		res.Failed = append(res.Failed, f.describe(remedyLegacyRerender))
	}
	for i := range changes {
		res.Rerendered = append(res.Rerendered, changes[i].row.Name)
	}
	if dryRun || len(changes) == 0 {
		return res, nil
	}
	detail := rerenderAudit{Reason: "legacy destination writer (pre-#260 sys.env URL, no auth)"}
	if _, err := applyWizardRerenders(ctx, txQ, org.ID, changes, LegacyRerenderActor, "system", "re-rendered: pre-#260 destination writer", detail); err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("committing: %w", err)
	}
	return res, nil
}
