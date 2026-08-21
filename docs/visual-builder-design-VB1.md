# Shepherd — Visual Pipeline Builder (VB-1): Complete Design & Implementation Plan

A Node-RED-style visual editor for building Alloy pipelines: drag components onto a canvas, wire them by typed ports, configure them in an inspector, validate continuously, generate deterministic Alloy config through the existing three-stage gate, and simulate the pipeline at three fidelity tiers before a single collector sees it. Visual pipelines are ordinary Shepherd pipelines (`source='visual'`) — they inherit matchers, revisions, the merge engine, RBAC, and audit for free.

**Standing rulings carried in from prior design work (not re-litigated here):**
- Graph JSON is the single source of truth for visual pipelines; generated text is an artifact. **No bidirectional editing** — hand-written pipelines get a read-only "view as graph"; visual pipelines are edited only on the canvas. One-way, forever.
- Block definitions come from the **generated schema artifact** (`internal/schema/artifacts/alloy-v<X>.json`, produced by `tools/alloy-schema-gen` running inside a pinned checkout of grafana/alloy, walking `component.Register` registrations + the `internal/component/metadata` accepts/exports data), deep-merged with a hand-maintained **overlay** (docs one-liners, palette categories, enum values, icons). Experimental components included, badged, gated.
- Everything generated flows through the existing validation gate (stages 1–3) before save/enable. The builder adds *earlier* layers, it never bypasses later ones.

---

# Part 1 — Data model

## 1.1 The graph document (stored in `pipelines.wizard_state`, `source='visual'`)

```jsonc
{
  "kind": "alloy-graph/v1",
  "schema_version": "alloy-v1.12.2",        // the schema artifact this graph was authored against
  "nodes": [
    {
      "id": "n_7f3a",                        // stable nanoid; never reused
      "component": "discovery.relabel",      // schema key
      "label": "annotated",                  // becomes the Alloy block label; user-editable, sanitized, unique per component type
      "position": { "x": 320, "y": 180 },
      "props": {                             // attribute values + nested blocks, shaped by the schema
        "rule": [ { "action": "keep", "source_labels": ["__meta_kubernetes_pod_annotation_prometheus_io_scrape"], "regex": "true" } ]
      },
      "disabled": false,                     // node-level bypass (see 4.6)
      "notes": ""                            // free-text annotation shown on hover
    }
  ],
  "edges": [
    { "id": "e_01", "from": { "node": "n_9c21", "port": "output" }, "to": { "node": "n_7f3a", "port": "targets" } }
  ],
  "bindings": [                              // config-node property bindings (NOT wires — see 3.4)
    { "node": "n_ab77", "prop": "endpoint[0].url", "ref": { "node": "n_secret1", "export": "data", "expr": "convert.nonsensitive(%s[\"url\"]) + \"/api/v1/push\"" } }
  ],
  "viewport": { "x": 0, "y": 0, "zoom": 1 },
  "meta": { "created_with": "shepherd-vb/1.0" }
}
```

Migration `000N`: no schema change needed (`wizard_state` JSONB exists); add `'visual'` to the `pipelines.source` CHECK. Revisions already snapshot `wizard_state` → **graph history and diff come free** from the existing revision system.

## 1.2 API surface (new endpoints, existing envelope/RBAC)

```
POST /api/orgs/{org}/visual/render     [orgadmin]  graph → { content, diagnostics[], node_map }   // node_map: alloy block ↔ node id, for error mapping
POST /api/orgs/{org}/visual/validate   [orgadmin]  graph → diagnostics[] (layers L2+L3, node-addressed)
GET  /api/schema/{version}             [reader]    the schema artifact + overlay, ETag-cached      // the palette's data source
POST /api/orgs/{org}/simulate/relabel  [orgadmin]  { rules|graph_slice, sample_targets } → per-target trace   (Tier 2, §6.3)
POST /api/orgs/{org}/simulate/logs     [orgadmin]  { stages|graph_slice, sample_lines } → per-line trace      (Tier 2, §6.3)
POST /api/orgs/{org}/simulate/runs     [orgadmin]  graph → { run_id }                              (Tier 3, §6.4)
GET  /api/orgs/{org}/simulate/runs/{id}            → status | results (captured series/lines, component health)
```

Pipeline save/enable endpoints are UNCHANGED — the client calls `render`, then saves the returned content as a normal pipeline with the graph in `wizard_state`. The server re-renders server-side on save and refuses if client-sent content ≠ server render (tamper/desync guard).

---

# Part 2 — UI design (exact, per the §13 design system)

## 2.0 Package manifest (exact — pnpm, pinned in package.json, additions only)

| Package | Version pin | Role | Why this one / rules |
|---|---|---|---|
| `@xyflow/react` | `^12.8` | The canvas (React Flow v12) | The de-facto standard; v12 package name (NOT the legacy `reactflow`). Use: custom node/edge components, `isValidConnection`, `onDrop`, minimap, controls. Do NOT import its stock CSS theme wholesale — import `@xyflow/react/dist/base.css` only and style via our tokens. |
| `@dagrejs/dagre` | `^1.1` | User-invoked auto-layout | Maintained dagre fork; left-to-right ranking, 60/40 node/rank separation. elkjs rejected: 10× bundle for layout quality we don't need. Lazy-loaded (`import()` on first Layout click). |
| `zustand` | `^5` | Graph document store | One store per open editor; the graph doc (§1.1) is the state shape verbatim — no derived duplication. |
| `zundo` | `^2.3` | Undo/redo middleware over zustand | `partialize` to `{nodes, edges, bindings}` ONLY (viewport/selection excluded per §2.3); `limit: 100`; `equality: deepEqual` on the partialized slice to collapse no-op sets. |
| `nanoid` | `^5` | Node/edge ids | `nanoid(8)` with prefix `n_`/`e_`. Never `Math.random` (determinism grep applies). |
| `idb-keyval` | `^6` | Draft autosave + schema-artifact cache | Keys: `vb:draft:<pipelineId>`, `vb:schema:<version>`. Tiny (<1KB); a full Dexie is unjustified. |
| `fuse.js` | `^7` | Palette fuzzy search | Keys: component name, overlay keywords, category; threshold 0.35. Lazy-loaded with the palette. |
| `fast-deep-equal` | `^3` | zundo equality + graph-diff leaf compare | |
| *(already present, reused — add nothing)* | | `zod` (inspector schemas generated at runtime from the schema artifact), `react-hook-form`, CodeMirror 6 stack (Code tab), TanStack Query (render/validate/simulate calls), shadcn/ui (inspector widgets, Sheet, drawer tabs) | |

**Forbidden:** any second graph lib, `reactflow` (v11 legacy), `elkjs`, `immer` (zustand set-fns suffice; immer+zundo interplay causes history bloat), any wasm bridge for codegen (the two-implementation + corpus decision in §4.2 stands). Bundle rule: the entire visual builder is a lazy route chunk (`/pipelines/visual/*` code-split); `pnpm build`'s chunk report must show it separate from the main bundle. There is no automated size gate — review the chunk report when touching the route split.

## 2.1 Layout

