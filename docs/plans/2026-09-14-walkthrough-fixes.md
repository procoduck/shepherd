# Walkthrough fixes — implementation plan (2026-09-14)

Closes the findings of the v0.6.0 manual UI walkthrough (chart 0.10.2, kind, three live Alloy
1.19.2 agents). Sixteen findings: 5 high, 7 medium, 4 low. Every root cause below was read in the
code before a fix was designed; the file:line references are to `main` at `5aa43eb`.

Integration branch: `fix/walkthrough-2026-09-14` (cut from `main` at `5aa43eb`). Eight slices, built
in parallel on branches cut from it, each owning a **disjoint** file set so the merges are
mechanical. The orchestrator pushes; nobody in a slice runs `git push` or touches `main`.

Repository rules (AGENTS.md, web/AGENTS.md, CONTRIBUTING.md) apply verbatim and are not restated,
except the four that decide the shape of every slice: a red run first for every new control, never
weaken an existing test, never commit `internal/spa/dist` (after any Playwright run:
`git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets`), and no ask-first
change (dependency, `proto/`, RBAC semantics, served-config content/hash) — those live in §3.

## 0. Findings and root causes, as verified

| # | Sev | Finding (one line) | Root cause, verified in code | Slice |
|---|---|---|---|---|
| F1 | high | Every page-owned dialog keeps only the first typed character | `web/src/components/ui/Modal.tsx:39-72` — the focus-trap effect is keyed on `[onClose]` and calls `panelRef.current?.focus()` (line 41) on every run; pages that pass an inline arrow (`DestinationsPage.tsx:228`, `GitPage.tsx:424`, `TeamsPage.tsx:202`, `AdminTokensPage.tsx:142`, `AdminOrgsPage.tsx:190`, `AdminClustersPage.tsx:143`) and keep the form state in the page re-render per keystroke → new `onClose` → effect re-runs → focus leaves the input. `AdminModal.tsx` is a re-export of the same component (line 6), so the admin dialogs are the same bug. `CreateUserModal`/`EditUserModal`/`ResetPasswordModal` pass a stable `onCancel` prop and hold their own state, hence unaffected. | S1 |
| F2 | high | Visual builder unreadable in light mode | The app's only base foreground is the raw utility `text-zinc-100` on `<body>` (`web/index.html:26`) and the Shell root (`web/src/components/Shell.tsx:174`); `web/src/index.css:56-80` redefines the nine surface/muted tokens for light and **no foreground** — `--color-zinc-100` stays Tailwind's `oklch(96.7% …)` in both themes (confirmed in the built `internal/spa/dist/assets/index-*.css`). Everything that *inherits* colour is near-white on `#fafafa`/`#fff`. The builder is where almost every label inherits: palette names (`Palette.tsx:163`, no colour class), node titles (`PipelineNode.tsx:349-352`), toolbar name/matcher inputs (`Toolbar.tsx:134,176` — `bg-background` + inherited colour), Flow check/Simulate/Save (`Toolbar.tsx:215,225`, `SandboxRunPanel.tsx:419`), the dialog title (`Modal.tsx:86`) and captured-series names (`SandboxRunPanel.tsx:150-164`, the `sim-series-table` columns whose only class is `font-mono`). Ordinary pages mostly route text through `text-muted*` tokens, which is why they read acceptably. Three dark-only literals compound it: `Palette.tsx:127-133` (`bg-indigo-950/50 text-indigo-300 hover:text-white`), `PipelineNode.tsx:336` (`bg-[#131f17]`), `CanvasPane.tsx:751-754` (MiniMap `bgColor='#0e0e11' nodeColor='#3f3f46'`). `theme.test.ts:80-89` documents `text-zinc-100/200/300` as the intended "foreground" shades. | S2 |
| F3 | high | Graph view, Verify render and simulate panels use the first org | `GraphViewPage.tsx:52-57` calls `getMe()` and takes `me.orgs[0].id`; `BottomDrawer.tsx:54-58` reads `window.__initialMe.orgs[0]` (documented never set in production, `main.tsx:14`) and returns silently with no outcome on a match (line 59); `BottomDrawer.tsx:64-77` uses `me.orgs[0].id` for both simulations; `UpgradeReview.tsx:59-62` same. `useOrg()`/`useOrgId()` (`web/src/hooks/useOrg.ts:74,106`) already resolve the selected org and `VisualBuilderPage.tsx:48,166-191` already does the cross-org fallback for shareable URLs. | S3 |
| F4 | high | Wizard-created pipelines have no revision and no audit row | `internal/mgmtapi/rpc_wizard.go:166-221` `CommitWizard` calls `CreatePipeline` (the store query) directly and returns; it never calls `createRevision` (`rpc_pipeline.go:1046`) nor `auditLog` (`helpers.go:65`) — both of which `PipelineService.CreatePipeline` does at `rpc_pipeline.go:517-520`. It also skips `validateSaveInput` (`rpc_pipeline.go:481`), so the commit is the one config write that bypasses Stage 1/2 (`RenderWizard` runs `Stages12` for the preview only, line 108). | S4 |
| F5a | high | Stored renders of visual pipelines are not regenerated after a schema bump | `internal/visual/render.go:554-559` stamps the header `schema <version>` from the schema used at save time; `pipelines.contents` is whatever the last Save rendered (`Toolbar.tsx:58-68`). Nothing re-renders on a bump — by design (served content changes are ask-first). The server can already *identify* them: `ListPipelinesRequest.needs_upgrade` (`pipeline.proto:95-98`, implemented at `rpc_pipeline.go:369-378` via `visualNeedsUpgrade`, line 218) and `Pipeline.wizard_state.schema_version`; the pipelines list (`PipelinesPage.tsx`) shows none of it. | S6 |
| F5b | high | Collector reads APPLIED while its agent rejected the served config | `internal/agentapi/service.go:143-155` persists whatever `RemoteConfigStatus` the poll carries; `service.go:234-274` + `internal/store/queries/collector_instances.sql:31-59` (`ClearStaleFailedStatus`) set `APPLIED` whenever a poll carries **no** status and its `hash` equals the served hash — and also when the stored status is NULL. "APPLIED" therefore means "the agent polls with the served hash", i.e. *received*. Nothing in the protocol Shepherd reads says *loaded*: `effective_config` (`proto/collector/v1/collector.proto:33`) is never read; beacon rows (`beacon_handler.go:135-141`, `UpsertBeaconComponent`) are keyed by token + the scrape `instance` label (`internal/beacon/project.go:56-88`), not by collector instance, and component health cannot tell a fresh load from the cached old config (an agent running its cache has healthy components). Blocked — see §3. | — |
| F6 | med | Text editor Save does not refresh the revision list or "Updated by" | `web/src/pages/PipelineEditorPage.tsx:111-115` — `saveMutation.onSuccess` invalidates only `['pipelines', orgId]`; `restoreMutation.onSuccess` (lines 138-140) invalidates `['pipeline', orgId, id]` and `['revisions', orgId, id]` too. | S6 |
| F7 | med | Self Monitoring wizard drops the log-collection step | `internal/wizard/selfmonitoring/wizard.go:128` — `logsEnabled := toggle && logsDest != "" && logPath != ""`; `log_path` (lines 77-79) has a `Placeholder` but no `Default`, and the runner seeds form values from `Default` only (`WizardRunnerPage.tsx:64-78`), so a blank path silently disables the block. No warning channel exists (`wizard.CommitResult`, `wizard.go:54-65`). | S4 |
| F8 | med | Upgrade Review reports edge-supplied attributes as newly required | `internal/visual/upgrade.go:138-152` — `attr_added_required` fires when `attr.Required && node.Props[attr.Name]` is missing, ignoring `doc.Edges` whose `to.port`/`from.port` names that attribute (`targets`, `forward_to` are ports, `render.go:56-63`). The class is also mis-named for an attribute that was required in the old schema too (`oldDef` is available, line 108). | S7 |
| F9 | med | Agent token ID is shown nowhere | `AdminTokensPage.tsx:14-52` columns are Name/Status/Created by; the created dialog (lines 168-208) shows `newSecret.name`+`secret` only, although `CreateAgentTokenResponse.id` (`admin.proto:181-185`) is returned and `AgentToken.id` is in the list rows. | S6 |
| F10 | med | Two problem counters disagree | `Toolbar.tsx:24,201` counts `severity === 'error'`; `BottomDrawer.tsx:179-181` shows `diagnostics.length` (errors + warnings). | S3 |
| F11 | med | Wizard destination fields are free text; a matcher is added silently | `WizardStepFields.tsx:94-101` renders every `type: "text"` field as `<Input>`; `metrics_dest_name`/`logs_dest_name` are declared `text` (`selfmonitoring/wizard.go:66,81`) although `ListDestinations` is available to the runner. `wizard.go:180-184` appends `role="singleton"` after the user's `cluster_pattern`; step 3's only field has no description of that (lines 90-93), and the review (`WizardRunnerPage.tsx:187-196`) renders the chips without saying which were added. | S4 (Go text) + S5 (UI) |
| F12 | med | Palette search ranks a fuzzy match above the exact one | `Palette.tsx:52-58` filters with `includes()` and keeps schema object order; there is no ranking. | S3 |
| F13 | low | Live agent's instance row has an empty Name | `service.go:105` `RegisterCollector` upserts `req.Msg.Name` verbatim (Alloy sends none) → the INSERT stores `''`; every later `GetConfig` (lines 135-139) falls back to the wire id, but `UpsertCollectorInstance`'s `CASE` (`collector_instances.sql:12-15`) treats an incoming name equal to the id as "keep the stored name" — which is `''` forever. `CollectorInstance` (`fleet.proto:23-32`) has no id field, so the UI cannot substitute one. | S7 |
| F14 | low | Editor shows the browser's spell-check underline | `web/src/editor/AlloyEditor.tsx:88-103` — no `EditorView.contentAttributes`, so CodeMirror's contenteditable inherits the browser default `spellcheck`. | S8 |
| F15 | low | Flow check has no textual outcome | `Toolbar.tsx:211-218` toggles `flowCheckActive`; the only consumer is `CanvasPane.tsx:339` (edge animation via `reconcileEdges`). No text is derived. | S3 |
| F16 | low | Chart needs Kubernetes 1.29 with CNPG on, but declares 1.25 | `deploy/helm/shepherd/Chart.yaml:11` `kubeVersion: ">=1.25.0-0"` is right for the plain install; `scripts/docs-content/requirements.html:11-22` and `database.html:55-77` never state the operator path's floor. | S8 |

