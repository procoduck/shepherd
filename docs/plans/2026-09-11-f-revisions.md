# F-REVISIONS — implementation plan (2026-09-11)

Closes the `docs/project-status.md` §3 item **F-REVISIONS** and the §4 scheduled item
"F-REVISIONS: `contents` on `PipelineRevision`, `RestoreRevision` RPC, then the text diff view
and Restore in the pipeline editor". Graph diff for visual pipelines stays a follow-up.

Integration branch: `feat/revisions` (cut from `main` at `d101f58`). Four work packages:
the **foundation** lands first, alone, directly on `feat/revisions`; **backend**, **web** and
**docs-tests** are then built in parallel on branches cut from `feat/revisions` and merged back.
The orchestrator pushes; nobody in a package runs `git push`, and nobody touches `main`.

## 0. Settled decisions (do not re-open)

| # | Decision |
|---|---|
| S1 | `PipelineRevision` gains `contents`, `matchers`, `enabled`, `wizard_state`. `ListRevisions` **stays metadata-only** — so does `Pipeline.revisions` on `GetPipeline`. Only the new `GetRevision` returns the heavy fields. |
| S2 | New `rpc GetRevision(GetRevisionRequest) returns (PipelineRevision)` — org reader. New `rpc RestoreRevision(RestoreRevisionRequest) returns (Pipeline)` — same authorization as `UpdatePipeline`: interceptor row identical to `UpdatePipeline`'s (`auth.RoleOrgReader`, the fine-grained decision is `authorizeOwnership` inside the handler, which admits org admin/editor and refuses a reader), `requireWriteAuthorized` first for machine callers, `capabilityApply` in `capabilityRequirements`. |
| S3 | Restore semantics: `RestoreRevision(org_id, id, revision, change_note?)` creates a **new** revision N+1 from the old row's `contents`+`matchers`+`enabled`(+`wizard_state`); runs the same gate as `UpdatePipeline` (`validateSaveInput` = name/matchers/Stage 1+2, plus `stage3Check` when the pipeline is enabled); writes the `pipeline_revisions` row with `change_note` `"Restored from revision N"` unless given; writes an audit row `pipeline.restore`; dirties + recomputes the serve cache exactly like an update. It never mutates the old revision. |
| S4 | Visual pipelines: migration adds `wizard_state jsonb NULL` to `pipeline_revisions`; `createRevision` stores it; restore of a visual pipeline restores graph and text together (the `pipelines.wizard_state` column is overwritten with the revision's, or left untouched when the revision has none — same COALESCE rule `UpdatePipeline` uses). |
| S5 | Git-sourced pipelines (`source=git`): Restore is **allowed** — it does **not** apply `errGitSourceReadOnly`; it writes a new revision like any other, and the UI warns that the next git sync will overwrite it. `UpdatePipeline` stays read-only for git. |
| S6 | REST shim routes: `GET /api/orgs/{org}/pipelines/{id}/revisions/{rev}` [org reader group] and `POST /api/orgs/{org}/pipelines/{id}/revisions/{rev}/restore` [org editor group]. Response bodies are `writeProtoJSON` (snake_case, `UseProtoNames`); a gate failure on restore goes through `writePipelineSaveError` so the legacy `{"error":…,"diagnostics":[…]}` envelope is preserved. |
| S7 | `@codemirror/merge` 6.12.x is an approved npm dependency. It must be imported only from modules that live inside the `AlloyEditor-*.js` lazy chunk (the `web/src/editor/LazyAlloyEditor.tsx` boundary), never from the entry chunk. |
| S8 | Graph diff for visual pipelines is out of scope. The text diff is what ships; restoring a visual pipeline from the text editor page works because the server restores `wizard_state` with it. |

## 1. Repository facts the packages depend on

- Newest migration is `0018_service_account_role`; this plan adds **`0019_pipeline_revision_wizard_state`**. `store.MigrateDown` is `Steps(-1)`, and `internal/store/service_account_role_migration_test.go` ("drops the role column on the down migration") assumes 0018 is head — it will go red the moment 0019 exists. Foundation fixes the mechanism, not the assertion (see F-6).
- `CreatePipelineRevision` has three callers whose `sqlc.CreatePipelineRevisionParams` literal changes shape when the query gains `wizard_state`: `internal/mgmtapi/rpc_pipeline.go:createRevision`, `internal/gitsync/reconciler.go:466`, `internal/cli/dev.go:464`.
- `internal/mgmtapi/capability_enumeration_test.go` and `internal/mcp/registry_test.go` classify a procedure as a write by **method-name verb prefix** (`Create/Update/Delete/Enable/Disable/Revoke/Claim/Unclaim/Rotate/Set/Commit`). `RestoreRevision` starts with `Restore`, which is not in either list — both lists must gain `"Restore"` or the G12 enumeration test silently stops covering the new write.
- `internal/mgmtapi/capability_enumeration_test.go:TestEveryProcedureHasAnAuthzRequirement` reads the proto descriptors: the instant `make generate` runs with the two new RPCs, it fails until both have `procedureRequirements` rows. This is foundation's compile-green gate.
- `web/src/routeCoverage.test.ts` parses the **first fenced block after `## 12. Management REST API`** in `docs/spec.md` and requires a mock handler for every line. The existing `/revisions` route is deliberately documented in **§D.3**, not inside the §12 fence. docs-tests must keep the two new routes in §D.3 (and §13.5) and **not** add them to the §12 fence — that is what keeps docs-tests and web disjoint.
- REST shim JSON is `UseProtoNames` (snake_case: `change_note`, `wizard_state`); Connect JSON is camelCase.
- The pipeline editor page (`web/src/pages/PipelineEditorPage.tsx`) seeds its form once per pipeline id (`seededFor` ref) — a restore must reset that ref (or set state from the returned `Pipeline`) or the editor keeps showing pre-restore text after the query refetches.
- The mocked Playwright suite fails a test on any unmatched request and on any `console.error`; every new RPC the UI calls needs a default handler in `web/tests/mocks/handlers.ts`.
- `internal/spa/dist` is rebuilt by every Playwright run; discard it before committing: `git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets`.

## 2. Package 1 — foundation (directly on `feat/revisions`, built first, alone)

Goal: schema, queries, proto, generated code, authz table rows, and `Unimplemented` stubs — with **every existing test still green**.

### Steps

- F-1 Migration `internal/migrations/sql/0019_pipeline_revision_wizard_state.up.sql`:
  `ALTER TABLE pipeline_revisions ADD COLUMN wizard_state jsonb;` (nullable, no default — a NULL means "this revision predates 0019 or the pipeline is not visual/wizard"). Header comment in the 0018 style: why nullable, what reads it (`GetRevision`/`RestoreRevision`), that `createRevision` copies `pipelines.wizard_state` verbatim at write time. Down file: `ALTER TABLE pipeline_revisions DROP COLUMN wizard_state;`.
- F-2 `internal/store/queries/pipeline_revisions.sql`:
  - `CreatePipelineRevision` inserts `wizard_state` as `$8` (`json.RawMessage`; nil encodes as NULL under pgx).
  - New `-- name: GetPipelineRevision :one` — `SELECT * FROM pipeline_revisions WHERE pipeline_id = $1 AND revision = $2;`.
- F-3 Proto `proto/shepherd/mgmt/v1/pipeline.proto`:
  - `PipelineRevision` gains `string contents = 5; repeated string matchers = 6; bool enabled = 7; google.protobuf.Struct wizard_state = 8;` with a comment that 5–8 are populated **only** by `GetRevision` (S1).
  - `message GetRevisionRequest { string org_id = 1; string id = 2; int32 revision = 3; }`
  - `message RestoreRevisionRequest { string org_id = 1; string id = 2; int32 revision = 3; string change_note = 4; }`
  - `rpc GetRevision(GetRevisionRequest) returns (PipelineRevision) {}` and `rpc RestoreRevision(RestoreRevisionRequest) returns (Pipeline) {}` with doc comments stating S2/S3/S5 in one line each.
- F-4 `cd web && pnpm install --frozen-lockfile` (protoc-gen-es), then `make generate` from the repo root. Commit the regenerated `gen/shepherd/mgmt/v1/pipeline.pb.go`, `gen/shepherd/mgmt/v1/mgmtv1connect/pipeline.connect.go`, `internal/store/sqlc/models.go`, `internal/store/sqlc/pipeline_revisions.sql.go`, `web/src/gen/shepherd/mgmt/v1/pipeline_pb.ts`. Never hand-edit these.
- F-5 Fix the three `CreatePipelineRevisionParams` call sites: `createRevision` passes `WizardState: p.WizardState` (the pipeline row's current graph — this is the "createRevision stores it" half of S4); `gitsync/reconciler.go` and `cli/dev.go` pass nothing extra (nil → NULL) with a one-line comment each.
- F-6 `internal/store`: add `MigrateTo(ctx, url string, version uint) error` (wraps `m.Migrate(version)`) beside `MigrateDown` in `internal/store/migrations.go`; change the 0018 spec's down case to `store.MigrateTo(ctx, url, 17)` with a comment that 0018 is no longer head. The assertion (`role` column gone) is unchanged — the mechanism is fixed, the control is not weakened. Add `internal/store/pipeline_revision_wizard_state_migration_test.go` (Ginkgo, `Label("integration")`, same harness as the 0018 spec): column exists and is nullable after `MigrateUp`; an insert omitting it reads back NULL; `MigrateTo(ctx, url, 18)` drops it.
- F-7 `internal/mgmtapi/rpc_interceptor.go`: `PipelineServiceGetRevisionProcedure: auth.RoleOrgReader`, `PipelineServiceRestoreRevisionProcedure: auth.RoleOrgReader` (with a comment pointing at the `UpdatePipeline` row and `authorizeOwnership`), and `PipelineServiceRestoreRevisionProcedure: capabilityApply` in `capabilityRequirements`.
- F-8 Add `"Restore"` to `writeVerbPrefixes` in **both** `internal/mgmtapi/capability_enumeration_test.go` and `internal/mcp/registry_test.go` (the second file's comment says it mirrors the first exactly — keep them identical).
- F-9 Stubs in `internal/mgmtapi/rpc_pipeline.go`: `GetRevision` and `RestoreRevision` return `connect.NewError(connect.CodeUnimplemented, errors.New("… lands with the F-REVISIONS backend package"))`. Nothing else in the service changes. `revisionToProto` stays metadata-only.
- F-10 `internal/mcp/views.go`: no change required (the MCP view mirrors the metadata subset and `get_pipeline` still receives metadata-only revisions). Leave it to backend.
- F-11 Run the verify list, discard `internal/spa/dist` if anything rebuilt it, commit as `feat(revisions): foundation — wizard_state on revisions, GetRevision/RestoreRevision proto, stubs` with the trailers.

### Files

`internal/migrations/sql/0019_pipeline_revision_wizard_state.{up,down}.sql`, `internal/store/queries/pipeline_revisions.sql`, `internal/store/sqlc/*` (generated), `internal/store/migrations.go`, `internal/store/service_account_role_migration_test.go`, `internal/store/pipeline_revision_wizard_state_migration_test.go`, `proto/shepherd/mgmt/v1/pipeline.proto`, `gen/shepherd/mgmt/v1/pipeline.pb.go`, `gen/shepherd/mgmt/v1/mgmtv1connect/pipeline.connect.go`, `web/src/gen/shepherd/mgmt/v1/pipeline_pb.ts`, `internal/mgmtapi/rpc_pipeline.go`, `internal/mgmtapi/rpc_interceptor.go`, `internal/mgmtapi/capability_enumeration_test.go`, `internal/mcp/registry_test.go`, `internal/gitsync/reconciler.go`, `internal/cli/dev.go`.

### Tests and red runs

- `internal/store/pipeline_revision_wizard_state_migration_test.go` — red run: temporarily rename the 0019 up file (or comment out the `ADD COLUMN`) → "column wizard_state must exist on pipeline_revisions" fails; restore.
- The existing `TestEveryProcedureHasAnAuthzRequirement` is the red run for F-7: run it once **before** adding the two rows and capture `procedure /shepherd.mgmt.v1.PipelineService/GetRevision has no entry in procedureRequirements …`; then add the rows.
- `TestEveryMutatingProcedureIsCapabilityClassified` after F-8 is the red run for the `capabilityRequirements` row: temporarily delete the `RestoreRevision: capabilityApply` line → `procedure …/RestoreRevision looks like a write (method name "RestoreRevision") but has no entry in capabilityRequirements`; restore.

### Verify (all must pass)

```
cd web && pnpm install --frozen-lockfile && cd ..
make generate && git status --porcelain gen internal/store/sqlc web/src/gen   # regenerate is idempotent after commit: empty
go build ./... && go vet ./...
make lint
go test ./internal/store/ -count=1
go test ./internal/mgmtapi/ -count=1
go test ./internal/mcp/ ./internal/gitsync/ ./internal/cli/ -count=1
cd web && pnpm typecheck && pnpm lint && pnpm exec vitest run
```

## 3. Package 2 — backend (branch `feat/revisions-backend`, owns `internal/**`)

### Steps

- B-1 `revisionToProtoFull(rv sqlc.PipelineRevision) *mgmtv1.PipelineRevision` in `rpc_pipeline.go`: metadata + `Contents`, `Matchers` (unmarshal, `[]string{}` on error, same as `pipelineToProto`), `Enabled`, `WizardState` (protojson-unmarshal into `structpb.Struct`, dropped on error, same best-effort rule as `pipelineToProto`). `revisionToProto` is untouched (S1).
- B-2 `GetRevision`: `loadPipeline` (org-scoping/NotFound rules), `revision <= 0` → `CodeInvalidArgument`, `GetPipelineRevision` → `pgx.ErrNoRows` → `CodeNotFound` (`errRevisionNotFound`), else `revisionToProtoFull`.
- B-3 `RestoreRevision`, in this order: `requireWriteAuthorized`; `loadPipeline`; `authorizeOwnership(orgID, pipelineOwnerTeamID(p))`; load the revision (NotFound as above); **no** `errGitSourceReadOnly` check (S5); build the candidate: `Name = p.Name`, `Contents = rev.Contents`, `Matchers` from `rev.Matchers`, `WizardState = rev.WizardState` (nil when NULL); `validateSaveInput(pipelineSaveInput{Name, Contents, Matchers, Source: p.Source, WizardState})` — the same gate as `UpdatePipeline`; when `p.Enabled`, `stage3Check(candidate, orgID, true)` → `CodeFailedPrecondition`; persist with `UpdatePipeline` query (`WizardState: rev.WizardState` — nil preserves the stored graph through the existing COALESCE, the S4 rule); if `rev.Enabled != p.Enabled`, run `stage3Check(candidate, orgID, rev.Enabled)` (the exact call `setEnabled` makes) and only then `SetPipelineEnabled` — so restoring an old `enabled=true` onto a disabled pipeline is an enable transition that is validated like `EnablePipeline`, and the "never serve unvalidated config" invariant holds; note in a comment that `enabled` is restored as state through the transition's own gate, never around it; `createRevision(ctx, updated, note, actor)` where `note = req.ChangeNote` or `fmt.Sprintf("Restored from revision %d", rev.Revision)`; `MarkServeCacheDirtyByOrg` + `go s.recomputeOrgCaches(...)` when the result is enabled (copy `UpdatePipeline`'s block, including its `//nolint` line — do not add a new nolint directive; the ≤20 budget is checked by `make lint`); `auditLog(ctx, s.store, actor, orgID, "pipeline.restore", "pipeline", p.ID.String())`; return `pipelineToProto(updated)`.
- B-4 REST shim: `pipelines.go` gains `GetRevision` (`GET …/revisions/{rev}`; `rev` parsed with `strconv.Atoi`, non-numeric → 400 `bad_request`) and `RestoreRevision` (`POST …/revisions/{rev}/restore`; optional JSON body `{"change_note": "..."}`, empty body allowed; errors through `writePipelineSaveError` so the gate's diagnostics envelope survives). `router.go`: the GET goes in the org-reader group next to `/pipelines/{id}/revisions`; the POST in the org-editor group next to `/pipelines/{id}/enable`.
- B-5 `internal/mcp/views.go`: `PipelineRevisionView` gains `Contents string \`json:"contents,omitempty"\``, `Matchers []string \`json:"matchers,omitempty"\``, `Enabled bool \`json:"enabled,omitempty"\``, `WizardState map[string]any \`json:"wizard_state,omitempty"\`` populated in `toPipelineView` from the proto getters — the view "mirrors `mgmtv1.PipelineRevision`" per its own comment, and `omitempty` keeps `get_pipeline` output byte-identical today. No new MCP tool (MCP is read + propose; `RestoreRevision` is a write and must **not** appear in `toolProcedures` — `TestNoToolReachesAMutatingProcedure` with the F-8 verb now enforces that).
- B-6 Ginkgo specs (new file `internal/mgmtapi/rpc_revisions_test.go`, `Label("integration")`, built on `newPipelineRPCRouter` + `newTestSession` with editor/reader groups the way `role_matrix_test.go` does; a REST case on `newRESTRouter`):
  1. **restore creates N+1 with the old contents** — create (rev 1), update (rev 2), restore 1 → response `contents` == rev-1 contents; `ListRevisions` has 3 items, newest `change_note == "Restored from revision 1"`, `revision == 3`; `GetRevision(1)` unchanged (the old row is never mutated) and its `contents` equals what was created.
  2. **restore goes through the validation gate** — seed a pipeline row, insert a `pipeline_revisions` row directly (via `st.Queries.CreatePipelineRevision`) whose `contents` fails Stage 1 (e.g. `prometheus.exporter.self "x" {` — unbalanced); `RestoreRevision` → HTTP 400/`failed_precondition` with the Connect error body carrying the gate's message; `ListRevisions` count unchanged and `GetPipeline().contents` unchanged.
  3. **a reader cannot restore** — reader-group session → `permission_denied`; editor-group session on the same pipeline → 200 (this is the S2 "same as UpdatePipeline" proof in one `DescribeTable`).
  4. **visual restore restores wizard_state** — seed `source="visual"` with graph A, `UpdatePipeline` with graph B + contents B (as `rpc_pipeline_test.go:194` does), restore 1 → `GetPipeline().wizardState` `MatchJSON(A)` and `contents` == A's contents; the new revision row's `wizard_state` `MatchJSON(A)`.
  5. **restore writes a `pipeline.restore` audit row** — `st.Queries.ListAuditLog` contains one row with `Action == "pipeline.restore"`, `ResourceID == pipeline id`, actor == the session's actor.
  6. **git-sourced restore is allowed** — seed `source="git"`; restore → 200; `UpdatePipeline` on the same pipeline still → `permission_denied` (`errGitSourceReadOnly` untouched).
  7. **GetRevision** — full fields present (`contents`, `matchers`, `enabled`, `wizardState`); `ListRevisions` items have **no** `contents` key (S1); unknown revision → `not_found`; another org's id → `not_found` (cross-org rule, mirror `cross_org_test.go`).
  8. **REST shim** — `GET /orgs/{org}/pipelines/{id}/revisions/1` returns snake_case with `contents`; `POST …/revisions/1/restore` as editor → 200 and revision count +1; as reader → 403; non-numeric `{rev}` → 400.
  9. **machine caller** — a propose-capability service account (`satierMakeServiceAccount`) is refused `permission_denied` on `RestoreRevision`; an apply account without `Shepherd-On-Behalf-Of` → `invalid_argument` (mirrors `service_account_tier_test.go`).
- B-7 Red runs to perform and report verbatim (revert each immediately): (a) delete the `validateSaveInput` call in `RestoreRevision` → spec 2 fails ("expected failed_precondition, got 200"); (b) replace `authorizeOwnership` with `nil` → spec 3's reader entry fails; (c) drop `WizardState: rev.WizardState` from the update params → spec 4 fails on `MatchJSON`; (d) delete the `auditLog` line → spec 5 fails; (e) hard-code `revisionToProto` to include `Contents` → spec 7's "no contents on ListRevisions" fails.
- B-8 Extend `role_matrix_test.go`'s reader `DescribeTable` with an `Entry` for `PipelineService/RestoreRevision` (reader refused). Extend `internal/mgmtapi/rest_roles_test.go` with the two REST cases if B-6.8 does not already live there.
- B-9 Run `make lint` (nolint budget, gofumpt, `exhaustive`), then `go test ./internal/mgmtapi/ ./internal/mcp/ -count=1`. Discard `internal/spa/dist` if touched. Commit `feat(revisions): GetRevision and RestoreRevision with REST shim, MCP view, specs` with the trailers.

### Files

`internal/mgmtapi/rpc_pipeline.go`, `internal/mgmtapi/pipelines.go`, `internal/mgmtapi/router.go`, `internal/mgmtapi/rpc_revisions_test.go` (new), `internal/mgmtapi/role_matrix_test.go`, `internal/mgmtapi/rest_roles_test.go`, `internal/mcp/views.go`. Backend must **not** touch `proto/`, `gen/`, `internal/store/**`, `web/**`, `docs/**`, `CHANGELOG.md`.

### Verify

```
make lint
go test ./internal/mgmtapi/ -count=1
go test ./internal/mcp/ -count=1
go test ./internal/store/ -count=1
```

## 4. Package 3 — web (branch `feat/revisions-web`, owns `web/**` except `web/src/gen/**` and `web/tests/fullstack/**`)

### Steps

- W-1 `cd web && pnpm add @codemirror/merge@^6.12.2` (approved dependency, S7). Commit `package.json` + `pnpm-lock.yaml` only (`scripts/repocheck` refuses any other lockfile).
- W-2 `web/src/editor/RevisionDiff.tsx` (new): `MergeView` from `@codemirror/merge` with `a` = old revision contents (left, "Revision #N") and `b` = current editor contents (right, "Current"); both panes `EditorView.editable.of(false)`, `alloyLanguage()`, `lineNumbers()`, the existing `alloyTheme` (export it from `AlloyEditor.tsx`), `highlightChanges`, `collapseUnchanged({ margin: 3 })`; prop `height`. Container `data-testid='revision-diff'`. **Re-export it from `AlloyEditor.tsx`** (`export { RevisionDiff } from './RevisionDiff'`) so it is part of the same module graph, and add to `LazyAlloyEditor.tsx`: `const RevisionDiffChunk = lazyNamed(() => import('./AlloyEditor'), 'RevisionDiff')` wrapped in the same `Suspense` fallback pattern (`data-testid='editor-loading'`). Nothing under `web/src/pages/**` may import `@codemirror/*` directly.
- W-3 `web/src/editor/revisionDiff.ts` (new, pure): `diffStats(oldText: string, newText: string): { added: number; removed: number }` — a line-set LCS-free count (lines only in new / only in old) used for the "+n −m" caption above the diff. Vitest `web/src/editor/revisionDiff.test.ts`: identical texts → 0/0; one inserted line → 1/0; a replaced line → 1/1; red run: return `{added:0,removed:0}` → the inserted-line case fails (`expected 1 to be 0`).
- W-4 `PipelineEditorPage.tsx`:
  - New state `selectedRevision: number | null`. Each revision row gets a **"View diff"** button (`data-testid='view-revision-btn'`) that sets it; the existing `restore-btn` keeps its test id but now opens the confirm dialog (stub toast removed).
  - `useQuery(['revision', orgId, id, selectedRevision], () => clients.pipeline.getRevision({ orgId, id, revision }))` when a revision is selected.
  - Right pane: when `selectedRevision != null` render a header ("Revision #N vs current", the `diffStats` caption, a **Back to editor** button, and — when `canWrite` — a **Restore this revision** button) and `<RevisionDiff …>` in place of `<AlloyEditor>`; otherwise the editor as today. Readers can view diffs (GetRevision is org-reader).
  - Confirm dialog on `Modal` from `@/components/ui/Modal` (`testId='restore-dialog'`): body "Restore revision #N? This creates a new revision from its contents and matchers; the current text is kept in history." When `pipeline.source === 'git'` add a second paragraph (`data-testid='restore-git-warning'`): "This pipeline is managed by Git. The restore is written as a new revision now, but the next git sync will overwrite it — change the file in the repository to make it stick." Confirm label "Restore".
  - `restoreMutation`: `clients.pipeline.restoreRevision({ orgId, id, revision })`; on success: `toast.success('Restored revision #N')`, `seededFor.current = null`, `setContents/ setName/ setMatchers` from the returned `Pipeline`, `qc.invalidateQueries(['pipeline', orgId, id])`, `qc.invalidateQueries(['revisions', orgId, id])`, `qc.invalidateQueries(['pipelines', orgId])`, close dialog, `setSelectedRevision(null)`; on error `toast.error(toApiError(e).message || 'Restore failed')`.
  - Restore visibility keys on `canWrite`, **not** on `readOnly` (git pipelines are read-only in the editor but restorable, S5).
- W-5 Mocks `web/tests/mocks/handlers.ts`: update `pipelineRevisionToWire` (drop the stale NOTE; include `contents`, `matchers`, `enabled`, `wizardState` when present on the fixture) and register `POST /shepherd.mgmt.v1.PipelineService/GetRevision` (find pipeline → find revision by number → 200 wire shape, 404-style Connect `not_found` otherwise) and `POST /shepherd.mgmt.v1.PipelineService/RestoreRevision` (`requireOrgRole(…, 'editor')`; copies the revision's `contents`/`matchers`/`enabled` onto the pipeline, unshifts a new revision `{revision: max+1, changed_by: me, change_note: 'Restored from revision N'}`, returns `pipelineToWire(p)`). Note in `web/src/routeCoverage.test.ts`'s `SPEC_TO_PROCEDURE` the two REST paths (`GET /api/orgs/{org}/pipelines/{id}/revisions/{rev}` → GetRevision, `POST …/revisions/{rev}/restore` → RestoreRevision) — harmless while they are outside the §12 fence, and correct the day someone moves them in.
- W-6 Mocked Playwright specs — extend `web/tests/specs/revisions.spec.ts` (delete its stale header comment, keep the existing list test) and add:
  1. **diff renders** — seed two revisions with different `contents`; click "View diff" on #1 → `getByTestId('revision-diff')` visible, `.cm-mergeView` present, the old-only line text visible in the left pane and the current-only line in the right; `api.calls('/shepherd.mgmt.v1.PipelineService/GetRevision')` has one call with `body.revision === 1`. Red run: point the query at `listRevisions` instead of `getRevision` → the calls assertion fails with `expected 1 to be 0`… (report the actual text).
  2. **restore calls the RPC and refreshes** — as `appAdmin`: View diff #1 → Restore → dialog visible → confirm → `api.calls('…/RestoreRevision')` has exactly one call with `{ id, revision: 1 }`; the editor `.cm-content` now contains revision 1's text; the toggle reads "Revision history (3)"; diff pane gone. Red run: remove `seededFor.current = null`/the `setContents` in `onSuccess` → the editor-content assertion fails.
  3. **git-sourced warning** — pipeline with `source: 'git'`: editor is still read-only (Save absent) but Restore is present; dialog shows `restore-git-warning`; a `ui`-sourced pipeline's dialog does not. Red run: hard-code the warning off → fails.
  4. **reader sees the diff but no Restore** — the `reader` persona from `personas.ts`: View diff works, `restore-btn` count is 0 and `RestoreRevision` is never called.
- W-7 Chunk proof: `cd web && pnpm build` and check the output: `grep -l "cm-mergeView\|@codemirror/merge" ../internal/spa/dist/assets/AlloyEditor-*.js` matches and `grep -L … ../internal/spa/dist/assets/index-*.js` shows the entry does not contain it; the build must not print the `chunkSizeWarningLimit` warning for the entry. Record the two chunk sizes in the commit message. Then discard the build: `git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets`.
- W-8 `pnpm check` (Biome write), then the verify list. Commit `feat(revisions): revision diff and restore in the pipeline editor` with the trailers.

### Files

`web/package.json`, `web/pnpm-lock.yaml`, `web/src/editor/RevisionDiff.tsx` (new), `web/src/editor/revisionDiff.ts` + `.test.ts` (new), `web/src/editor/AlloyEditor.tsx` (export theme + re-export), `web/src/editor/LazyAlloyEditor.tsx`, `web/src/pages/PipelineEditorPage.tsx`, `web/tests/mocks/handlers.ts`, `web/tests/fixtures/factories.ts` (only if `revision()` needs `wizard_state`), `web/tests/specs/revisions.spec.ts`, `web/src/routeCoverage.test.ts` (mapping rows only). Web must **not** touch `web/src/gen/**`, `web/tests/fullstack/**`, `internal/**`, `docs/**`.

### Verify

```
cd web && pnpm typecheck && pnpm lint && pnpm exec vitest run
cd web && pnpm exec playwright test tests/specs/revisions.spec.ts tests/specs/pipeline-editor.spec.ts tests/specs/editor-role.spec.ts
cd web && pnpm build 2>&1 | tee /tmp/vite-build.log && grep -q "cm-mergeView" ../internal/spa/dist/assets/AlloyEditor-*.js && ! grep -q "cm-mergeView" ../internal/spa/dist/assets/index-*.js && ! grep -q "chunks are larger than" /tmp/vite-build.log
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
make web-ci
```

## 5. Package 4 — docs-tests (branch `feat/revisions-docs`, owns `docs/**`, `CHANGELOG.md`, `scripts/docs-content/**`, `site/**`, `web/tests/fullstack/**`)

### Steps

- D-1 `CHANGELOG.md`: insert `## Unreleased` above `## v0.5.0` with a **Pipelines — Shipped** entry: revision contents on the API (`GetRevision`), `RestoreRevision` (new revision, same gate, `pipeline.restore` audit row, allowed for git-sourced pipelines with the sync-overwrite caveat), the editor's text diff and Restore, the new REST shim routes, and the `pipeline_revisions.wizard_state` migration (0019, additive, no operator action). One line that graph diff for visual pipelines is not built.
- D-2 `docs/project-status.md`: remove the §3 F-REVISIONS block (or replace it with a one-line "closed — see `CHANGELOG.md` Unreleased"); tick the §4 scheduled item and reword its tail so the remaining follow-up is explicit; add a "Smaller follow-ups" entry **Graph diff for visual pipelines** (text diff ships; the visual builder page has no revision UI; `RevisionDiff` is text-only); update the §1 "what works" list only if it names revisions; keep the document map unchanged.
- D-3 `docs/spec.md` §13.5 (line ~750, the "Revision Select" sentence): replace the "not buildable yet" clause with what is built — a per-revision "View diff" that opens a read-only CodeMirror merge view (old vs current) in the editor column, a "Restore this revision" confirm dialog (editor+), the git-sync warning, and that graph diff for visual pipelines is a follow-up. §D.3: append the two routes `GET /api/orgs/{org}/pipelines/{id}/revisions/{rev} [reader]` and `POST /api/orgs/{org}/pipelines/{id}/revisions/{rev}/restore [orgeditor]` with the restore semantics in one sentence, and drop "list only as of v0.5.0". **Do not add these lines to the §12 fenced block** — `web/src/routeCoverage.test.ts` parses that fence and web's mock table is not this package's to edit; §D.3 is where the existing `/revisions` route already lives.
- D-4 `scripts/docs-content/authoring.html`: after section 3 (before the "All three land in the same place" note) add `<h2 id="4-revisions-and-restore">4. Revisions and restore</h2>` with two short paragraphs: every save is a numbered revision; the editor shows any revision as a diff against the current text and can restore it — a restore is a new revision that goes through the same validation gate, never a rewrite of history; git-managed pipelines can be restored but the next sync wins. Then `make docs` and commit the regenerated `site/docs/authoring.html` (never hand-edit `site/docs/`).
- D-5 Fullstack spec `web/tests/fullstack/revisions.spec.ts` (new; `import { expect, loginAs, loginAsAdmin, DEV_VIEWER, test } from './fixtures'`; no `page.route` — `make check-no-route-mocks` forbids it):
  1. **REST round trip** — as admin: create pipeline (rev 1, contents A), PUT (contents B), `GET …/revisions/1` → `contents === A`; `POST …/revisions/1/restore` → 200 and `contents === A`; `GET …/revisions` → 3 items, `items[0].change_note === 'Restored from revision 1'`; `GET …/pipelines/{id}` → `contents === A`; `GET /api/orgs/{org}/audit` contains an `action === 'pipeline.restore'` row for the id.
  2. **UI restore** — open `/pipelines/{id}`, expand "Revision history (3)", click "View diff" on #2 (contents B) → `revision-diff` visible; click Restore → confirm → editor `.cm-content` contains B; history reads (4).
  3. **viewer refused** — `loginAs(page, DEV_VIEWER…)`: `POST …/revisions/1/restore` → 403.
  Cleanup deletes the pipeline. Run with `make test-fullstack` if Docker is available; otherwise commit it and report it as **written, unrun**, naming the command the orchestrator must run.
- D-6 Verify list, then commit `docs(revisions): changelog, ledger, spec, authoring guide, fullstack restore spec` with the trailers.

### Files

`CHANGELOG.md`, `docs/project-status.md`, `docs/spec.md`, `scripts/docs-content/authoring.html`, `site/docs/authoring.html` (generated by `make docs`; `site/docs/index.html` only if the generator rewrites it), `web/tests/fullstack/revisions.spec.ts` (new). docs-tests must **not** touch `web/src/**`, `web/tests/specs/**`, `web/tests/mocks/**`, `internal/**`, `proto/**`.

### Tests and red runs

- The fullstack spec's own red run (only when the stack runs): temporarily change the asserted `change_note` to `'Restored from revision 2'` → the round-trip case fails with the real note in the message; restore. If unrun, say so explicitly — do not report a red run that did not happen.
- `make check-docs-drift` is the control that `site/docs/` matches the generator; run `make docs` then confirm `git status --porcelain site/docs` is empty after commit.

### Verify

```
make docs && make check-docs-drift && make check-docs-version
cd web && pnpm exec biome check tests/fullstack/revisions.spec.ts && pnpm typecheck
make test-fullstack            # only if Docker is available; otherwise report "written, unrun"
```

## 6. Integration order and the final gate on `feat/revisions`

1. foundation → `feat/revisions` (verify §2).
2. backend, web, docs-tests in parallel from that commit; merge in any order — file sets are disjoint. The only shared seam is behavioural: web's mocks assume the Connect field names `contents/matchers/enabled/wizardState` on `PipelineRevision` and the request shapes `{orgId,id,revision}` / `{orgId,id,revision,changeNote?}` — both fixed by the foundation's proto, not by backend.
3. After all three merge, on `feat/revisions`:

```
make lint
make test
make web-ci
cd web && pnpm exec playwright test tests/specs/revisions.spec.ts
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
make test-fullstack   # if Docker is available
```

## 7. Risks

- **0018 head-migration spec goes red the moment 0019 exists** (`Steps(-1)`); foundation must ship `MigrateTo` and the mechanism fix in the same commit as the migration or the foundation gate cannot be green.
- **`Restore` is not a recognised write verb** in the two enumeration tests; without F-8 the G12 classification of `RestoreRevision` is unenforced and the MCP allow-list test would not notice a future tool reaching it.
- **`enabled` on restore**: restoring an old `enabled=true` onto a currently-disabled pipeline is an enable transition and must go through `stage3Check(…, true)` (the same path `EnablePipeline` uses) — B-3 keeps the "never serve unvalidated config" invariant; the spec 1 fixture should cover the disabled→disabled common case and one enabled case.
- **Visual render-match check**: `validateSaveInput` runs `checkVisualRenderMatch` for `source=visual` when `wizard_state` is present; a revision written before 0019 has NULL `wizard_state`, so restore of such a revision restores text only and leaves the stored graph — the D3 "graph is the source of truth" rule then makes the builder load a graph that no longer matches the text. Backend must document this in the handler comment; docs-tests notes it in the CHANGELOG entry ("revisions written before this release carry no graph").
- **Chunk boundary**: a stray `import { MergeView } from '@codemirror/merge'` in a page file pulls the merge package (and, transitively, `@codemirror/view`) into the entry chunk; W-7's grep + the `chunkSizeWarningLimit` are the two controls.
- **routeCoverage drift**: if anyone adds the two REST routes to the §12 fence later, `web/src/routeCoverage.test.ts` needs the `SPEC_TO_PROCEDURE` rows (W-5 adds them pre-emptively).
- **Fullstack spec may ship unrun** if no Docker stack is available to the docs-tests agent; the orchestrator must run `make test-fullstack` before the release, and the ledger must not claim fullstack coverage until it has.