Route `/pipelines/visual/new` and `/pipelines/:id/visual` (only for `source='visual'`; other sources get the read-only graph view at `/pipelines/:id/graph`). Full-height, four regions:

```
┌────────────┬──────────────────────────────────────────┬──────────────┐
│ PALETTE    │ CANVAS (React Flow)                      │ INSPECTOR    │
│ w-64       │ flex-1                                   │ w-[360px]    │
│ border-r   │                                          │ border-l     │
│            │                                          │              │
├────────────┴──────────────────────────────────────────┴──────────────┤
│ BOTTOM DRAWER (h-64, collapsible): tabs Problems | Code | Simulate   │
└──────────────────────────────────────────────────────────────────────┘
Toolbar row above all (h-11): name input · matcher summary chip (opens the standard matcher builder in a Sheet)
· validation status live-region · Simulate ▾ · Save · Save & Enable
```

## 2.2 Palette (left)

- Search input (fuzzy over component name + overlay keywords), then category accordions from the overlay: **Sources** (discovery.*, loki.source.*, otelcol.receiver.*, prometheus.exporter.*), **Transform** (discovery.relabel, prometheus.relabel, loki.process, otelcol.processor.*), **Destinations** (prometheus.remote_write, loki.write, otelcol.exporter.*), **Config** (remote.kubernetes.secret, local.file — the non-wire nodes, §3.4), **Advanced** (everything else).
- Palette item: component icon (overlay), short name, stability badge — GA nothing, `preview` sky, `experimental` amber. Experimental items HIDDEN unless the org toggle `allow_experimental_components` is on AND the user is org admin (server enforces at render: an experimental node with the toggle off is an L2 error, not just hidden UI).
- Drag onto canvas (React Flow `onDrop`) or click-to-place. Dragging onto an EDGE splices the node in when port types are compatible on both sides (the Node-RED insert gesture) — otherwise the edge rejects with a shake animation.
- Bottom of palette: "Destination presets" — the org's `destinations` rendered as one-drag composite nodes (drops the write component pre-bound to the destination's secret, §3.4).

## 2.3 Nodes on canvas

Anatomy (custom React Flow node, one component for all types):

```
┌─────────────────────────────┐
│ ⣿ icon  prometheus.scrape   │   header: category-tinted left border (4px), mono component name
│    "app"                    │   label row: editable inline (double-click), shows sanitized preview if it differs
│ ○ targets      forward_to ●─│   ports: inputs left, outputs right; port dot color = wire TYPE (§3.1)
│ ⚠ 2            scrape 60s   │   footer: problem badge (count, click → Problems filtered) · key-prop summary (overlay-defined)
└─────────────────────────────┘
```

- Node width fixed 240px; height auto by port count. Selected: indigo ring. Disabled (§4.6): 50% opacity + dashed border. Error: red left border overrides category tint.
- Multi-select (shift-drag / shift-click), drag-move with 8px grid snap, copy/paste (⌘C/⌘V — pasted nodes get new ids, labels suffixed `_copy`), delete (⌫ with edges cascade), ⌘Z/⇧⌘Z undo/redo (zustand + zundo, 100-step history over the graph document ONLY — viewport moves excluded), ⌘A, arrow-key nudge. Auto-layout button (dagre, left-to-right) for pasted/imported messes — never automatic, always user-invoked.
- Minimap bottom-right; zoom 0.25–2; space-drag pan; `?` opens the shortcut sheet.

## 2.4 Inspector (right)

