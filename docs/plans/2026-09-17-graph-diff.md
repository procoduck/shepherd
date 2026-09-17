# Graph diff for visual pipelines (#118)

_2026-09-17. Ships in the next release; move to `docs/archive/plans/` then._

## Problem

Revision diff is text-only (`RevisionDiff` + `PipelineEditorPage`, a CodeMirror
merge of rendered Alloy). The visual builder has no revision UI at all, so a
visual pipeline's graph-level change — a node added, a wire re-pointed, a prop
edited — can only be read as a diff of the generated config text, which is
noisy and doesn't map back onto the canvas.

## Approach (decided 2026-09-17: server-side)

A new `VisualService.DiffRevisions` RPC computes a **structural** graph diff in
Go and returns it as data the builder renders. Both a revision's graph and the
current pipeline's graph are already stored as `wizard_state`
(`pipeline_revisions.wizard_state` / `pipelines.wizard_state`, the internal
`visual.GraphDocument` JSON — migration 0019), so no new data is persisted.

### Backend

- `internal/visual/diff.go` — a pure `DiffGraphs(from, to GraphDocument)
  GraphDiff`. Nodes matched by id (added / removed / changed, with per-field and
  per-prop `FieldChange`s); edges by id (added / removed / changed on
  endpoints-or-order); bindings by `(node, prop)` (added / removed / changed on
  ref). Output is deterministically ordered. Table-tested in `diff_test.go`
  (Ginkgo, matching the package).
- `VisualService.DiffRevisions` (`rpc_visual.go`) — loads the pipeline
  (org-scoped, NotFound on mismatch like GraphView), resolves each side's graph
  from `wizard_state`, falling back to a schema-aware `ParseAlloy` of that
  side's contents (reusing GraphView's schema handling, extracted to
  `contentsToGraph`). `to_revision == 0` diffs against the live pipeline. A side
  with no readable graph is treated as empty and flagged `*_opaque` + `warning`.
- Authz: `VisualServiceDiffRevisionsProcedure → RoleOrgReader` in
  `rpc_interceptor.go` (a diff reveals no more than GraphView + GetRevision,
  both reader).

### Frontend

- `diffRevisions()` wrapper in `api/client.ts` over the generated client.
- A `RevisionCompare` modal opened from a Toolbar **History** button (shown only
  for a saved pipeline). Lists revisions (`listRevisions`), and on selection
  renders the diff grouped into Nodes / Wires / Bindings with add/remove/change
  affordances and per-field before→after. Read-only; restore stays the text
  editor's job for now.
- Mock handler + a `visual-revision-diff` spec.

## Out of scope

- Restoring a revision from the visual builder (text editor already does it).
- Diffing two arbitrary revisions in the UI (the RPC supports it; the first UI
  only offers "revision N vs current").