## 1. Repository facts every slice depends on

- `web/src/components/admin/AdminModal.tsx` re-exports `Modal`/`ModalActions`; fixing `Modal.tsx`
  fixes every dialog in the app. `overlays.test.tsx` and `AdminModal.test.tsx` cover focus/Escape/
  return-focus and must stay green.
- `web/src/theme.test.ts` enforces: exact hex per token in the `@theme` block and in **both** light
  blocks (`html.light` and `:root:not(.dark)`), no raw `*-zinc-*` utilities outside the
  `text-zinc-100/200/300` allowlist, no `-foreground`/`primary`/… phantom classes under
  `web/src/visual/`. A new token must be added to `EXPECTED_TOKENS_*` (extending the check) and must
  not be named `*foreground*`.
- `web/src/index.css`'s light rules are **unlayered**, so they beat Tailwind's `@layer theme`
  `--color-zinc-*` definitions regardless of specificity — redefining `--color-zinc-100/200/300` on
  `html.light` and `:root:not(.dark)` flips every `text-zinc-100/200/300` and the inherited base
  colour at once. Nothing in `web/src` uses `bg-/border-/ring-zinc-100..300` (`RAW_ZINC_PATTERN`
  would fail on it), so the redefinition affects text only.
- The mocked Playwright suite fails on any unmatched request and any `console.error`; personas are
  used verbatim as `window.__initialMe` (`tests/fixtures/personas.ts:1-5`), so `BottomDrawer`'s
  Verify *works in the mocked suite* and is dead only in production — the red run for F3's Verify
  must assert the **org id in the request body**, not "the button does something".