Selected node → schema-driven property form (the SAME form-generation machinery as the wizard framework — react-hook-form + zod generated from the schema's attribute types):
- Sections: **Label** (with live sanitization preview + uniqueness error), **Attributes** (required first, then optional collapsed under "More"), **Blocks** (nested blocks as repeatable card lists — `rule`, `endpoint`, `stage.*` — add/remove/reorder with drag handles; reorder matters and is preserved into codegen), **Bindings** (§3.4), **Notes**, **Danger** (disable toggle, delete).
- Field widgets by type: string→Input, bool→Switch, duration→Input with unit hint + the standard suggestions, enum→Select from schema `values`, list(string)→chips, map→key/value rows, secret-typed→ONLY a binding picker (free-text secret entry is refused at the UI — secrets travel by reference, never by value; L2 enforces).
- Every field label carries the overlay doc one-liner as a Tooltip; unknown/overlay-missing docs show the component docs deep-link (`grafana.com/docs/alloy/<ver>/reference/components/<name>/`).
- Nothing selected → pipeline-level inspector: name, matchers (summary + edit), schema version + upgrade banner (§5.3), stats (nodes/edges/problems).

## 2.5 Bottom drawer

- **Problems**: the unified diagnostics list (all layers §4), each row: severity dot · layer chip (L1–L4) · node label (click = select+center node) · message. Filterable by node via the node badges.
- **Code**: read-only CodeMirror (the existing Alloy mode) showing the LIVE render — re-rendered client-side ≤300ms debounced from the deterministic client generator (§4.5), server-verified on demand ("Verify render" button diffs client vs server output; mismatch is a bug banner + telemetry event). Node hover on canvas highlights its lines; cursor in code highlights the node (via `node_map`).
- **Simulate**: §6.5.

---

# Part 3 — Blocks: definition, ports, linking rules

## 3.1 Port type system (from the metadata package, via the schema artifact)

Wire types (closed set, each with a fixed dot color): `targets` (violet) — `[]discovery.Target`; `prom.metrics` (orange) — Prometheus appendable/receiver; `loki.logs` (green) — `loki.LogsReceiver`; `otel.traces` / `otel.metrics` / `otel.logs` (sky, three distinct dots) — OTel consumers; `pyroscope.profiles` (rose). The schema artifact records per component: `inputs: [{prop, type, cardinality}]` (input = a list-typed attribute that accepts references, e.g. `forward_to`, `targets`) and `outputs: [{export, type}]` (e.g. `output: targets`, `receiver: prom.metrics`).

## 3.2 Linking rules (enforced at connect-time — invalid wires are impossible, not flagged later)

| Rule | Behavior |
|---|---|
| Type match | `from.output.type == to.input.type` exactly. Incompatible target port dots grey out the moment a drag starts from a source port (React Flow `isValidConnection`). |
| Fan-out | An output may feed any number of inputs (Alloy exports are shareable). |
| Fan-in | An input's cardinality comes from the schema: list-typed inputs (`forward_to`, `targets` via concat) accept MULTIPLE incoming wires — codegen emits the list/`array.concat`; scalar reference props accept exactly one (second wire replaces with a confirm toast). |
| No cycles | On connect, DFS from `to` — reaching `from` rejects with "Alloy graphs are acyclic" toast. O(V+E) per connect, fine at canvas scale (≤ ~200 nodes soft cap with a perf warning at 100). |
| No self-loops, no duplicate identical edges | Rejected silently (no-op). |
| OTel signal discipline | The three otel types are distinct wires; `otelcol.receiver.otlp` exposes three separate output ports; processors expose matching in/out per signal they support (from metadata). |

## 3.3 Dangling-port policy (what "incomplete" means)

- A **required input** with zero wires → L1 error ("`prometheus.scrape "app"` has no targets").
- An **output** with zero wires on a non-terminal node → L1 warning ("data goes nowhere") — warning not error, because prometheus.exporter.self legitimately exports without local consumption only when something scrapes it; the overlay marks which components are "terminal-ok".
- A graph with zero Destination-category nodes → L1 error (a pipeline that ships nothing is a mistake by definition here; the escape hatch is the text editor).

## 3.4 Config nodes & bindings (the non-wire pattern)

`remote.kubernetes.secret` and `local.file` are **config nodes**: they render on canvas (Config category tint, no stream ports) but connect via **bindings**, not wires — a secret-typed or expression-capable property in the inspector opens a picker listing on-canvas config nodes (+ "add secret node" shortcut); selecting one records a `bindings[]` entry with the expression template. The rendered dependency (`remote.kubernetes.secret.dest.data["url"]`) creates the Alloy-level edge; on canvas the binding draws as a thin dashed grey connector (non-interactive, toggleable via View menu) so the dependency is visible without cluttering the stream topology. Destination presets (§2.2) are exactly: write component + secret config node + bindings, instantiated from the org destination record — the same rendering path the app-observability wizard templates use, now reified as graph fragments.

---

# Part 4 — Validation & code generation

## 4.1 Validation layers (each strictly earlier/cheaper than the next; all feed ONE Problems list)

| Layer | Where | When | Catches |
|---|---|---|---|
| **L1 graph rules** | client, pure functions over the graph doc | every mutation, sync | type/cardinality/cycle violations (mostly *prevented* at connect), dangling required ports, missing destination, label collisions, required props empty, experimental-gate, secret-by-value attempts |
| **L2 render+syntax** | server `POST /visual/validate` (client render used optimistically) | 800ms idle debounce | anything the generator produces that the Alloy parser rejects; binding expression errors; schema/overlay drift |
| **L3 `alloy validate`** | server, same endpoint (stage-2 machinery reused) | same call as L2 | unknown components at the PINNED alloy version, semantic attr errors, stability-level violations |
| **L4 merge dry-run** | server, on save/enable (existing stage 3, unchanged) | save/enable | collisions and breakage against every affected collector's full merge |

Server diagnostics come back **node-addressed** via `node_map` (the renderer records `alloy line range → node id`); a stage-3 failure dialog reuses the existing per-collector accordion. The invariant: **the visual layer adds L1; L2–L4 are the existing gate re-hosted — no parallel validation stack.**

## 4.2 Codegen: deterministic, total, boring

Input: graph doc. Output: Alloy text + `node_map`. Algorithm (identical client and server — implemented ONCE in Go, compiled to the client via... no: implemented twice, Go (authoritative) + TS (preview), kept honest by the golden corpus in §7 and the save-time render-equality check in §1.2 — the pragmatic call over wasm):

1. **Sanitize + uniquify labels**: `sanitize(label)` (existing merge-engine rules); collision within the same component type → L1 error (never auto-suffix at render time — determinism over convenience).
2. **Topological sort** (Kahn) over stream edges + binding edges combined; tie-break by `(category order: Config, Sources, Transform, Destinations, Advanced) then component name then label` — byte-stable output for identical graphs regardless of insertion order.
3. **Emit per node**: header `component "label" {`; attributes in SCHEMA ORDER (not props-map order); wired inputs render as reference lists (`targets = [discovery.relabel.annotated.output]`, multi-fan-in concatenated in edge-creation order — order recorded on the edge, reorderable in the inspector); bindings render their expression templates; nested blocks in inspector order; disabled nodes and their edges are SKIPPED entirely with a `// node <label> disabled` comment marker.
4. **Value serialization** per schema type: strings escaped/quoted, durations bare-quoted (`"60s"`), maps/lists Alloy-literal, secrets impossible by construction (§2.4).
5. Header comment: `// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); graph revision <n>, schema alloy-v<X>`.
6. The result is a **standalone pipeline snippet** — declare-wrapping stays the merge engine's job, unchanged.

## 4.3 Read-only graph view for text pipelines

`/pipelines/:id/graph` (any source): server parses the content with the existing syntax parser, extracts component blocks + reference expressions into a best-effort graph (unmapped expressions render as a grey "expr" chip on the edge; anything unparseable into topology renders the node with an "opaque" badge). Dagre-laid-out, zero editing affordances, one button: "Recreate as visual pipeline" → scaffolds a NEW visual pipeline from the extraction for the user to complete (explicitly lossy, confirm dialog says so). This is the only text→graph path and it is a copy, never a link.

## 4.4 Persistence & lifecycle

Autosave: local draft (IndexedDB, keyed by pipeline id) every mutation; explicit **Save** writes the pipeline (render → gate → revision) — the standard model, drafts are only crash insurance with a "restore draft?" banner. Save & Enable adds the enable step (L4). Revisions: restoring an old revision restores its `wizard_state` graph; the diff view for visual revisions shows BOTH the text diff (existing) and a graph diff tab (nodes/edges added/removed/changed, computed structurally — colored badges on a merged canvas render).

## 4.6 Node disable

The Node-RED muscle-memory feature: per-node bypass without deletion. Disabled node = excluded from codegen; L1 re-evaluates the resulting graph (disabling a mid-chain relabel leaves its consumers dangling → immediate L1 errors show the blast radius before you save). Edge-through behavior is deliberately NOT provided (auto-splicing around a disabled transform changes semantics silently — the user re-wires explicitly).

---

# Part 5 — Block lifecycle: how definitions update

## 5.1 The generation pipeline — exact mechanics (where the blocks come from)

**Source of truth:** the `grafana/alloy` repository at the FLEET-PINNED tag — single-sourced from `deploy/versions.env` `ALLOY_VERSION` (the V4-12 file), so the schema version and the validate-binary version can never drift apart: they are one variable. Everything needed lives under the repo's `internal/` tree, which is why generation must run **inside** the checkout (Go forbids importing `internal/` from outside):

| What | Where in grafana/alloy | Extraction |
|---|---|---|
| Complete component list (GA + preview + experimental) | `internal/component/all` (blank-import registers everything) + the registry in `internal/component` | import `_ ".../internal/component/all"`, iterate the registrations |
| Name, stability | `component.Registration{Name, Stability}` | direct |
| Attributes & nested blocks (names, required/optional, types) | each registration's `Args` struct via `alloy:"name,attr[,optional]"` / `alloy:"name,block[,optional]"` tags | `reflect` walk of the Args type, recursive into block-typed fields; Go type → schema type mapping (string, secret [`alloytypes.Secret`/`OptionalSecret`], bool, number, duration [`time.Duration`], list, map, capsule) |
| Port types (accepts/exports) | `internal/component/metadata` — `metadata.ForComponent(name)` → `{Accepts, Exports []DataType}` (Targets, LokiLogs, PrometheusMetricsReceiver, OTel consumers, PyroscopeProfilesReceiver) | DataType → our wire-type ids (§3.1); input PROP names identified as the list-of-capsule reference-accepting Args fields (`forward_to`, `targets`, …); output export names from the Exports struct fields |
| Default snippet per component | `Args` implementing `syntax.Defaulter` (`SetToDefault()`), re-marshalled with `github.com/grafana/alloy/syntax.Marshal` | `reflect.New(argsType)` → SetToDefault → Marshal → the palette drop template |

**The runner** — `tools/alloy-schema-gen/{run.sh, extract.go, README.md}`. `run.sh` (invoked as `make schema`; needs network to the git mirror — a CI job concern only, never part of app builds). *The listing below is the historical design sketch; the current interface is the Makefile's `schema`/`schema-verify` targets, which drive `run.sh` via `SCHEMA_OUT_DIR`/`SKIP_RECONCILE` and write into `internal/schema/artifacts/`:*

```bash
#!/usr/bin/env bash
set -euo pipefail
source deploy/versions.env                                   # ALLOY_VERSION=v1.12.2
SRC=$(mktemp -d)
git clone --depth 1 --branch "${ALLOY_VERSION}" \
  "${ALLOY_REPO:-https://github.com/grafana/alloy}" "$SRC"    # CI overrides ALLOY_REPO with an internal mirror if applicable
mkdir -p "$SRC/cmd/shepherd-schema-dump"
cp tools/alloy-schema-gen/extract.go "$SRC/cmd/shepherd-schema-dump/main.go"
( cd "$SRC" && go run ./cmd/shepherd-schema-dump ) | jq -S . > "schema/alloy-${ALLOY_VERSION}.json"   # jq -S: key-sorted, stable diffs
rm -rf "$SRC"
```

`extract.go` (~300 lines): registers all components via the blank import, iterates the registry, performs the extractions above, emits the artifact exactly as `GET /api/schema` serves it — components keyed by name: `{stability, doc:"", attributes:[{name,type,required}], blocks:[…recursive], inputs:[{prop,type,cardinality}], outputs:[{export,type}], default_snippet}` (`doc` empty; the overlay fills it). Components whose Args defy reflection (rare capsule-only oddities) are emitted `"opaque": true` — palette Advanced category, inspector reduced to the raw default snippet — and the extractor logs them for overlay special-casing. The extractor NEVER silently drops a registered component: the artifact header's `components_total` must equal the registry count (self-checked, non-zero exit on mismatch).

**Committed outputs & CI discipline:** `internal/schema/artifacts/alloy-v<X>.json` is COMMITTED (app builds stay hermetic — no network, no clone at build time) alongside `internal/schema/artifacts/overlay.json`. CI job `make schema-verify` (weekly scheduled workflow; run it on Alloy-bump PRs): regenerate and diff against the committed artifact — drift fails with the diff attached. An Alloy version bump is therefore ONE reviewable PR: `versions.env` change + regenerated artifact + overlay entries for new components + the fleet stage-3 revalidation sweep. Overlay guards: overlay keys must exist in the artifact (hard fail — overlay can't rot); new artifact components lacking an overlay `category` land in Advanced with a CI **warning** naming them (a warning, not a failure — a new experimental component must never block the version bump; categorization is tracked follow-up).

The **overlay** (`internal/schema/artifacts/overlay.json`) deep-merges over the artifact: docs one-liners, palette categories, icons, enum value lists, terminal-ok flags, port display order, discovery-stub mappings (§6.4), and rename migrations (§5.3).

## 5.2 Serving & client consumption

`GET /api/schema/{version}` serves artifact+overlay merged, `ETag: <content hash>`, immutable per version. The client caches per version in IndexedDB. The palette, inspector forms, port system, L1 rules, and both codegens all derive from this ONE payload — there is no second component list anywhere (the text editor's `alloySchema.ts` is REPLACED by a thin adapter over the same payload in this milestone, retiring the hand-curated file and its drift test).

## 5.3 Graph upgrades across Alloy versions (the hard part, designed honestly)

Every graph records `schema_version`. When the fleet's pinned Alloy (and thus the served schema) moves:

1. **Nothing changes automatically.** Existing pipelines keep serving their generated text; text doesn't rot when schemas move.
2. Opening a graph whose `schema_version` < current → **upgrade banner** in the pipeline inspector: "Authored against alloy-v1.12.2; current is v1.13.0 — Review upgrade".
3. **Upgrade review** = a structural diff of the graph against the new schema, computed server-side (`POST /visual/upgrade-check`): per node — component removed (→ node marked ERROR "no longer exists", codegen refused until resolved), attribute removed (→ prop flagged, value shown, one-click discard), attribute added-required (→ error, inspector highlights), enum value removed, stability downgraded/upgraded (badge change), port-type changes (edges re-validated; a now-invalid wire goes red with the rule that broke it). The user resolves each item; **Accept upgrade** stamps the new `schema_version` and creates a revision. Bulk view: org admins get `/pipelines?needs_upgrade=true` filter + a count on the Overview.
4. **Renames/migrations** the diff can't infer: the overlay carries an optional `migrations` map (`"prometheus.old_name" → {to, prop_renames}`) hand-added when release notes reveal them; the upgrade review applies these as pre-resolved suggestions.
5. Guardrail interaction with the fleet: the L3 validator runs the CURRENT pinned binary — so an un-upgraded graph whose generated text still validates keeps working, and one that wouldn't is caught at the next save, never silently at serve time (serve-side content is frozen text; the existing stage-3-on-Alloy-bump sweep from the schema-gen design remains the fleet-wide safety net).

---

# Part 6 — Simulation (three tiers, honest about fidelity)

The tiers answer three different questions and are labeled as such in the UI — conflating them is the failure mode this design refuses.

## 6.1 Tier map

| Tier | Question answered | Where | Latency | Fidelity limits |
|---|---|---|---|---|
| **S1 — Flow check** | "Is this graph well-formed and where does data flow?" | client, free | instant | structure only |
| **S2 — Stage trace** | "What would THIS rule/stage do to THIS sample data?" | server, deterministic | <1s | per-stage subset (below); no timing, no discovery |
| **S3 — Sandbox run** | "What actually comes out the end when a real Alloy runs this?" | ephemeral Alloy, capture harness | 20–60s | discovery stubbed; synthetic inputs; real components otherwise |

## 6.2 S1 — Flow check (client)

Beyond L1: an **animated flow overlay** (toolbar toggle) — dots travel the wires colored by type, sourced only from nodes whose category is Sources, halting at breaks; a broken chain is visually obvious in a way a problem list isn't. Plus per-wire tooltips: "targets (list) · 2 producers → concat".

## 6.3 S2 — Deterministic stage trace (server)

The genuinely-simulable subset, implemented by embedding the REAL upstream evaluation code — never reimplementations:

- **Relabel trace** (`/simulate/relabel`): rules from a selected `discovery.relabel`/`prometheus.relabel` node (or the whole chain of relabel nodes upstream of a selection) + sample targets → per-target, per-rule trace using `github.com/prometheus/prometheus/model/relabel` directly. Response: for each input target, each rule's effect (kept/dropped/labels before→after), final surviving set. Sample sources, in the picker: (a) built-in fixtures (curated k8s pod-discovery label sets: annotated pod, un-annotated, different namespace — shipped with Shepherd), (b) paste JSON, (c) **live**: "fetch real label sets" — a bounded query against the org's Prometheus destination (`/api/v1/targets`-shaped or a series query, config-gated `simulate.live_samples.enabled`, off by default) so users trace against their actual pods.
- **Log stage trace** (`/simulate/logs`): the loki.process stage subset — `stage.json`, `stage.logfmt`, `stage.regex`, `stage.labels`, `stage.label_drop`, `stage.drop`, `stage.multiline`, `stage.template` — evaluated via the vendored grafana/loki stage packages where importable; any stage outside the subset renders in the trace as an explicit "not simulated — passes through unchanged in this preview" step (never silently skipped). Input: pasted lines or built-in fixtures (JSON app log, logfmt, multiline stacktrace).
- **Metric-name filter preview**: a keep/drop regex prop anywhere gets an inline "test against names" mini-widget (client-side, RE2-compatible check server-side on demand).

S2 results render in the drawer's Simulate tab as a **pipeline-of-cards trace**: input → each stage a card (rule text, effect, diff-highlighted labels/line) → output; surviving/dropped counts summarized. Clicking a stage card selects its node.

## 6.4 S3 — Sandbox run (the real thing, contained)

**Simulator service** (`shepherd-simulator`): a small Deployment (dev: compose service) colocated with Shepherd, comprising the pinned Alloy binary + a capture harness + a synthetic source suite. Shepherd drives it over an internal-only HTTP API; runs are ephemeral, queued (1–2 concurrent), TTL 5 minutes, results retained 1 hour.

**Run pipeline:**
1. Shepherd renders the graph, then applies the **simulation transform** — a graph-level rewrite (this is WHY simulation is graph-native and near-impossible to do safely on raw text):
   - Every **Destination** node's endpoint bindings/URLs are rewritten to the harness's capture receivers (a real `prometheus.remote_write` receiver, a Loki push receiver, an OTLP receiver — the harness records everything and serves it back). The capture endpoints are unauthenticated inside the pod.
   - **The transformed graph is CONSTRUCTED, not filtered** (rule K). Every surviving node's props are built fresh, copying in only the block-qualified attribute paths the overlay's `sim_keep` names for that exact component, plus the paths the transform wrote itself. This replaced a type-driven sweep that deleted attributes declared `secret`: only 338 of the artifact's 6482 declared attribute paths are typed `secret`, so a credential declared `string` (measured: 516 of 716 credential-named paths are not typed `secret`) reached the sandbox verbatim and passed real `alloy validate`. Type is not a boundary, and neither is name — the allowlist unit is the path. Expressions (`{"$expr": ...}`) and `GraphBinding`s are refused everywhere, which is what removes `sys.env("EXFIL_URL")` structurally rather than by pattern. A component the overlay dispositions `sim_unsupported` fails the run closed.
   - Every **discovery.*** node is replaced by a `discovery.static`-equivalent emitting the harness's synthetic targets carrying the STANDARD meta-label set that discovery would produce (the fixture library from 6.3a, so relabel chains behave identically). The mapping table `discovery component → stub label set` lives in the overlay; unmapped discovery components fail the run with "cannot stub <x> — use S2 for its downstream rules".
   - `loki.source.kubernetes`/file sources → the harness's synthetic log emitter (writes the fixture lines, tailed via loki.source.file inside the sandbox).
   - `remote.kubernetes.secret` → stub component returning harness-known dummy values (URLs already rewritten; other secret uses get `"simulated"`).
2. The transformed config passes stages 1–2 (it must be valid too), then the simulator: `alloy run` with `--storage.path` tmpfs, run for `duration` (default 30s, max 120), scraping the synthetic exporter (which exposes a configurable set of series: counters advancing, a gauge, histogram — enough to exercise scrape+relabel+write for real).
3. Results: captured **series** (name, labels, sample count) and **log lines** (labels, line) at the capture receivers; **component health** from the sandbox Alloy's own API (`/api/v0/web/components` — every component's health, evaluation errors); the sandbox's stderr tail. Failures (component unhealthy, nothing captured) are the SIGNAL — that's the point.
4. UI: Simulate ▾ → "Sandbox run (30s)…" → progress states (validating → transforming → running 30s countdown → collecting) → results view: three tabs (Metrics captured — a filterable series table; Logs captured; Component health — per-node health badges ALSO painted onto the canvas nodes for the duration of viewing, the single best debugging affordance in the whole feature). "What was rewritten" disclosure lists every transform applied, keeping fidelity honest.

**Security containment:** the software boundary is the reconstruction above, not a sweep: there is no code path that copies an unclassified authored value into the sandbox config, and `Transform` re-asserts that before returning (see the post-conditions in `docs/proofs/transform-secret-drop.md`). Every component the shipped artifact declares must resolve to exactly one S3 disposition — `discovery_stub`, `sim_destination`, `sim_keep`, `sim_secret_source`, `sim_unsupported` — or `make schema-verify` fails, so an Alloy bump cannot widen the surface silently. The keep lists are hand-maintained and are therefore the place human judgement now lives; the point of the model is that it is a BOUNDED judgement ("are these ~50 components' lists right?") where every error fails safe, rather than the unbounded one ("did we spot every credential among 6482 attribute paths?") that was demonstrably lost. **A path is not the whole story for a `map`-typed attribute**, and the round-2 review measured the gap: `internal/schema`'s name guard reads only the segments the ARTIFACT declares, so `external_labels` is one guarded segment while `external_labels.<whatever the user typed>` is two, and thirteen kept paths in the shipped overlay are declared `map`. Rule K's `constrainKeys` re-runs the SAME predicate (`schema.IsCredentialName`, exported so the two cannot drift) over every user-chosen key inside a kept value at any depth, and over the label names in a target set — so every segment of the effective path is guarded, the build-time half and the runtime half meeting in the middle. `prometheus.scrape`'s `params` (the canonical query-string credential mechanism, excluded for exactly the reason `http_headers` already was) and `metrics_path` (which takes a query string and is the attribute form of the `__metrics_path__` meta label the class already removes) were dropped from the keep list rather than guarded. Red and green proof for all of it: `docs/proofs/transform-secret-drop.md`, "Round 2: the keys the path guard could not see".

**Reachability containment — the network, not the transform:** what the sandbox may READ is **not** bounded by the config-construction above, and this paragraph used to claim it was. It cannot be: a `discovery.relabel` rule retargets a scrape at RUNTIME (`target_label = "__address__"` with a `replacement` assembled from regex captures), so the address the sandbox dials need never appear as a literal in the rendered text and no analysis of that text can bound it. `rule.*.target_label` and `rule.*.replacement` are on the keep list because relabel is the single thing users most want to simulate, and deleting relabel is not on the table. **The reachability control is the sandbox's network:** the simulator sits alone on a Docker `internal: true` network with no route off it, verified by execution (see the paragraph below and `docs/proofs/simulator-containment.md` §P0). What the transform DOES do here is reduce reachability and keep the disclosure honest, and that is worth having: an address-bearing attribute is absent from the sandbox config because nothing kept it — `proxy_url`, `oauth2.token_url`, `otlphttp`'s per-signal `*_endpoint` siblings and the forty others are not "unmapped", they are not there; and a target set (`targets` on the twelve components whose target list really is a Prometheus label set), which must survive or the pipeline has nothing to scrape, carries the `target_set` value class, so rule K forces `__address__` to the harness's synthetic exporter and `__scheme__` to `http` unconditionally, drops the other request-steering meta labels, and drops a user-named label whose name is credential-shaped. Two components that LOOK like the other twelve in the artifact are not: `prometheus.exporter.blackbox` and `prometheus.exporter.snmp` declare `targets` as a `list` in the same way and carry the probe destination in an ordinary `address` key that forcing `__address__` never touched, so both are `sim_unsupported` and fail the run closed (round-2 HIGH). The simulator's `CheckEndpoints` is declared defence in depth on the same footing — deny-by-default over any-scheme URLs, bracketed IPv6, hosts inside DSNs and any *computed* expression — so a transform bug that left a NAMED production endpoint in the config fails loudly rather than writing to it. Neither is the reachability boundary. An earlier static rule that claimed to be (**P5**, `address_not_harness`) was **deleted**: it could not see the runtime retarget it was built for, and it refused five of six ordinary graphs over inert relabel label names. Proof, red and green: `docs/proofs/transform-address-closure.md`; the honest negative is asserted in `internal/simulate/transform_address_test.go` ("Transform: a runtime retarget is NOT contained by the transform") and its network half in `e2e/sandbox_egress_test.go`.

Around that: simulator pod: no service account token, NetworkPolicy egress limited to the harness itself (deny-all otherwise — a malicious graph can't exfiltrate; note this also means graphs scraping real cluster targets don't work in S3 by design), CPU/mem limits, non-root, the same distroless base. The graph arriving at the simulator has already passed the same org-admin RBAC as any pipeline save.

The network control is the ONLY thing that bounds what the sandbox can reach, so it is asserted by EXECUTION, never by parsing the compose file: `e2e/sandbox_egress_test.go` dials a hermetic canary from the simulator's own network namespace by name and by literal IP (both must fail), dials it from the ordinary network (must succeed — without that control the denials prove nothing), reads `Internal=true` back from the running Docker daemon, and submits the runtime-retarget graph itself to a live sandbox to show it is accepted by every software gate and denied by the network. Since the renderer defect that made every discovery-to-scrape wire unrunnable was fixed (finding M13, `docs/proofs/sandbox-sim-e2e.md` §1), that retarget spec also asserts the middle link by execution: the run reaches `completed` and the sandbox's own captured series come back labelled `instance=<canary-ip>:8080`, so the transform's non-containment is demonstrated rather than inferred. `.github/workflows/e2e.yml` runs them — on the merge queue and on any PR touching the containment surface — via `make e2e-sim`, whose first ginkgo pass is the egress-probe filter under `--fail-on-empty` so a renamed label cannot turn the job into a green no-op, and whose second pass runs the S3 run-lifecycle specs so the same job proves the sandbox delivers as well as contains. The mutation transcript — `internal: true` flipped off, probes re-run, flipped back — is in `docs/proofs/simulator-containment.md` §P0, and it records the one result that matters for reading these probes correctly: the by-name probe stays green without `internal: true`, because DNS absence is not routing denial. **This applies to docker-compose only.** The Helm chart now ships the simulator with a default-deny `NetworkPolicy` and `automountServiceAccountToken: false` (`deploy/helm/shepherd/templates/*-simulator.yaml`, asserted by `deploy/helm/chart_test.go`), but finding H5 stays open on the half that matters here: a rendered-template assertion is not a probe. Nothing dials from inside a real cluster's simulator Pod the way `P-deny-ip` dials from inside the compose one, so the Kubernetes posture is claimed, not exercised.

## 6.5 What simulation deliberately does NOT claim

No performance/cardinality prediction, no k8s discovery behavior (stubbed), no multi-collector merge simulation (that's stage 3's job), no production-secret usage anywhere. Each results view carries a one-line fidelity note naming its tier's limits.

**S3 runs a NARROWER pipeline than the one you authored.** Because the sandbox config is built from an allowlist, any attribute not on its component's keep list is absent — not just credentials and endpoints, but TLS material, proxy settings, header maps, filesystem paths, and every setting of a source the simulator cannot stub. The "What was rewritten" disclosure names every one of those drops, per node and per path, so the narrowing is visible rather than inferred. A simulation that behaves differently from production because a setting was dropped is a fidelity limit we state; a simulation that behaves the same because a real credential travelled with it is not a limit we are willing to trade for.

**S3 never scrapes what you named.** Every target set is re-pointed at the simulator's synthetic exporter over plain http, whatever address the graph carried and however that address arrived — typed literally, wired from a discovery node, or produced by a relabel rule copying a discovered meta label into `__address__`. The `target_address_forced` disclosure names each one. So S3 answers "does my scrape → relabel → write chain work", never "is that endpoint up", "does that host answer on that port" or "do my credentials work against it"; a graph whose real question is any of the latter cannot be answered here by design, and the run says so rather than reporting a plausible failure.

---

# Part 7 — Testing: full per-suite specification (unit → suite → e2e)

All standing rules apply: Ginkgo v2 + Gomega backend, Vitest/Playwright frontend, red-green proofs to `docs/proofs/`, assertion-depth rule, no sleeps, deterministic fixtures. Every test below is a Named test unless marked (infra).

## 7.1 Shared golden corpus (the backbone — build FIRST, milestone 3 gate)

`internal/visual/testdata/corpus/` — pairs `<name>.graph.json` + `<name>.golden.alloy`. Initial mandatory entries:

| Corpus entry | Exercises |
|---|---|
| `minimal-scrape` | 3 nodes: static targets → scrape → remote_write; single wires |
| `fanin-fanout` | 2 discovery sources → 1 relabel (fan-in concat, edge order) ; 1 scrape → 2 remote_writes (fan-out) |
| `nested-blocks` | remote_write with 2 `endpoint` blocks + tls_config + write_relabel_config; relabel with 5 ordered `rule` blocks |
| `bindings-secret` | secret config node + bindings into endpoint url/basic_auth (`convert.nonsensitive` expression rendering) |
| `logs-chain` | k8s log source → relabel → loki.process (json+labels+drop stages, ordered) → loki.write |
| `otel-three-signals` | otlp receiver → batch processor → otlp exporter, three distinct signal wires |
| `disabled-node` | mid-chain node disabled: skipped emission + `// node … disabled` marker + the downstream dangling state frozen as authored |
| `label-edgecases` | labels needing sanitization (`a-b`, unicode, leading digit) — rendered sanitized, golden proves the mapping |
| `kitchen-sink` | ≥ 15 nodes across all categories, every value type (string/bool/duration/list/map), notes, viewport junk (must not affect output) |

**Corpus sync (infra):** the TS generator consumes THE SAME files — `make generate` copies `internal/visual/testdata/corpus/` → `web/src/visual/__fixtures__/corpus/`; CI check: `diff -r` the two trees (drift fails naming files). One corpus, two consumers, mechanically identical.

## 7.2 Go unit — `internal/visual` (renderer & graph ops)

1. **"corpus renders byte-exact"** — table over every entry: render(graph) == golden, byte-for-byte. Red run: change attribute emission order → all entries fail.
2. **"render is permutation-invariant"** — for each entry: shuffle `nodes`/`edges` array order (seeded, 5 permutations) → output identical to golden. This is the determinism theorem. Red run: remove the topo tie-break → kitchen-sink diverges.
3. **"node_map is complete and correct"** — for each corpus entry: every emitted block's line range maps to exactly one node id; every non-disabled node appears exactly once; ranges are non-overlapping and cover all component blocks. Red run: off-by-one the range tracking → overlap assertion fails.
4. **"fan-in preserves edge order"** — reorder the two edges in `fanin-fanout` via the recorded order field → the reference list in output flips accordingly (both orders goldened).
5. **"sanitize collision is an error, never auto-suffixed"** — two nodes same component, labels `a-b`/`a_b` → render returns the L1-class error naming both nodes; output empty. Red run: add auto-suffixing → test fails on the error expectation.
6. **"disabled node emission"** — covered by corpus, plus: disabling a node whose consumer then has zero wires still RENDERS (rendering is total; the dangling state is L1's job, not the renderer's) — assert render succeeds with the dangling reference absent.
7. **"secret-typed prop with a literal value is refused"** — a graph doc hand-crafted with a raw string in a secret prop → render error `secret_by_value` naming node+prop. Red run: drop the check → renders a plaintext secret → fail.
8. **Graph diff unit suite** — structural diff over corpus pairs with known deltas: added node, removed edge, changed prop, reordered blocks → diff output matches expected delta JSON (goldened).

## 7.3 Go unit — `tools/alloy-schema-gen` + artifact invariants

Generation itself needs network + checkout, so it's a CI job, not a unit test. What IS unit-tested (hermetic, against the COMMITTED artifact):
1. **"artifact invariants hold"** — parse `schema/alloy-<pinned>.json`: `components_total` equals map size; every component has ≥1 output or is overlay-marked terminal/source-only… (assert the closed rule set: stability ∈ {ga, public-preview, experimental}; every input type ∈ the wire-type closed set; every `default_snippet` parses with the syntax parser — this last one is the strongest: N-hundred snippets through stage-1). Red run: hand-corrupt one snippet in a test copy → fail.
2. **"overlay guards"** — every overlay key exists in the artifact; every §3.1 wire type referenced by the overlay's port-order entries is valid; the discovery-stub map (§6.4) keys are all discovery.* components present in the artifact.
3. **(CI job, not a test)** `make schema-verify` — regenerate + diff, as specced in §5.1; its red proof is the Alloy-bump drill: bump versions.env locally without regenerating → schema-verify fails with the diff.

## 7.4 Go integration (Ginkgo, testcontainers PG) — endpoints & gate

1. **"render endpoint: content + node_map + diagnostics contract"** — POST a corpus graph → 200, content equals golden, node_map present. POST the collision graph → 422 with diagnostic `{layer:"L2", node_id, code:"label_collision"}`.
2. **"validate is node-addressed through stage 2"** — graph with a semantically-bad attr (bad duration on scrape) → the stage-2 (alloy binary, `Label("needs-alloy-binary")`) diagnostic carries the offending NODE id via node_map, not just a line. Red run: drop the line→node translation → diagnostic arrives node-less → fail.
3. **"save refuses client/server render divergence"** — save with tampered content (one byte changed vs server render of the same wizard_state) → 409 `render_mismatch`. Red run: remove the server-side re-render check → 200 → fail.
4. **"visual pipelines traverse the full existing gate"** — save+enable a corpus pipeline with matchers → stages 1–3 run (assert stage-3 executed via the affected-collector recompute), revision row created with the graph in wizard_state, audit row actor set. Then restore an older revision → wizard_state graph restored byte-identical.
5. **"upgrade-check classifies every diff class"** — synthetic schema pairs (v_old, v_new fixtures committed under `internal/visual/testdata/schemas/`) covering: component removed, attr removed, attr added-required, enum value removed, port-type changed, stability changed, overlay migration present → response items match expected class+node per fixture (goldened). Red run: remove the required-attr detection → that fixture's expectation fails.
6. **Simulation transform suite (pure functions — heavy coverage here, it's the safety boundary):** "destination endpoints rewritten to capture URLs" (every Destination-category node, all binding forms); "secrets dropped: the transformed graph contains zero binding refs to secret nodes and zero secret-typed literals" — grep-level assertion over the transformed render; "discovery nodes stubbed per the overlay map, unmapped discovery fails with `cannot_stub`"; "transform output passes stages 1–2". Red runs: skip the secret-drop → the zero-secrets assertion fails (this proof file is the security evidence for §6.4's containment claim).
7. **S2 relabel golden traces** — fixture target sets (the three built-ins) through fixture rule chains → committed trace JSONs, evaluated via the real `prometheus/model/relabel`. Expected outcome includes per-rule label before/after and final keep/drop per target. A trace diff after a prometheus dependency bump is a FINDING (upstream behavior change surfaced — the feature working). Red run: reverse the keep/drop mapping → fail.
8. **S2 log-stage traces** — same pattern over the supported stage subset; one fixture includes an UNSUPPORTED stage → the trace contains the explicit `not_simulated` step (never silently absent).
9. **Simulator run lifecycle (against the simulator container via testcontainers or the compose service)** — submit `minimal-scrape` → poll to `completed` within 90s → results contain ≥1 captured series whose name matches the synthetic exporter's counter AND carries a label added by no relabel (baseline), component-health all healthy. Failure path: submit a graph whose transform yields an unhealthy component (bad scrape interval accepted by validate but 0 targets) → run completes with the health tab showing the state, not an error 500.

## 7.5 Vitest (web) — L1, TS codegen, store

1. **L1 rule table** (`l1.test.ts`, DescribeTable-style over `test.each`): every §3.2 row as cases — type mismatch rejected, list-input multi-wire accepted, scalar-input second wire replaces, cycle (3-node and 2-node) rejected, self-loop no-op, dangling required input → error diagnostic, output-nowhere → warning unless terminal-ok, zero-destination → error, experimental node with gate off → error, secret literal → error, label collision → error with both node ids. Red run: disable the cycle DFS → cycle cases fail.
2. **TS codegen vs corpus** — every synced corpus entry: `renderTS(graph) === golden` (string-exact) AND the permutation test (same seeds as Go). This plus 7.2 pins the two implementations to one behavior; a divergence fails whichever side drifted.
3. **Store/undo** — mutations produce history entries; viewport/selection changes produce NONE (zundo partialize); 100-entry cap evicts oldest; undo of a paste removes all pasted nodes+edges atomically; redo restores ids identically (no re-nanoid on redo).
4. **Draft persistence** — mutation → idb draft written (fake-indexeddb); restore round-trips; explicit Save clears the draft.
5. **Inspector schema→zod generation** — for 5 representative components from the real artifact: generated zod accepts the default snippet's values and rejects type violations (string in duration, unknown enum).
6. **Graph diff (TS)** — same delta fixtures as 7.2.8, same expected outputs (shared via the corpus sync).

## 7.6 Playwright — mocked suite (`web/tests/specs/visual-*.spec.ts`)

Mock layer: `GET /api/schema` served from the committed artifact fixture (trimmed to ~25 components for speed — trimming script committed, not hand-edited); render/validate/simulate endpoints mocked with corpus-derived responses.

1. **`visual-canvas.spec.ts`** — palette search narrows (type "rela" → discovery.relabel visible, prometheus.scrape absent); click-to-place adds a node with the default label; drag-to-place via mouse events (ONE dnd test — React Flow drop needs `dataTransfer` synthesis; the click-path is the workhorse everywhere else, noted in a comment); node inline-rename shows sanitization preview when label is `my-label`; copy/paste yields new ids (assert two nodes, distinct `data-id`); ⌘Z removes the paste atomically.
2. **`visual-linking.spec.ts`** — start a drag from a `targets` output: compatible ports get the highlight class, a `prom.metrics` input does NOT (assert class absence); completing a valid wire draws the edge (edge count +1); attempting the invalid one leaves count unchanged; wiring a cycle fires the toast with "acyclic"; second wire into a scalar input → confirm toast → replacement (count unchanged, `from` changed).
3. **`visual-inspector.spec.ts`** — select relabel node: `action` renders as a Select containing exactly the enum values; add two `rule` blocks, drag-reorder, assert order in the (mocked-render) Code tab flips; secret-typed field shows ONLY the binding picker (no text input in the DOM); required-empty prop → problem badge on the node shows count, Problems row click selects+centers the node (assert `.selected` class + viewport change).
4. **`visual-code-sync.spec.ts`** — Code tab shows the mocked render; hovering a node adds the highlight decoration to its mapped lines (mock returns node_map); clicking a diagnostic in Problems places the CodeMirror cursor in the range.
5. **`visual-disable.spec.ts`** — disable mid-chain node → node at 50%/dashed (class assertions), downstream dangling problems appear naming the consumer; re-enable clears them.
6. **`visual-upgrade.spec.ts`** — pipeline mocked with old schema_version → banner renders; open review (mocked upgrade-check with one of each diff class) → each item renders its class UI (removed component = error card, removed attr = discard button); Accept → mocked save called with the new version stamp (assert via api.calls).
7. **`visual-simulate-s2.spec.ts`** — select relabel node → Simulate tab → pick built-in fixture → mocked trace renders as stage cards; a dropped target row shows the rule that dropped it; clicking a stage card selects the node.
Quality bars: every assertion content-level (the `locator('body')` grep applies); the autocomplete-kill-probe analog here: `visual-linking` red run = remove `isValidConnection` → the invalid-wire test fails (proof file).

## 7.7 Fullstack (`web/tests/fullstack/visual-graph-roundtrip.spec.ts`, `visual-pipeline-wiring.spec.ts`) — real stack, no mocks

1. **Build-and-serve journey**: login (LA-1) → new visual pipeline → place static-targets + scrape + remote_write (click-to-place), wire them, bind the seeded destination preset → Problems reaches zero → Save & Enable with matcher `cluster="prod-eu-1", role="metrics"` (seeded) → navigate to the collector's Served Config → assert it CONTAINS the generated header comment AND the scrape block with the chosen label (content-level, against the REAL render+merge). Expected outcome precisely: served config contains `// generated by shepherd visual builder` and `prometheus.scrape "<label>"`.
2. **Revision restore**: edit the graph (change scrape_interval), save → 2 revisions; restore rev 1 → reopen canvas → the interval prop shows the original value (graph round-trip through the DB proven in-browser).
3. **View-as-graph**: open the seeded TEXT pipeline's `/graph` → nodes rendered, zero edit affordances (no palette in DOM, inspector read-only), "Recreate as visual" → confirm dialog mentions lossy → lands on a new draft canvas with ≥ the extractable nodes.
4. **RBAC**: reader persona → `/pipelines/:id/visual` for a visual pipeline renders read-only canvas (no palette, no Save), per the hidden-not-disabled rule.

## 7.8 E2E (compose, real Alloy) — additions

1. **Scenario `visual-serve`** (joins the ordered flow after scenario 2): via the API, save+enable a corpus-equivalent visual pipeline matched to the e2e collector → `Eventually` the real Alloy reports APPLIED with the new hash and Shepherd's served-config endpoint shows the generated content → disable → hash reverts. (The agent applying visually-generated config is the end of the chain; content assertion includes the header comment.)
2. **Scenario `sandbox-sim`** (`--profile sim`, simulator service in compose): submit `minimal-scrape` for a real run → completed → captured series include the synthetic counter with the relabel-produced label; component health all-green in results; the rewrite disclosure lists exactly {2 destination rewrites? per graph: 1 destination rewrite, 1 discovery stub}. **Kill probe (the suite-review standard):** break the destination rewrite in the transform → captured series EMPTY → scenario fails; capture as the group's red proof.
3. Quality probes inherited: no sleeps, 3× stability run, content assertions.

## 7.9 Suite-level gates & budgets

| Suite | Budget | Gate |
|---|---|---|
| Go tests (visual + schema invariants, incl. binary-labelled) | ≤ 3m | `make test`, every PR |
| Vitest visual | ≤ 20s | `make test-ui` prelude |
| Playwright mocked visual specs | ≤ 60s added | `make test-ui`, every PR |
| Fullstack visual spec | ≤ 45s added | `make test-fullstack`, every PR |
| E2E visual-serve | ≤ 90s added | merge queue |
| E2E sandbox-sim | ≤ 3m | merge queue, `sim` profile |
| schema-verify | ≤ 4m (clone-bound) | Alloy-bump PRs + weekly cron |

Mutation-gate extension (the WSR 6.2 discipline): the 8-invariant PR-gate check grows three VB invariants — L1 cycle rejection, renderer determinism tie-break, transform secret-drop — target stays **caught by `make test && make test-ui` alone**; verified in the VB feature review.

# Part 8 — Delivery milestones (each ends green, standing rules apply)

1. **Schema unification**: serve `GET /api/schema`, retire `alloySchema.ts` for the adapter, generator + overlay CI in place. (Pays off before any canvas exists — the text editor's autocomplete jumps from ~20 to full-catalog.)
2. **Canvas core**: React Flow shell, palette, nodes, typed connect rules, inspector forms, L1, undo/redo, autosave drafts. No save yet.
3. **Codegen + gate**: both generators + corpus, render/validate endpoints with node-addressed diagnostics, Code tab, save/enable through the gate, `source='visual'`, revisions + graph diff.
4. **Read-only graph view** + "recreate as visual".
5. **S1 + S2**: flow overlay; relabel + log traces with fixtures; live-sample fetch behind its flag.
6. **Upgrade machinery**: version stamps, upgrade-check, review UI, needs_upgrade filter, overlay migrations.
7. **S3**: simulator service, transform, harness, run UI, containment, e2e profile.
8. **Hardening**: perf at 100+ nodes (memoized node renders, edge virtualization if needed), a11y pass on canvas (keyboard node navigation, focus ring on ports, drawer landmarks), docs (user guide + a "graph vs text — when to use which" page), WSR-style feature-loop review.

Sequencing note: milestone 1 is independently valuable and safe to ship alone; 2–3 are the minimum lovable builder; 5–7 are separable follow-ons in any order (S2 before S3 — it's 10× cheaper and answers 80% of "what will this rule do").