- The two-org persona + `localStorage['shepherd.orgId']` pattern is in
  `web/tests/specs/org-switcher.spec.ts:10-23`; `api.calls('<Service>/<Proc>')` returns request
  bodies; `api.override` replaces one mock handler for one test.
- `ListPipelines`' default mock (`tests/mocks/handlers.ts:951`) ignores `needsUpgrade`;
  `pipelineToWire` (line 111) already carries `wizardState`. Only S6 may edit `handlers.ts`.
- `visualNeedsUpgrade` compares `wizard_state.schema_version` with `schema.CurrentVersion()`; the
  builder learns the served version through `currentSchemaVersion(schema)`
  (`web/src/visual/schemaVersion.ts`), never from a literal.
- `createRevision` is a method on `PipelineService` (`rpc_pipeline.go:1046-1063`) that needs only
  `s.store`; `auditLog` is package-level. `WizardService` has `store` and `validator`.
- `internal/mgmtapi/rpc_pipeline_test.go:266-290` and `rpc_revisions_test.go` show how specs read
  `ListPipelineRevisions` and `ListAuditLog(Column1: orgUUID(orgID), Limit: 100)` directly.
- `internal/wizard/selfmonitoring/wizard_test.go` is golden-based (`testdata/*.golden.alloy`);
  `schema_conformance_test.go` lists every attribute Commit emits — adding no new attribute keeps it
  untouched.
- Generated files: `internal/store/sqlc/*` regenerates from `.sql` via `make generate`
  (needs `pnpm install` in `web/` once). No proto changes anywhere in this plan.
- `scripts/docs-content/*.html` → `make docs` → `site/docs/` (never hand-edit); `make
  check-docs-drift` is in `make lint`.
- Go tests run from the repo root; `internal/agentapi` and `internal/mgmtapi` specs are
  `Label("integration")` on testcontainers Postgres.

## 2. Slices

Each slice: branch `fix/wt-<key>` from `fix/walkthrough-2026-09-14`; one commit per slice (or a
few, all with the trailers); red-run evidence (command + failing assertion text, verbatim) goes in
the slice's report and in the commit body.

### S1 — `modal-focus` · Modal keeps focus in the input (F1)

**Files:** `web/src/components/ui/Modal.tsx`, `web/src/components/ui/overlays.test.tsx`,
`web/tests/specs/dialogs.spec.ts`.

**Approach.** In `Modal.tsx` keep the latest `onClose` in a ref (`const onCloseRef = useRef(onClose);
onCloseRef.current = onClose;` — the same pattern `UpgradeReview.tsx:55-56` uses for `docRef`),
have the Escape branch call `onCloseRef.current()`, and key the effect on `[]` so the panel is
focused **once per mount** and the keydown listener is installed once. Nothing else changes: the tab
trap, `aria-modal`, return-of-focus on unmount, `ModalActions`. Callers are not touched — the fix is
in one place, as the finding asks.

**Red run (write first).** `web/tests/specs/dialogs.spec.ts`, new test
`typing character by character into a page-owned dialog keeps every character`:
`/destinations` → New destination → `getByLabel(/name/i).pressSequentially('prom-eu-1', { delay: 20 })`
→ `expect(input).toHaveValue('prom-eu-1')`; a second block does the same on `/admin/tokens` → New
token (the `AdminModal` alias path). On today's code both fail with
`Expected string: "prom-eu-1" Received string: "p"`. Record the exact text.
Also add a vitest in `overlays.test.tsx`: render `<Modal onClose={() => {}}>` with an input,
re-render with a *new* arrow, assert `document.activeElement` is still the input — red today.

**Acceptance.** Both Playwright tests green; every existing `dialogs`, `admin*`, `teams`,
`destinations`, `git*` spec green; `overlays.test.tsx` + `AdminModal.test.tsx` green (Escape still
closes, focus still returns).

**Check.**
```
cd web && pnpm typecheck && pnpm biome check . && pnpm test
cd web && pnpm exec playwright test tests/specs/dialogs.spec.ts tests/specs/admin.spec.ts tests/specs/teams.spec.ts tests/specs/destinations.spec.ts tests/specs/git-page.spec.ts
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
```

### S2 — `light-foreground` · a light-mode foreground for the builder (F2)

**Files:** `web/src/index.css`, `web/src/theme.test.ts`, `web/src/visual/components/PipelineNode.tsx`,
`web/src/visual/components/CanvasPane.tsx`, `web/tests/specs/theme.spec.ts`.

**Approach.**
1. `index.css`: in **both** light blocks (`@media (prefers-color-scheme: light) { :root:not(.dark) }`
   and `html.light`) add `--color-zinc-100: #18181b; --color-zinc-200: #27272a;
   --color-zinc-300: #3f3f46;` with a comment: these three are the design system's foreground shades
   (theme.test.ts allowlist), so in light mode they *are* the foreground token — redefining them is
   what makes `<body class="text-zinc-100">`, the Shell root and everything that inherits from them
   dark on light, with no per-component churn. Contrast on `#fafafa`/`#ffffff`: all ≥ 10:1.
2. `theme.test.ts`: add the three entries to `EXPECTED_TOKENS_LIGHT` (they are checked in both
   blocks by the existing `it.each`), and amend the allowlist comment so it says the shades are
   theme-flipped in `index.css`. Do not touch `EXPECTED_TOKENS_DARK` (Tailwind defines the dark
   values).
3. Builder literals: `PipelineNode.tsx:336` `bg-[#131f17]` → `bg-emerald-500/10` (works on both
   surfaces); `CanvasPane.tsx:751-754` MiniMap `bgColor`/`nodeColor`/`nodeStrokeColor` → values
   chosen from `theme` (`useTheme` is already in scope there: dark keeps the current literals, light
   uses `#f4f4f5` / `#d4d4d8` / `#4f46e5`, the light panel/border-strong/accent hexes).
   `Palette.tsx`'s compat-banner literals are fixed in S3 (file ownership).
4. The Toolbar, Sandbox-run dialog title and captured-series names need no edit: they inherit, and
   step 1 flips what they inherit. The spec proves it.

**Red run (write first).** `web/tests/specs/theme.spec.ts`, new describe `visual builder text
follows the theme`: login `appAdmin`, seed `schemaFixture`, `emulateMedia dark`, open
`/pipelines/visual/new`, click `[data-component="prometheus.scrape"]`; read
`getComputedStyle(el).color` for (a) `[data-testid="palette-item-prometheus.scrape"] span.font-mono`,
(b) `[data-testid="pipeline-node"] .font-mono span` (node title), (c) `[data-testid="toolbar-name"]`,
(d) open `simulate-menu-trigger` → `simulate-menu-sandbox-run` → `[data-testid="sandbox-run-dialog"] h2`
(the `CreateRun`/`GetRun` mocks exist; close the dialog before toggling). Toggle the theme
(`getByRole('button', { name: /toggle theme/i })`), `expect(html).toHaveClass(/light/)`, then
`expect.poll` each colour `not.toBe(dark)` **and** assert its relative luminance (parse `rgb(r, g, b)`,
WCAG formula inline in the spec) is `< 0.3`. Today all four read `rgb(244, 244, 245)` before and
after → the first `not.toBe` fails: `Expected: not "rgb(244, 244, 245)" Received: "rgb(244, 244, 245)"`.

**Acceptance.** New theme spec green; existing `theme.spec.ts` cases green (body background
assertions unchanged); `theme.test.ts` green with the three new light entries; no raw zinc / phantom
class regressions; the whole `visual-*` spec set green (colour changes only).

**Check.**
```
cd web && pnpm typecheck && pnpm biome check . && pnpm test
cd web && pnpm exec playwright test tests/specs/theme.spec.ts tests/specs/visual-canvas.spec.ts tests/specs/visual-drag-highlight.spec.ts tests/specs/visual-simulate-s3.spec.ts
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
```

### S3 — `visual-components` · selected org, verify outcome, counters, palette rank, flow-check text (F3, F10, F12, F15, Palette half of F2)

**Files:** `web/src/visual/components/GraphViewPage.tsx`, `web/src/visual/components/BottomDrawer.tsx`,
`web/src/visual/components/UpgradeReview.tsx`, `web/src/visual/components/UpgradeReview.test.tsx`,
`web/src/visual/components/Palette.tsx`, `web/src/visual/components/Toolbar.tsx`,
`web/src/visual/paletteSearch.ts` (new), `web/src/visual/paletteSearch.test.ts` (new),
`web/tests/specs/visual-graph-view.spec.ts`, `web/tests/specs/visual-simulate-s2.spec.ts`,
`web/tests/specs/visual-code-sync.spec.ts`, `web/tests/specs/visual-toolbar-save.spec.ts`,
`web/tests/specs/visual-palette-search.spec.ts` (new).

**Approach.**
- **Org (F3).** `GraphViewPage`: replace the `getMe()` chain with `useOrg()`; try `orgId` first and,
  on `not_found`, the user's other orgs, then `setOrgId(found)` — the same fallback
  `VisualBuilderPage.tsx:166-191` uses, so a shared `/graph` URL still opens. `BottomDrawer`:
  `useOrgId()` for `verify`, `runRelabel`, `runLogs`; delete the `window.__initialMe` read.
  `UpgradeReview`: `useOrgId()` instead of `me.orgs[0]`; its vitest mocks `useMe` — switch the mock
  to `../../hooks/useOrg` (`useOrgId: () => 'org-0001'`) and keep every assertion.
- **Verify outcome (F3).** After `renderVisual`, render `<span data-testid="verify-render-result">`
  reading `Server render matches` or `Server and client render differ` (keep the red class for the
  mismatch case; `serverMismatch` becomes a tri-state `null | 'match' | 'mismatch'`).
- **Counters (F10).** The drawer tab shows the same number as the toolbar chip: `Problems {errors}`
  with `errors = diagnostics.filter(d => d.severity === 'error').length`, plus `· {warnings} warning(s)`
  when any; the red/green class keys on `errors`. The problems list itself still lists everything.
- **Palette rank (F12).** New pure `rankPaletteItems(query, items)` in `paletteSearch.ts`: score
  exact name match > name segment exact (`prometheus.remote_write` for `remote_write`) > name prefix
  > name substring > doc substring; stable sort within a tier by name. `Palette.tsx` uses it inside
  the `search` branch; category grouping is preserved but categories are emitted in the order of
  their best-ranked item when a search is active (so the exact hit's category comes first).
- **Palette literals (F2 half).** `Palette.tsx:127-133` → `bg-accent/10 text-accent`,
  `hover:text-white` → `hover:text-zinc-100`.
- **Flow check text (F15).** `Toolbar.tsx`: while `flowCheckActive`, render
  `<span data-testid="flow-check-result">` derived from the store — `Flow OK · N nodes, M wires` when
  `errors === 0`, else `Flow broken · K problem(s)`; nodes counted as `doc.nodes.filter(!disabled)`,
  wires as `doc.edges.length`. Pure derivation from state already in the store; no store change.

**Red runs (write first, in this order).**
1. `visual-graph-view.spec.ts`, `graph view is fetched for the selected org, not the first`: the
   two-org persona from `org-switcher.spec.ts`, `addInitScript` sets `localStorage['shepherd.orgId']
   = 'org-0002'`, seed the pipeline with `org_id: 'org-0002'`; after `graph-view` renders,
   `expect(api.calls('VisualService/GraphView')[0].body.orgId).toBe('org-0002')`. Today:
   `Expected: "org-0002" Received: "org-0001"`.
2. `visual-simulate-s2.spec.ts`, `relabel simulation uses the selected org`: same persona/storage;
   click `simulate-relabel-run`; assert `api.calls('SimulateService/SimulateRelabel')[0].body.orgId
   === 'org-0002'`. Today `"org-0001"`.
3. `visual-code-sync.spec.ts`, `Verify render reports a match for the selected org`: open the Code
   tab, click Verify; `expect(getByTestId('verify-render-result')).toHaveText(/matches/)` and the
   `VisualService/Render` call body `orgId === 'org-0002'`. Today the test id does not exist
   (`Timed out … waiting for getByTestId('verify-render-result')`).
4. `visual-toolbar-save.spec.ts`, `the drawer problems tab counts what the toolbar chip counts`:
   place a node that yields ≥1 error and ≥1 warning against `schemaFixture` (check the fixture; if
   no component yields a warning, seed a graph via `vb:import-graph` with a disabled-node warning
   the way `visual-upgrade.spec.ts` seeds), read the chip's number and assert
   `drawer-tab-problems` text starts with `Problems <same number>`. Today the tab shows the larger
   total.
5. `paletteSearch.test.ts` (vitest): `rankPaletteItems('remote_write', [receive_http, remote_write])[0].name
   === 'prometheus.remote_write'` — red against a first implementation that returns the input order
   (write the function as `return items.filter(...)` first, run, capture `expected
   'prometheus.receive_http' to be 'prometheus.remote_write'`, then implement). Playwright
   `visual-palette-search.spec.ts` then asserts the first `[data-testid^="palette-item-"]` after
   typing `remote_write` is `prometheus.remote_write` (seed a schema variant if `schemaFixture` lacks
   `prometheus.receive_http`).
6. `visual-toolbar-save.spec.ts`, `flow check shows a textual outcome`: click `flow-check-toggle`,
   expect `flow-check-result` visible with `/Flow OK/`. Today: test id missing.

**Acceptance.** All six red runs green; `visual-graph-view`, `visual-simulate-s2/s3`,
`visual-code-sync`, `visual-toolbar-save`, `visual-upgrade`, `visual-inspector` specs green;
`UpgradeReview.test.tsx` green with the `useOrg` mock; no `orgs[0]`/`__initialMe` left under
`web/src/visual` (`grep` returns nothing).

**Check.**
```
cd web && pnpm typecheck && pnpm biome check . && pnpm test
cd web && pnpm exec playwright test tests/specs/visual-graph-view.spec.ts tests/specs/visual-simulate-s2.spec.ts tests/specs/visual-code-sync.spec.ts tests/specs/visual-toolbar-save.spec.ts tests/specs/visual-palette-search.spec.ts tests/specs/visual-upgrade.spec.ts tests/specs/visual-inspector.spec.ts
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
grep -rn "orgs\[0\]\|__initialMe" web/src/visual   # must print nothing
```

### S4 — `wizard-commit-go` · wizard commits get a revision, an audit row, the gate; log step default (F4, F7, Go half of F11)

**Files:** `internal/mgmtapi/rpc_wizard.go`, `internal/mgmtapi/rpc_pipeline.go`,
`internal/mgmtapi/rpc_wizard_commit_test.go` (new), `internal/wizard/selfmonitoring/wizard.go`,
`internal/wizard/selfmonitoring/wizard_test.go`.

**Approach.**
- `rpc_pipeline.go`: extract the body of `PipelineService.createRevision` into a package-level
  `createPipelineRevision(ctx, st *store.Store, p sqlc.Pipeline, note, actor string) error`; the
  method becomes a one-line delegate so every existing caller (create/update/restore) is unchanged.
- `rpc_wizard.go` `CommitWizard`, after `wiz.Commit` and before the insert: run the same Stage 1/2
  gate the editor's create runs — `s.validator.Stages12(ctx, validate.WrapForValidation(name,
  result.Contents))` and refuse with `connect.CodeFailedPrecondition` carrying the first diagnostic
  when `!Valid` (the wizard stays disabled-on-create, so Stage 3 belongs to `EnablePipeline` as
  today). After the insert: `createPipelineRevision(ctx, s.store, p, "created", actor)` (log-only on
  error, matching `rpc_pipeline.go:517-519`) and `auditLog(ctx, s.store, actor, orgID,
  "pipeline.create", "pipeline", p.ID.String())`. Same note, same action string, same store path.
- `selfmonitoring/wizard.go`: `log_path` gains `Default: "/var/log/alloy/*.log"`; in `Commit`, when
  the toggle is on and a Loki destination is named but the path is blank, use that default rather
  than dropping the block (`logsEnabled` no longer depends on `logPath != ""`). `cluster_pattern`'s
  `Description` gains one sentence: `The wizard also adds role="singleton" so only singleton
  collectors receive this pipeline.` (Go text is the schema the UI renders; S5 tags the chip.)
  Goldens are unchanged: both existing entries pass a path.

**Red runs (write first).**
1. `internal/mgmtapi/rpc_wizard_commit_test.go` (`Label("integration")`, the fixture from
   `rpc_wizard_test.go`): **(a)** `CommitWizard` then `st.Queries.ListPipelineRevisions(pid)` has one
   row, `Revision == 1`, `ChangeNote == "created"`, `Contents == pipeline.contents`; today `Expected
   <int>: 0 to equal <int>: 1`. **(b)** `st.Queries.ListAuditLog(orgUUID(orgID), Limit 100)` contains
   a row `Action == "pipeline.create"`, `ResourceID == pid`; today `to contain element matching …
   found 0`. **(c)** the "quote in scrape_url" state from `rpc_wizard_test.go`'s render case →
   `CommitWizard` returns HTTP 412 / `failed_precondition` and `ListPipelines` is empty; today it
   returns 200 and a row exists. **(d)** editor parity: `PipelineService/CreatePipeline` and
   `CommitWizard` on the same org each add exactly one revision and one `pipeline.create` row
   (a `DescribeTable` over the two procedures).
2. `selfmonitoring/wizard_test.go`: `It("tails logs at the default path when log_path is blank")` —
   state `{metrics_dest_name, logs_enabled: true, logs_dest_name: "loki-prod"}` → `Contents`
   `ContainSubstring("loki.source.file")` and `ContainSubstring("/var/log/alloy/*.log")`, and
   `Role == "singleton"`. Today: `Expected <string> … to contain substring "loki.source.file"`.
   Add `It("declares a default log path in the schema")` asserting the `log_path` field's `Default`.

**Acceptance.** Both new spec files green; `rpc_wizard_test.go`, `rpc_pipeline_test.go`,
`rpc_revisions_test.go`, `wizard_registration_test.go`, `capability_enumeration_test.go` unchanged and
green; selfmonitoring goldens + `schema_conformance_test.go` green; `make lint` 0 issues (the
`//nolint` budget is unchanged — reuse existing directives, add none).

**Check.**
```
make lint
go test ./internal/wizard/... -count=1
go test ./internal/mgmtapi/ -count=1
```

### S5 — `wizard-runner-ui` · destinations are picked, wizard-added matchers are labelled (UI half of F11)

**Files:** `web/src/wizard/WizardStepFields.tsx`, `web/src/wizard/WizardStepFields.test.tsx`,
`web/src/wizard/WizardRunnerPage.tsx`, `web/tests/specs/wizard.spec.ts`.

**Approach.**
- `WizardRunnerPage` queries `clients.destination.listDestinations({ orgId })` (mock exists) and
  passes `destinations` to `WizardStepFields`. A `text` field whose `name` ends in `_dest_name`
  renders a `<Select>` of the org's destinations of the matching type (`metrics_` → `prometheus`,
  `logs_` → `loki`, any other prefix → all), keeping the field's label/required/description; when
  the org has none of that type it falls back to today's `<Input>` plus a hint linking to
  `/destinations`. The wire shape is unchanged: the value is still the destination *name*.
- Review: a matcher chip whose quoted value is not one of the form's string values gets a
  `data-testid="wizard-added-matcher"` badge `added by the wizard` (`role="singleton"` on
  self-monitoring; `role="<chosen>"` on app-observability is *not* tagged because the user picked
  it).
- `commitMut.onSuccess` also invalidates `['pipelines', orgId]` so the list shows the new pipeline
  without a reload.
- Existing `wizard.spec.ts` walk loop: it fills empty `input[type="text"]`; extend it to also pick
  the first real option of any empty required `select` — the walk still visits every step and still
  commits (not a weakening; it adapts to the control type).

**Red runs (write first).** `wizard.spec.ts`: **(a)** seed `destinations: [destination({name:'prom-prod',
type:'prometheus'}), destination({name:'loki-prod', type:'loki'})]`, walk to the Destinations step,
`expect(page.getByLabel('Metrics destination')).toHaveRole('combobox')` and its options contain
`prom-prod` and not `loki-prod`; today: `Expected: "combobox" Received: "textbox"`. **(b)**
`api.override('POST','/shepherd.mgmt.v1.WizardService/RenderWizard', …)` returning matchers
`['cluster=~"prod-.*"', 'role="singleton"']`; on Review expect exactly one `wizard-added-matcher`
badge, next to the `role` chip; today: count 0. Vitest in `WizardStepFields.test.tsx`: a
`metrics_dest_name` text field with one prometheus destination renders a select — red today.

**Acceptance.** New assertions green; the original walk-and-commit test green; `WizardStepFields`/
`WizardStepper` vitests green; `route-guard`, `rbac`, `persona-floor` specs (they visit `/wizards`)
green.

**Check.**
```
cd web && pnpm typecheck && pnpm biome check . && pnpm test
cd web && pnpm exec playwright test tests/specs/wizard.spec.ts tests/specs/rbac.spec.ts tests/specs/persona-floor.spec.ts
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
```

### S6 — `pages-list-editor-tokens` · stale-render badge, editor Save refresh, token ID (F5a, F6, F9)

**Files:** `web/src/pages/PipelinesPage.tsx`, `web/src/pages/PipelineEditorPage.tsx`,
`web/src/pages/AdminTokensPage.tsx`, `web/tests/mocks/handlers.ts`,
`web/tests/specs/pipelines-list.spec.ts`, `web/tests/specs/pipeline-editor.spec.ts`,
`web/tests/specs/admin.spec.ts`.

**Approach.**
- **F5a (UI only, no serve-time change).** `PipelinesPage` adds a second query
  `['pipelines', orgId, 'needs-upgrade'] → listPipelines({ orgId, needsUpgrade: true })` (the
  server already computes it, `rpc_pipeline.go:369-378`) and, for every id in that set, renders in
  the Source cell a badge `data-testid="pipeline-stale-render"`: `rendered under
  <wizard_state.schema_version> — open the builder and Save to re-render`, linking to
  `/pipelines/$id/visual` (where the upgrade banner and Save do the work). Read the version from
  `(p.wizardState as { schema_version?: string } | undefined)?.schema_version`. Mock: make the
  default `ListPipelines` handler honour `needsUpgrade` — filter `source === 'visual'` whose
  `wizard_state.schema_version` is non-empty and differs from `st.schema?._meta?.alloy_version`
  normalised as `currentSchemaVersion` does (fall back to `'alloy-v1.18.1'`, the version every other
  mock names, when no schema is seeded).
- **F6.** `saveMutation.onSuccess` in `PipelineEditorPage` also invalidates
  `['pipeline', orgId, id]` and `['revisions', orgId, id]` (exactly the restore path's set) and
  re-arms `seededFor.current = p.id` the way restore does, so the refetched pipeline does not
  overwrite the form.
- **F9.** `AdminTokensPage`: an `ID` column (`font-mono text-xs select-all`, first column after
  Name) with a copy button, and the created dialog shows `id` (from `CreateAgentTokenResponse.id`)
  above the secret with its own copy button and the caption `remotecfg username`. The secret handling
  (`newSecret` state, never logged) is unchanged; `newSecret` gains `id`.

**Red runs (write first).**
1. `pipelines-list.spec.ts`, `a visual pipeline rendered under an older schema is badged`: seed
   `schema: schemaFixture` and two visual pipelines, `wizard_state.schema_version` `'alloy-v1.12.0'`
   and the fixture's current; expect `pipeline-stale-render` count 1, inside the older one's row,
   with `href` to `/pipelines/<id>/visual`. Today: `Expected: 1 Received: 0`.
2. `pipeline-editor.spec.ts`, `Save refreshes the revision list and Updated by`: seed one revision;
   edit, Save; the mock `UpdatePipeline` must also unshift a revision and set `updated_by` (edit the
   mock in `handlers.ts` — additive); expect the toggle text `Revision history (2)` and
   `Updated by: …` to update **without** `page.reload()`, and
   `api.calls('PipelineService/ListRevisions').length` to be 2 (one on load, one after save).
   Today: `Expected string: "Revision history (2)" Received: "Revision history (1)"`.
3. `admin.spec.ts`, `token list and created dialog show the token id`: seeded tokens show their `id`
   in a cell; after New token → Create, the dialog contains the mock's `tok-…` id. Today: `getByText('tok-0001')` not found.

**Acceptance.** Three red runs green; `pipelines-list`, `pipeline-editor`, `revisions`,
`editor-role`, `admin`, `admin-users`, `org-switcher` specs green (org-switcher overrides
`ListPipelines` itself, so its filter still applies); `routeCoverage.test.ts` unchanged.

**Check.**
```
cd web && pnpm typecheck && pnpm biome check . && pnpm test
cd web && pnpm exec playwright test tests/specs/pipelines-list.spec.ts tests/specs/pipeline-editor.spec.ts tests/specs/revisions.spec.ts tests/specs/editor-role.spec.ts tests/specs/admin.spec.ts tests/specs/org-switcher.spec.ts
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
```

### S7 — `backend-small` · upgrade diff honours wired ports; instance name never empty (F8, F13)

**Files:** `internal/visual/upgrade.go`, `internal/visual/upgrade_test.go`,
`internal/agentapi/service.go`, `internal/agentapi/service_test.go`,
`internal/store/queries/collector_instances.sql`, `internal/store/sqlc/collector_instances.sql.go`
(regenerated only).

**Approach.**
- **F8.** In `UpgradeCheck`, before the attribute scan, build `wired[nodeID] = set(port names)`
  from `doc.Edges` (`to.port` for the target node, `from.port` for the source node). An attribute is
  "satisfied" when it is in `node.Props` **or** in `wired[node.ID]`. Additionally only classify
  `attr_added_required` when the attribute was not already required in `oldDef` (it is *added*
  by the upgrade); an attribute required in both schemas and unsatisfied is a pre-existing L1
  problem the builder already reports, not an upgrade finding. Exported behaviour otherwise unchanged
  (`NeedsUpgrade`, other classes).
- **F13.** `RegisterCollector` applies the same fallback `GetConfig` does (`name = req.Msg.Name;
  if name == "" { name = req.Msg.Id }`); `UpsertCollectorInstance`'s `CASE` keeps the stored name
  only when it is itself non-empty: `WHEN (EXCLUDED.name = '' OR EXCLUDED.name = collector_instances.id)
  AND collector_instances.name <> '' THEN collector_instances.name ELSE COALESCE(NULLIF(EXCLUDED.name,''),
  collector_instances.id)`. Then `make generate` (sqlc only regenerates; commit the diff under
  `internal/store/sqlc/`). Existing rows with `''` heal on the agent's next poll.

**Red runs (write first).**
1. `upgrade_test.go`, `7.4.5.x — a required attribute satisfied by an edge is not reported`: a
   two-node doc (source with an output port, target `test.component` whose required attr is the
   port name — use/add a component in `testdata/schemas/v_new.json` whose required attr matches an
   input port `prop`) wired by one edge; expect no `attr_added_required` item. Today: `Expected …
   not to contain element matching HaveField("Class", "attr_added_required")`. Second case: an
   attribute required in **both** `v_old` and `v_new` and absent → not reported; today reported.
2. `service_test.go`, under the existing register/getconfig suite: `RegisterCollector` with
   `Name: ""` then `GetConfig` with no `collector.name` attribute → the instance row's `Name ==
   req.Id`. Today: `Expected <string>: "" to equal <string>: "<id>"`.

**Acceptance.** Both red runs green; `internal/visual`, `internal/agentapi`, `internal/store`
suites green; `make generate` idempotent (`git status --porcelain internal/store/sqlc` empty after
commit); `make lint` 0 issues.

**Check.**
```
cd web && pnpm install --frozen-lockfile && cd .. && make generate && git status --porcelain gen internal/store/sqlc web/src/gen
make lint
go test ./internal/visual/ ./internal/agentapi/ ./internal/store/ -count=1
```

### S8 — `docs-editor` · spell-check off, documented floors, changelog and ledger (F14, F16, records for the batch)

**Files:** `web/src/editor/AlloyEditor.tsx`, `web/tests/specs/editor-spellcheck.spec.ts` (new),
`scripts/docs-content/requirements.html`, `scripts/docs-content/database.html`, `site/docs/**`
(regenerated by `make docs` only), `CHANGELOG.md`, `docs/project-status.md`.

**Approach.**
- **F14.** Add `EditorView.contentAttributes.of({ spellcheck: 'false', autocorrect: 'off',
  autocapitalize: 'off' })` to the extension list in `AlloyEditor.tsx` (both read-only and editable
  paths share it). Chunk boundary unchanged (`@codemirror/view` is already inside the lazy chunk).
- **F16.** `requirements.html`: the Kubernetes row states the two floors — 1.25+ for the plain chart
  (what `kubeVersion` enforces) and **1.29+ when `cnpg.enabled=true`**, because the pinned
  CloudNativePG 0.29.0 chart refuses older nodes and the operator is a prerequisite the chart does
  not install; `database.html` "Let the chart provision one" gets the same one-line note next to
  the "does not install the operator" note, plus the kind pin CI uses (`kindest/node:v1.31.4`) as a
  known-good. `Chart.yaml` is **not** changed (its floor is correct for the default install). Then
  `make docs`; commit the regenerated `site/docs/`.
- `CHANGELOG.md` `## Unreleased`: a **Fixed** list covering every slice of this plan (one line each,
  Shipped taxonomy), and a **Known** line for F5b pointing at §3 of this plan. `docs/project-status.md`
  §4 "Smaller follow-ups": add F5b (blocked, options listed) and the wizard warning-channel gap (no
  `Warnings` on `CommitResult`; would be a proto change), and note the walkthrough report as the
  source. This slice writes the records for all eight slices; the others do not touch these files.

**Red run (write first).** `editor-spellcheck.spec.ts`: open `/pipelines/new` as `appAdmin`,
`expect(page.locator('.cm-content')).toHaveAttribute('spellcheck', 'false')`. Today the attribute is
absent: `Expected: "false" Received: null` (record the exact text). The docs half has no runtime
control; its checks are `make docs && make check-docs-drift && make check-docs-version`.

**Acceptance.** Spec green; `editor-autocomplete`, `pipeline-editor` specs green;
`make check-docs-drift` and `make check-docs-version` pass; `git status --porcelain site/docs`
empty after commit; the changelog names every slice actually merged (the orchestrator adjusts the
list at integration if a slice is dropped).

**Check.**
```
cd web && pnpm typecheck && pnpm biome check . && pnpm test
cd web && pnpm exec playwright test tests/specs/editor-spellcheck.spec.ts tests/specs/editor-autocomplete.spec.ts
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
make docs && make check-docs-drift && make check-docs-version
```

## 3. Blocked — needs a decision (not a slice)

### B1 — F5b: APPLIED cannot distinguish "received" from "loaded"

What the code does today is in the table above; the short version: `remote_config_status` is the
agent's own last report, and the B1 clearing rule (`ClearStaleFailedStatus`) promotes NULL/FAILED to
`APPLIED` whenever a status-less poll carries the served hash. Alloy 1.19.2's exact report sequence on
a config that passes Shepherd's gate but fails its own load (FAILED once then silence? never FAILED,
hash of the rejected content?) was not captured in the walkthrough and cannot be read from this
repository; every option below depends on it. Decide after a reproduction against a live agent
(serve the walkthrough's `targets = [discovery.kubernetes.pods.targets]` text to one agent and
record every `GetConfig` request: `hash`, `remote_config_status`, `effective_config`).

Options, in order of how little they change:

1. **Keep the status, stop the clear-back for a same-hash FAILED.** Add a migration with
   `remote_config_status_hash text` on `collector_instances`; `UpdateInstanceStatus` records the poll's
   `hash` beside a FAILED; `ClearStaleFailedStatus` skips a FAILED recorded against the hash now being
   matched. No proto/RBAC/served-config change. Only helps if Alloy reports FAILED at least once with
   the new hash — the reproduction decides.
2. **Derive "loaded" from `effective_config`.** `GetConfigRequest.effective_config` is on the
   protocol already (`collector.proto:33`) and unread. If Alloy populates it, `GetConfig` can compare
   its bytes to the served content and write a new status value (`LOADED` vs `RECEIVED`, or keep
   `APPLIED` = loaded and add `RECEIVED`). No proto change, but a **new value in the status
   vocabulary** the UI (`CollectorsPage.tsx:11-13`, `CollectorDetailPage.tsx:16-18`), the e2e suite
   (`e2e/e2e_test.go:438-444`) and spec §4 all enumerate — that is an API-semantics decision.
3. **Join beacon health.** Requires keying `beacon_inventory` rows to a collector instance (the
   baseline's `instance` label would have to be made the wire id — a served-config content change,
   ask-first) and still cannot tell "new config loaded" from "old config still running healthily".
   Listed for completeness; not recommended.

### B2 — a warning channel for wizards

F7/F11 want the wizard to *say* when it drops or adds something. `wizard.CommitResult` and
`RenderWizardResponse` have no `warnings`; adding one is a `proto/` change. S4 removes the silent
drop by defaulting the path, and S5 labels added matchers client-side, so the walkthrough symptoms
are closed without it; a first-class warnings field stays a decision.

### B3 — `Chart.yaml` `kubeVersion` when `cnpg.enabled`

Helm cannot express a conditional `kubeVersion`; raising the global floor to 1.29 would refuse plain
installs that work today. S8 documents the operator-path floor instead. Raising the floor is a chart
**minor** (UPGRADING.md section) — decide separately.

## 4. Integration order and the final gate on `fix/walkthrough-2026-09-14`

1. All eight slices are cut from the same commit and may merge in any order — file sets are disjoint
   (S3 owns `web/src/visual/components/*` **except** `PipelineNode.tsx`/`CanvasPane.tsx`, which S2
   owns; `handlers.ts` is S6's alone; `CHANGELOG.md`/`docs/project-status.md`/`site/**` are S8's
   alone).
2. Behavioural seams to re-check once merged: S2's foreground flip changes computed colours S3's
   specs never assert (fine); S6's `ListPipelines` mock now filters on `needsUpgrade` — S3's graph
   view/upgrade specs pass `needsUpgrade: false`, unaffected; S4's Stage 1/2 gate on `CommitWizard`
   and S5's runner — the runner already disables Create while diagnostics are non-empty, so a
   refusal only surfaces for a client that bypasses the review.
3. Final gate, from the repo root:

```
make lint
make test
make web-ci
cd web && pnpm exec playwright test && cd ..
git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets
make docs && make check-docs-drift && make check-docs-version
```

4. Release: none of this needs a chart change; it ships in the next app tag with a rebuilt
   `internal/spa/dist` per the release conventions (never in these slices).

## 5. Risks

- **S2 redefines Tailwind's `--color-zinc-100/200/300` for light.** It is deliberate and scoped to
  text (the theme test bans every other raw zinc utility), but any future `bg-zinc-200` would come
  out dark in light mode — `RAW_ZINC_PATTERN` is the guard; the S2 comment in `index.css` must say
  so.
- **S3's org fallback in `GraphViewPage`** may switch the selected org when a shared URL is opened —
  the same behaviour `VisualBuilderPage` already has; the spec asserts the request body, not the
  switcher.
- **S4 adds a gate to a write that had none.** Any wizard whose output fails Stage 1/2 now cannot
  commit; every catalog wizard's goldens already pass Stage 1 (`wizard_test.go` per package), so
  nothing regresses, and the runner never enables Create on diagnostics.
- **S6's mock change** to `ListPipelines` is additive but global; a spec seeding visual pipelines
  with an old `schema_version` and no `schema` will now see the badge — none does today (grep
  `schema_version:` under `web/tests/specs` before merging).
- **S7's SQL** changes an `ON CONFLICT` expression; `make generate` must be idempotent and the
  sweeper/lifecycle specs (`sweeper_test.go`) must stay green — they do not assert on `name`.
- **F5b stays open.** The changelog must say so; the ledger carries the reproduction recipe.
