# Review: visual builder — canvas UX, component discovery, inspector forms, defaults

**Scope**: `web/src/visual/components/*` (VisualBuilderPage, CanvasPane, PipelineNode, Palette,
InspectorPanel, Toolbar, BottomDrawer, GraphViewPage), the data those components consume
(`internal/schema/artifacts/*`, `web/src/visual/store.ts`, `l1.ts`, `renderTS.ts`), and the tests
that cover them. Schema *generation* and the graph/validation *model* are other reviewers'
subjects; they appear here only where they surface in the UI.

**Method**: read the source; ran `pnpm test` (155 unit tests) and the 10 mocked
`visual-*.spec.ts` suites (52 tests); booted the dev stack (`make dev`) and drove the real UI with
Playwright/Chromium at 1280×800, 1440×900 and 1600×1000 against the **real** 184-component schema;
fetched `GET /api/schema/current` and measured its shape; validated the builder's own saved output
with the real `alloy validate` v1.18.1 binary inside `dev-alloy-metrics-1`. All numbers below are
measured, not estimated.

---

## Verdict

**No. A competent operator cannot build a working metrics pipeline in this UI today.** Four
independent blockers stack up, each sufficient on its own. (1) *Nothing can be deleted* — Backspace,
Delete, double-click and right-click all do nothing to a node or a wire, because React Flow runs in
controlled mode and the `selected` flag is never round-tripped into the node/edge arrays, so RF's
internal selection is empty forever (`CanvasPane.tsx:169-188`, `:232-245`); a mis-drawn wire is
permanent unless you undo every action after it. (2) *The most important wire in a metrics pipeline
cannot be drawn reliably* — `prometheus.scrape` has two input ports and both `<Handle>`s render at
the identical screen point (measured: both at `(736, 512)`), so a precise drop on the port dot lands
on the wrong (topmost) handle, silently fails the type check and produces no edge and no error;
14 components, including every scraper and every file/docker/kubernetes log source, are affected.
(3) *The destination cannot be configured* — `prometheus.remote_write`'s only top-level attribute is
`external_labels`; its `endpoint { url = … }` block is one of 2 347 block-nested attributes (69.6 %
of the entire configuration surface) that the inspector does not render at all, so the builder emits
`prometheus.remote_write "sink" { }` and the pipeline ships metrics nowhere. (4) *The output is not
valid Alloy* — every field is a text box that stores a string, so a `list`/`map`/`capsule`-typed
attribute (200 of 1 024 top-level attributes) renders quoted; the pipeline I built and saved through
the UI is rejected by the real binary with `expected array, got string`. On top of the blockers, a
correctly wired `prometheus.scrape` shows two permanent `required_attr_missing` errors that no user
action can clear, so the toolbar's green "Valid" state is unreachable for a real pipeline. The
canvas plumbing underneath (typed wires, drag affordances, undo/redo, cycle rejection, the
compatibility filter) is genuinely well built — the failure is in the last mile: selection, deletion,
port layout, form coverage and defaults.

---

## How it works today

### Layout

`VisualBuilderPage.tsx:161-171` is a fixed four-region shell: `Toolbar` (h-11) / `Palette` +
`CanvasPane` + `InspectorPanel` in a flex row / `BottomDrawer`. Measured geometry inside Chromium
(dark theme, `/pipelines/visual/new`):

| Viewport | App sidebar | Palette | **Canvas** | Inspector | Canvas as % of width |
|---|---|---|---|---|---|
| 1280×800 | 240 | 256 | **424 × 623** | 360 | 33 % |
| 1440×900 | 240 | 256 | **584 × 723** | 360 | 41 % |
| 1600×1000 | 240 | 256 | **744 × 823** | 360 | 47 % |

Opening the bottom drawer (`h-8` → `h-64`) takes the canvas at 1440×900 down to **584 × 499**.
A `PipelineNode` is `w-60` = 240 px (`PipelineNode.tsx:155`), so at 1280 px the canvas is 1.77 nodes
wide and at 1440 px it is 2.4 nodes wide. Palette (`Palette.tsx:84`, `w-64`) and inspector
(`InspectorPanel.tsx:23,60`, `w-[360px]`) are hard-coded and neither is collapsible or resizable;
the only collapsible region is the bottom drawer (`BottomDrawer.tsx:163`).

### Canvas interaction

`ReactFlow` is configured at `CanvasPane.tsx:472-493`: `fitView`, `snapToGrid` on an 8 px grid,
`minZoom 0.25` / `maxZoom 2`, `connectionRadius 30`, controls pinned top-left, pannable+zoomable
minimap bottom-left. Wires are colour-coded by wire type from the schema
(`schemaAdapter.ts:72-74`), the in-flight connection line takes the source port's colour
(`CanvasPane.tsx:223-229`), and during a drag every node projects the in-flight wire onto its own
ports and reports `data-drop-state` = `idle|valid|snapped|dimmed` (`store.ts:67-87`,
`PipelineNode.tsx:116-122`). `isValidConnection` type-checks the pair (`CanvasPane.tsx:270-284`) and
`onConnect` rejects cycles with a toast (`:319-343`). Keyboard: ⌘/Ctrl+C, V, Z, ⇧Z, A are handled on
the canvas wrapper (`:393-459`); ⌃\` toggles the drawer (`BottomDrawer.tsx:35-44`). Nodes are placed
by palette click (`Palette.tsx:73-80`) or HTML5 drag-and-drop (`CanvasPane.tsx:381-390`). Node label
is edited by double-clicking the label text (`PipelineNode.tsx:186-198`).

### Discovery

`Palette.tsx:43-71`. Components are grouped into five `<details open>` sections
(sources / transform / destinations / config / advanced) and sorted in schema key order within each.
Experimental components are hidden unless `allowExperimental` (`:46`) — measured: **153 of 184**
items are listed, the 31 experimental ones are hidden, and the 9 public-preview ones show a blue
`preview` chip (`:137-147`). Search is a case-insensitive **substring** match over the component
name **and** its overlay `doc` string (`:50-56`). When exactly one node is selected and the search
box is empty, the list is filtered to components with a port compatible with the selection
(`:57-68`), with a dismissible "Compatible with …" banner (`:99-112`).

### Inspector

`InspectorPanel.tsx:59-123`. For the single selected node it renders `def.attributes` — the
**top-level** attributes only — as a flat, unordered list of:

* `type === 'secret'` → a static read-only box reading "Use a config node binding" (`:66-72`)
* `attr.values?.length` → a `<select>` (`:74-86`)
* `type === 'bool'` → a checkbox (`:87-95`)
* everything else → a plain `<input type="text">` (`:96-106`)

plus a "Danger → Disable this node" checkbox (`:110-121`). There is no label field, no notes field,
no delete button, no per-field help, no required marker, no type hint, no default, and no block
rendering.

### Defaults

`store.ts:176-189`: a new node is `{ label: component.split('.').pop(), props: {}, disabled: false,
notes: '' }`. Nothing is prefilled.

### Feedback

Toolbar validity chip (`Toolbar.tsx:165-184`) → clicking it clicks the drawer's Problems tab
(`:102-104`). Bottom drawer has three tabs: Problems (flat text rows, `BottomDrawer.tsx:205-213`),
Generated config (live client-side `renderTS` output plus a "Verify render" button that diffs
against the server, `:215-224`), Simulation (relabel/log-stage tracing against built-in fixtures,
`:71-160`).

---

## Findings

### F1 — Nothing on the canvas can be deleted. **CRITICAL**

**Evidence.** Observed on the running app: with a node selected (inspector correctly showing it),
`Backspace` → 2 nodes, `Delete` → 2 nodes; with an edge clicked, `Backspace` → 1 edge,
double-click → 1 edge; right-click → no context menu; `.react-flow__node.selected` count is **0**
and `.react-flow__edge.selected` count is **0** at all times.

Root cause: React Flow is driven in controlled mode, but `rfNodes`/`rfEdges` deliberately omit the
`selected` field (`CanvasPane.tsx:173-176` — *"Do NOT pass `selected` here — let React Flow own its
selection state"*) while `onNodesChange` explicitly discards RF's `select` changes
(`CanvasPane.tsx:241` — *"'select' changes are handled exclusively by onSelectionChange"*). Because
the controlled array re-renders every node with `selected: false`, RF's internal selection never
survives, so `deleteKeyCode` has nothing to delete and `onNodesChange`'s `remove` branch
(`:238-240`) and `onEdgesChange`'s (`:247-256`) are dead code. `InspectorPanel` offers only
*disable* (`:110-121`), never *delete*.

**Why it matters.** This is the single hardest blocker. Every graph editor a user has ever touched
(Node-RED, draw.io, Figma, Lucidchart) deletes with the Delete key. Here, one mis-dropped node or
one wire to the wrong port is unrecoverable except by undoing everything after it. Users will
abandon the canvas and hand-edit the Alloy file.

**Direction.** Round-trip selection into the controlled arrays (add `selected: selected.includes(n.id)`
to `rfNodes`/`rfEdges` and let `onNodesChange`'s `select` change drive the store), *or* stop relying
on RF's selection entirely and implement Delete/Backspace in the existing `onKeyDown` handler
alongside copy/paste. Add an explicit delete affordance too — a per-node × on hover, a
right-click context menu, and a Delete button in the inspector header.

---

### F2 — Selection has no visual feedback on the node. **HIGH**

**Evidence.** The node root's class string is byte-identical before and after clicking it
(observed): `…border-t-border border-r-border border-b-border…` both times, and the accent
branch at `PipelineNode.tsx:139-142` / `:146-150` requires the RF `selected` prop, which is
permanently `false` (see F1). `CanvasPane.tsx:179` computes `isSelected: selected.includes(n.id)`
and passes it in `data` with the comment *"so PipelineNode can style it without RF's controlled
prop"* — but `PipelineNode` never reads `data.isSelected`; it reads the RF prop. The value is dead.

**Why it matters.** With multiple nodes on a small canvas (F5), the only way to know what the
inspector is editing is to correlate the panel's `h3` with a node title — and that `h3` scrolls out
of view as soon as you scroll the form (F9). Select-all (⌘A) produces no visible change at all.

**Direction.** Use `data.isSelected` in the class computation (one-line fix), or fix F1 and let the
RF prop work. Also give multi-selection a visible treatment.

---

### F3 — Multi-input components render all input handles at the same point; the primary wire cannot be drawn on the dot. **CRITICAL**

**Evidence.** `PipelineNode.tsx:201-227` lays port *labels* out in a flex column but each `<Handle
position={Position.Left}>` is positioned by React Flow's own CSS (`top: 50%`) relative to the single
`relative` container at `:201`. Measured on a real `prometheus.scrape` node: handles
`[{"id":"targets","p":[736,512]},{"id":"forward_to","p":[736,512]}]` — identical coordinates, two
runs, two zoom levels. The visible `targets` and `forward_to` labels sit ~35 px apart; the dots do
not.

Behaviourally: dragging from `discovery.kubernetes.targets` and dropping **exactly on the port dot**
of `prometheus.scrape` produced **0 edges** — the topmost handle (`forward_to`, `prom.metrics`) wins
the hit test, `isValidConnection` rejects `targets → prom.metrics`, and the drag ends with no edge,
no toast, no explanation. Dropping instead on the *"targets" label text* produced the edge, because
`connectionRadius: 30` (`CanvasPane.tsx:487`) then resolves to the nearest *valid* handle.

**14 of 184 components have ≥2 input ports** — `prometheus.scrape`, `pyroscope.scrape`,
`loki.source.file`, `loki.source.docker`, `loki.source.kubernetes`, `faro.receiver`,
`prometheus.enrich`, `loki.enrich`, `pyroscope.enrich`, `pyroscope.ebpf`, `pyroscope.java`, and the
three `database_observability.*`. That set contains the pivot component of every metrics pipeline
and of every file-based logs pipeline.

**Why it matters.** The user aims carefully at the labelled port, hits it, and gets nothing. There is
no error, so the natural conclusion is "the canvas is broken". It is also unteachable: the working
gesture (aim *near* the port, not *at* it) is the opposite of the intended one.

**Direction.** Give each handle an explicit vertical offset (`style={{ top: … }}` computed from the
port's row, or one `<Handle>` per flex row with `position: relative` on the row). Then add a
negative signal on a failed connection ("targets can't accept prom.metrics") rather than silently
dropping the gesture. Honour the schema's `port_display_order` (present on 27 components, currently
not read anywhere in the frontend) while you are in there.

---

### F4 — The inspector reaches 23 % of the configuration surface; blocks are 0 %. **CRITICAL**

**Evidence.** Measured over the served schema (`GET /api/schema/current`, 184 components):

| | count | share of all attributes |
|---|---|---|
| Total attributes | **3 371** | 100 % |
| Top-level attributes (`def.attributes`) | 1 024 | 30.4 % |
| Attributes nested inside blocks (696 blocks, up to 5 deep) | **2 347** | **69.6 %** |
| Top-level with a plausible widget (string/duration → text, bool → checkbox, number → text) | 783 | **23.2 %** |
| Top-level rendered as text but typed `list`/`map`/`capsule` (wrong widget, invalid output — see F6) | 200 | 5.9 % |
| Top-level `secret` → read-only placeholder | 41 | 1.2 % |

`InspectorPanel.tsx:63` iterates `def.attributes` only. `def.blocks` is declared in
`types.ts:69-74` and consumed by the *text editor's* autocomplete (`editor/alloyCompletion.ts:123`)
but never by the inspector — grep for `blocks` under `web/src/visual/components/` returns nothing.

Consequences:
* **61 of 184 components have at least one *required* attribute inside a block** and therefore cannot
  be validly configured at all (`discovery.azure.oauth.client_id`, `discovery.ec2.filter.name`,
  `prometheus.remote_write.endpoint.url`, …).
* **15 components have zero top-level attributes**, so their inspector is an empty form with only the
  Disable checkbox (`otelcol.receiver.otlp`, `otelcol.processor.attributes`,
  `otelcol.exporter.splunkhec`, `loki.echo`, `prometheus.exporter.self`, …).
* **95 of 184 components** hold more of their configuration in blocks than at top level.
* `prometheus.remote_write` — the destination in the canonical metrics pipeline — exposes exactly
  **one** field (`external_labels`, a map). Observed generated output for a fully wired pipeline:
  `prometheus.remote_write "remote_write" {\n}`. There is no way to enter the endpoint URL.

**Why it matters.** This is the difference between a demo and a tool. The UI can wire a topology but
cannot configure it, and the parts it cannot configure include *where the data goes*.

**Direction.** Render blocks as collapsible nested groups (repeatable blocks get add/remove rows —
that pattern also unlocks `prometheus.relabel`'s `rule` blocks and `loki.process`'s `stage` blocks,
which the Simulation tab already assumes exist in `props.rules`/`props.stage`,
`BottomDrawer.tsx:55-68`). If full block support is too big for one step, hand-curate an
"essentials" set per component (the schema already has a `key_props` field — declared in
`types.ts:67`, populated for 2 components, read by nobody) and render at minimum
`prometheus.remote_write.endpoint.url`.

---

### F5 — Secrets point at a feature that does not exist. **HIGH**

**Evidence.** `InspectorPanel.tsx:66-72` renders 41 top-level secret attributes (plus 48 more inside
blocks) as a static box reading "Use a config node binding", and `l1.ts:53-60` errors if a secret
ever holds a literal. But no code path in the frontend ever creates a `GraphBinding`: `store.ts` has
no binding action, `doc.bindings` is only ever `[]` from `makeDefaultDoc` (`store.ts:146`) or
whatever `importGraph` supplies, and grep for `binding` across `web/src` finds only the renderer
(`renderTS.ts:146-147`), the wire mapper (`api/client.ts:52,81`) and this placeholder string.

**Why it matters.** Any authenticated destination is unbuildable, and the UI's instruction to the
user is a dead end — there is no "config node", no binding picker, nothing to click.

**Direction.** Either build the config-node/binding picker the message promises, or change the
message to state the truth ("secrets must be set in the text editor") until it exists.

---

### F6 — Every field is a text box, so typed attributes generate invalid Alloy — and it saves. **CRITICAL**

**Evidence.** `InspectorPanel.tsx:96-106` writes `e.target.value` (always a `string`) into
`node.props`. `renderTS.ts:31-44`'s `serialize` then quotes any string regardless of the schema type.
End-to-end, observed in the running app: typing `5000` into `sample_limit` (type `number`) and
`OpenMetricsText1.0.0` into `scrape_protocols` (type `list`) produced

```alloy
prometheus.scrape "scrape" {
  scrape_interval = "30s"
  scrape_protocols = "OpenMetricsText1.0.0"
  sample_limit = "5000"
  targets = [discovery.kubernetes.kubernetes.targets]
  forward_to = [prometheus.remote_write.remote_write.receiver]
}
```

The Save button was enabled, the server render accepted it, and `GetPipeline` returns exactly that
text as the pipeline's `contents`. Feeding the same text to the real binary:

```
$ alloy validate /tmp/gen.alloy
Error: /tmp/gen.alloy:10:22: expected array, got string
  10 |   scrape_protocols = "OpenMetricsText1.0.0"
Error: validation failed
```

200 top-level attributes (and 429 more inside blocks) are `list`/`map`/`capsule`-typed and hit this.
Related: the `<select>` branch at `InspectorPanel.tsx:74-86` is **dead code** — zero attributes in
the served schema carry a `values` array (verified by walking the whole payload); only the test
fixture invents them (`tests/fixtures/schema-fixture.ts:31,47`).

**Why it matters.** The builder silently authors configs that the agent will refuse to load, and
nothing between the text box and the collector notices. `ValidatePipeline` returns `{"valid":true}`
for the broken text, so even the pipeline-level validation does not catch it.

**Direction.** Type-aware widgets: number input for `number`, duration input with unit hints for
`duration`, chip/tag list for `list`, key–value rows for `map`, and store native JS types in
`props`. Show the attribute's type next to its label. Longer term, run the real Alloy type check
server-side before allowing save.

---

### F7 — Wired ports still report `required_attr_missing`, so "Valid" is unreachable. **HIGH**

**Evidence.** `l1.ts:61` skips the required check only when `attr.type === 'secret'` or
`attr.input_type` is set — but `input_type` **appears nowhere in the served schema** (0 occurrences
across the whole payload; the only mentions in the repo are the field declaration `types.ts:47` and
this guard). **42 of 184 components** declare a required attribute whose name is also an input port
(`prometheus.scrape.targets`, `prometheus.scrape.forward_to`, `discovery.relabel.targets`,
`loki.process.forward_to`, …). Observed on a fully wired, correctly configured pipeline:

```
PROBLEMS: 2
 - L1 required_attr_missing: prometheus.scrape "scrape" is missing required attribute "targets"
 - L1 required_attr_missing: prometheus.scrape "scrape" is missing required attribute "forward_to"
```

Toolbar chip: red, "2 problems". Both nodes carry a red left border and a `⚠ 2` badge.

Worse, the "fix" the UI implies — type something into the `targets` text box the inspector helpfully
renders next to the wired port — makes things strictly worse. `renderTS.ts:117-134` emits the
attribute, then `:135-145` emits the port wire under the *same name*:

```alloy
prometheus.scrape "app" {
  targets = "typed-by-user"
  targets = [discovery.kubernetes.k8s.targets]
}
```

```
Error: attribute "targets" may only be provided once
```

**Why it matters.** The primary correctness signal in the toolbar is permanently red on a correct
pipeline, which trains users to ignore it — and the one obvious remedy produces a config the agent
rejects outright.

**Direction.** Treat "attribute is satisfied by an incoming wire" as satisfying `required` (either
populate `input_type` in the schema, or in `l1.ts` skip required attributes whose name matches an
input port that has ≥1 edge). Hide port-backed attributes from the inspector, or render them
read-only showing the wired source. Make `renderTS`/`render.go` refuse to emit an attribute that a
port already emits.

---

### F8 — Placing a node prefills nothing; two of the same component instantly collide. **HIGH**

**Evidence.** `store.ts:176-189` creates `props: {}` and `label: component.split('.').pop()`.
`default_snippet` is declared (`types.ts:62`) and never read anywhere in `web/src` — though it would
not help much: all **184** snippets in the artifact are the trivial `component "example" {}`.
`notes` is always `''` and has no UI. `def.doc` — a curated one-line description present for all 184
components in the overlay — is used **only** as a search haystack (`Palette.tsx:55`) and is never
displayed; the text editor shows it (`editor/alloyCompletion.ts:99`) but the visual builder does
not. The overlay's per-component `icon` (184 values) is never read either.

Observed: clicking `prometheus.scrape` twice gives two nodes both labelled `"scrape"`, which
immediately raises two `label_collision` errors, and the Generated config tab goes **completely
blank** (`renderTS.ts:55-68` returns `content: ''` on a collision, and `BottomDrawer.tsx:223` renders
that empty string with no message).

**Why it matters.** A freshly dropped node is an empty shell with an unhelpful name and (for the
42 components of F7) two red errors. Placing two of anything breaks the preview with no explanation.
Node-RED's baseline — drop a node, get sensible defaults and a unique auto-suffixed name — is not met.

**Direction.** Auto-suffix duplicate labels at creation time (`scrape`, `scrape_2`). Seed
`props` from schema defaults once the generator emits them. Show `def.doc` in a palette tooltip and
at the top of the inspector. When the preview cannot render, say why in the Code tab instead of
showing an empty `<pre>`.

---

### F9 — The inspector is an unstructured wall of 33 identical text boxes. **HIGH**

**Evidence.** Measured on `prometheus.scrape`: 33 form controls, `scrollHeight` 1 939 px in an
823 px panel — 2.4 screens of scrolling. Fields appear in raw schema order (`InspectorPanel.tsx:63`)
with no grouping, no search, no required markers, no type hints, no descriptions, no default values
and no indication which are already satisfied by a wire. The component name `h3` (`:61`) is not
sticky, so it scrolls away. There is no validation on input: values are committed to the store on
every keystroke (`:100-104`) and nothing is checked until the diagnostics pass re-runs globally.

**Why it matters.** Finding `scrape_interval` among 33 boxes named only by their raw Alloy
identifier requires already knowing the Alloy reference — which is exactly the user who does not need
a visual builder.

**Direction.** Sticky header with the component name, label and doc. Required fields first and
marked. Collapse advanced/optional fields behind a disclosure. Inline per-field validation and a
field filter box. Show wire-satisfied ports as read-only rows.

---

### F10 — First drop jumps the canvas to 200 % zoom; click-placement stacks nodes on top of each other. **MEDIUM**

**Evidence.** `.react-flow__viewport` transform measured before/after the first drop:
`scale(1)` → `translate(-108px, 105.5px) scale(2)`. `fitView` (`CanvasPane.tsx:477`) fits a single
node, hits `maxZoom: 2` (`:492`) and stays there. Subsequent drops at the same screen offsets then
map to nearly the same flow coordinates: three drops 220 px apart on screen produced nodes at flow
positions 21 px apart, visibly stacked (screenshot). Click-placement is no better —
`Palette.tsx:73-80` staggers by 60 px in x and 80 px in y (cycling every 3), against a node
240 px wide and 68–164 px tall, so consecutively placed nodes always overlap. Overlapping nodes also
make selection unpredictable: a click on a covered node's label reaches the covering node instead.

**Why it matters.** The very first interaction is a disorienting zoom jump, and the second and third
place nodes underneath the first. There is no auto-layout, no snap-away-from-collision, and no
"arrange" command to recover.

**Direction.** Clamp the initial fit (`fitView({ maxZoom: 1 })`), or skip fitting until ≥2 nodes.
Stagger click-placement by at least the node's measured size, and offset a drop that would land on an
existing node. Consider an "auto-arrange" toolbar action (Dagre left-to-right) — it would also fix
the layout of imported graphs.

---

### F11 — Wires for destinations run right-to-left, so the graph reads backwards. **MEDIUM**

**Evidence.** In the served schema, `prometheus.remote_write` has `inputs: []` and
`outputs: [{export: "receiver"}]`, while `prometheus.scrape` has `inputs: [targets, forward_to]` and
`outputs: []`. This is textually correct (Alloy writes `forward_to = [prometheus.remote_write.x.receiver]`),
but on the canvas it means the *destination* exposes a right-side source handle and the *scraper*
exposes left-side target handles: the only way to draw the wire is destination → scraper, i.e. the
arrow points from the sink back to the source. In the built pipeline the wire loops backwards behind
the node (screenshot), and 21 destination-category components behave this way.

**Why it matters.** Node-RED and every dataflow tool the audience knows read left-to-right,
source → sink. Here the arrows contradict the data flow, the canvas cannot be read at a glance, and
the compatibility filter ("Compatible with …") suggests destinations as things that *feed* a scraper.

**Direction.** This is a modelling decision owned by the graph reviewer, but the UI can compensate:
render `forward_to`-style ports on the *right* of the source node and reverse the drawn arrow so the
visual direction matches data flow, while keeping the underlying reference direction unchanged.

---

### F12 — Palette search is a plain substring match over 153 items with no ranking or descriptions. **MEDIUM**

**Evidence.** `Palette.tsx:50-56`. Measured queries against the real schema:

| query | hits | note |
|---|---|---|
| *(empty)* | 153 | 20 visible without scrolling; list `scrollHeight` 5 036 px = 7.5 screens |
| `scrape` | 8 | `prometheus.scrape` ranks **last** (schema key order) |
| `remote write` | **0** | space kills it; `remote_write` → 2 |
| `k8s` | 2 | `discovery.kubernetes` **not** among them |
| `reids` (typo) | 0 | no fuzzy matching |
| `metrics` | 43 | doc matches, unranked |
| `grafana cloud` | 0 | no synonyms/aliases |

Results are never highlighted, never ranked (`prometheus.scrape` is 8th of 8 for "scrape"), and the
`doc` string that produced a doc-only match is not shown, so hits look arbitrary. Category sections
show no counts. The entire first screen of the unfiltered palette is `discovery.*`.

**Why it matters.** With 184 components, search *is* the discovery mechanism. A user who knows the
product but not the exact Alloy identifier (`remote write`, `k8s`, `grafana cloud`) gets zero
results and concludes the component does not exist.

**Direction.** Fuzzy/token-aware matching (split on space/underscore/dot), relevance ranking
(exact > prefix > name-substring > doc), show the matched doc line under the name, highlight the
matched span, and add curated aliases. Show per-category counts, and remember the last query.

---

### F13 — Experimental components are hidden with no way to reveal them. **MEDIUM**

**Evidence.** `Palette.tsx:46` filters on `allowExperimental`, which is initialised `false` in
`store.ts:170` and has **no setter and no UI control** anywhere. 31 of 184 components (17 %) are
therefore unreachable in the builder, and `l1.ts:35-45` additionally errors on any experimental node
in an imported graph — with the message *"requires the org toggle to be enabled"*, referring to a
toggle that does not exist in the UI.

**Direction.** Add the toggle (org-scoped, as the message implies), or a "show experimental"
checkbox in the palette header; at minimum, stop referring to a control that is not there.

---

### F14 — Problems are dead text: no node link, no code, no fix hint. **MEDIUM**

**Evidence.** `BottomDrawer.tsx:205-213` renders each diagnostic as a plain `<div>` of
`layer code: message`. Rows are not clickable, do not select or centre the offending node, and are
not grouped or deduplicated — placing two identical components produced 11 rows including four
copies of the same two messages, each identifying the node only as `"scrape"` (both are called
`"scrape"`). `label_collision` rows say *"Duplicate label on prometheus.scrape"* without saying what
to rename or which of the two nodes is meant. The node badge (`PipelineNode.tsx:257-266`) shows a
count with no tooltip and no click target.

**Direction.** Make rows clickable (select + `fitView` on the node), show the node label and a "Fix"
affordance where one is mechanical (rename, add wire), group by node, and dedupe.

---

### F15 — Save gates on matchers only: an unnamed pipeline with errors saves fine. **MEDIUM**

**Evidence.** `Toolbar.tsx:94-95`: `canSave = !matchersRequired && !saveMutation.isPending` — the
error count (`:22`) is computed and displayed but never gates. Observed: with an empty name and
4 client-side errors, Save is enabled; the pipeline in the walkthrough below saved successfully
(`toast: "Pipeline created"`, navigation to `/pipelines/<uuid>`) with 2 unresolved errors and
Alloy-invalid contents. The only thing that stopped a save was a *server-side* render diagnostic
(label collision), surfaced as a toast that disappears (`:84-91`).

**Direction.** Require a name. Warn (or block, behind a confirm) when client diagnostics contain
errors. Persist the server's render diagnostics into the Problems drawer rather than a transient
toast.

---

### F16 — Contradictory validity signals. **LOW**

`Toolbar.tsx:173-183` shows green "Valid" whenever `errors === 0`, even with warnings — while
`BottomDrawer.tsx:170` colours the Problems tab red whenever `diagnostics.length > 0`. A
warnings-only graph therefore shows "✓ Valid" and "Problems 3" in red simultaneously. When errors do
exist, the chip reports `diagnostics.length` (errors **+** warnings) but is labelled by the error
state — so "2 problems" may mean 1 error and 1 warning.

---

### F17 — Keyboard shortcuts are undiscoverable and focus-fragile. **LOW**

`CanvasPane.tsx:393-459` requires the canvas wrapper to hold focus. Clicking any inspector field or
the toolbar name moves focus away, after which ⌘Z/⌘C/⌘V silently do nothing until the canvas is
clicked again. Nothing in the UI advertises the shortcuts (only ⌃\` is shown, in the drawer,
`BottomDrawer.tsx:188-193`), there are no undo/redo buttons, and paste always offsets by a growing
24 px (`:414`) with a `_copy` suffix (`:420`) even for the first paste into empty space.

---

### F18 — Minimap and controls occupy scarce canvas; the minimap is near-useless. **LOW**

At 1440×900 the minimap (`CanvasPane.tsx:504-513`, default ~200×150 px) covers ~7 % of a 584×723
canvas and, on a 3-node graph, renders as a near-empty grey rectangle (screenshot). Controls sit
top-left (`:498`) over the area where the first node lands. Neither is collapsible.

---

### F19 — No component documentation anywhere in the builder. **MEDIUM**

Covered under F8: `def.doc` (curated for all 184 components) is only a search haystack; `def.icon`
(184 values) and `port_display_order` (27 components) are read by nothing; `key_props` is declared in
`types.ts:67` and never used. Port names are rendered raw (`targets`, `forward_to`, `receiver`) with
no tooltip explaining what they carry. The user must know Alloy already.

---

## Walkthrough: "build a metrics pipeline from scratch"

Real run, dev stack, 1600×1000, admin/admin, `/pipelines/visual/new`.

1. **Land on the empty canvas.** Canvas is 744×823 of a 1600×1000 window. Inspector says "Select a
   node to inspect. Nodes: 0 Edges: 0". Toolbar says "✓ Valid". Save is disabled with the tooltip
   "Add at least one matcher before saving" — the *first* thing the UI asks for is a fleet matcher,
   before any pipeline exists. ⚠️ *friction*
2. **Find a Kubernetes discovery.** The palette opens on 153 items, alphabetical inside SOURCES; the
   whole first screen is `discovery.*`. Typing `k8s` returns 2 components, neither of which is
   `discovery.kubernetes`. Typing `kubernetes` finds it, 1st of 8. No description is shown to confirm
   it is the right one. ⚠️ *friction (F12, F19)*
3. **Place it.** Click → the node appears and the canvas immediately jumps from 100 % to 200 % zoom.
   ⚠️ *friction (F10)*
4. **Place `prometheus.scrape` and `prometheus.remote_write`.** They land 21 px apart, overlapping the
   first node. There is no arrange command; the recovery is to drag each one apart by hand or hit
   zoom-out/fit repeatedly. ⚠️ *friction (F10)*
5. **Wire discovery → scrape.** Drag from the purple `targets` dot to the scrape node's `targets`
   dot — the wire snaps green mid-drag, then nothing happens on drop. No edge, no message. Repeat:
   same. The wire only lands when dropped on the *"targets" text*, not the dot. 🛑 **blocker (F3)**
6. **Wire scrape → remote_write.** There is no output on `prometheus.scrape`. The only wire that can
   exist runs *from* `remote_write.receiver` *into* `scrape.forward_to`, so the arrow points from the
   sink back to the scraper and loops backwards on screen. ⚠️ *friction (F11)*
7. **Realise the destination node is in the wrong place** and try to move it left of the scraper. Fine
   — dragging works well.
8. **Configure discovery.** Select it (no visual change on the node — F2), inspector shows 3 fields:
   `api_server`, `role`, `kubeconfig_file`. Type `pod` into `role`. Works. There is no hint that `pod`
   is one of a fixed set, no list of valid roles, no doc. ⚠️ *friction (F9)*
9. **Configure the scraper.** 33 identical text boxes, 2.4 screens tall, unordered, unlabelled by
   type. `targets` and `forward_to` appear as editable text boxes even though both are wired ports —
   typing in them produces a duplicate attribute the agent rejects. Setting `scrape_interval = 30s`
   works. Setting `sample_limit = 5000` emits `"5000"`. Setting `scrape_protocols` emits a quoted
   string where Alloy needs an array. 🛑 **blocker (F6, F9)**
10. **Configure the destination.** Inspector shows exactly one field: `external_labels`. The endpoint
    URL — the whole point of `remote_write` — is not present, and no part of the UI hints that an
    `endpoint` block exists. 🛑 **blocker (F4)**
11. **Check Problems.** 2 errors: `prometheus.scrape "scrape" is missing required attribute "targets"`
    and `… "forward_to"` — both of which *are* wired. The toolbar chip is red. Nothing the user can do
    clears them. 🛑 **blocker (F7)**
12. **Check the Generated config.** It shows the truth: `prometheus.remote_write "remote_write" {}` and
    quoted list values. Nothing flags either as a problem.
13. **Try Simulation.** With the scrape node selected, both sub-panels say "Select a relabel node to
    trace" / "Select a loki.process node to trace". Simulation only exists for `prometheus.relabel`
    and `loki.process` (`BottomDrawer.tsx:72,114`). ⚠️ *dead end*
14. **Delete the accidental extra node placed in step 4.** Backspace: nothing. Delete: nothing.
    Right-click: nothing. Double-click: opens rename. The inspector offers "Disable this node", not
    delete. The only way out is ⌘Z repeatedly, discarding all later work. 🛑 **blocker (F1)**
15. **Save.** Name field accepts empty; adding a matcher enables Save. Save succeeds
    ("Pipeline created") despite the 2 errors. The persisted `contents` fail `alloy validate` with
    `expected array, got string`. 🛑 **blocker (F6, F15)**

**Net:** steps 5, 9, 10, 11, 14 and 15 are blocking. A determined operator can produce a *saved*
pipeline, but not a *working* one, and cannot correct a mistake without unwinding their work.

---

## What is genuinely good and should be preserved

* **Typed, colour-coded wires with live drag affordance.** `isValidConnection` (`CanvasPane.tsx:270-284`),
  the wire-coloured connection line (`:223-229`), the per-node `idle/valid/snapped/dimmed` projection
  (`store.ts:67-87`, `PipelineNode.tsx:116-122`) and the 30 px snap radius add up to a connection
  experience better than Node-RED's. Exposing the state as `data-drop-state` for tests instead of
  scraping classes is exactly right.
* **Cycle prevention with a clear toast** (`CanvasPane.tsx:319-343`) plus a second check in the store
  (`store.ts:242-251`).
* **Undo/redo via zundo with a well-tuned `equality`** (`store.ts:337-345`) that excludes viewport and
  drag state, so panning and connection drags do not pollute history, and a paste is one atomic entry.
  This is a thoughtful piece of work with real unit tests behind it.
* **The compatibility filter** (`Palette.tsx:57-68`) — selecting `discovery.kubernetes` narrows 153
  components to 13. This is the single best discovery affordance present, and it should be
  strengthened (e.g. offer it from a dropped-on-empty-canvas wire end) rather than replaced.
* **Live generated-config preview with a "Verify render" client/server diff**
  (`BottomDrawer.tsx:217-224`). The honesty of showing the exact text is valuable, and the
  client/server render parity check is a good idea.
* **The load/edit error paths in `VisualBuilderPage.tsx:72-137`** — cross-org lookup, a clear message
  for non-visual pipelines, and an explicit `opaque` failure instead of an empty canvas.
* **`GraphViewPage`'s read-only mode and the honest "Recreate (lossy)" confirm dialog**
  (`GraphViewPage.tsx:174-201`).
* **Matcher validation with inline feedback** (`Toolbar.tsx:39-49`, `matcher.ts`) — the one form field
  in the whole builder that validates as you type.
* **Performance discipline**: the `connectingFrom`-in-store design and its documented rationale
  (`store.ts:16-26`, `PipelineNode.tsx:88-96`) avoid re-rendering every node per drag.

---

## Test-coverage assessment

**Numbers.** 155 Vitest unit tests pass in 1.1 s; 52 mocked Playwright tests across 10
`visual-*.spec.ts` files pass in 8.3 s. No test in the repo is skipped in the visual suites
(`test.skip` appears only in `destinations`, `editor-autocomplete`, `git`, `revisions`, `states`,
`served-config`). Every blocker in this review is present in `main` with the suite fully green.

**The central problem: the fixture schema is a fiction.** `tests/fixtures/schema-fixture.ts` defines
9 components whose shape contradicts the schema the backend actually serves:

| fixture claims | reality (`GET /api/schema/current`) |
|---|---|
| `prometheus.scrape` has `outputs: [{export: 'metrics'}]` (`:49`) | `outputs: []` |
| destinations have `inputs: [{prop:'receiver'}]` (`:20`) | `inputs: []`, `outputs: [{export:'receiver'}]` |
| attributes carry enum `values` (`:31`, `:47`) | **0** attributes carry `values` in 184 components |
| no component has more than 1 input port | 14 components have 2 |
| every component has 0–3 top-level attributes and `blocks: []` | 1 024 top-level + 2 347 block-nested attributes; 128 components have blocks |

Because of this, the suite exercises a topology in which data flows left-to-right, every node has at
most one input handle, and forms are 1–3 fields long. F3 (overlapping handles), F4 (block coverage),
F6 (list/map types), F7 (port-backed required attrs), F9 (33-field forms) and F11 (inverted wires)
are all structurally invisible to it. `visual-linking.spec.ts` proves wiring works — but only for
single-input components.

**Nothing tests selection state or deletion.** No test asserts a selected node looks different, and
no test presses Delete/Backspace — hence F1 and F2 shipped. `visual-canvas.spec.ts:41-46` even
force-clicks and re-focuses to work around the selection fragility rather than asserting on it.

**Vacuous assertions.**
* `visual-inspector.spec.ts:31-38` — named "inspector shows attribute inputs for selected node" but
  asserts only `toBeVisible()` and `toContainText('discovery.relabel')`; the fixture gives
  `discovery.relabel` **zero** attributes, so it would pass with an entirely empty form.
* `visual-inspector.spec.ts:40-52` — "secret-typed field shows binding picker text, not a text input"
  asserts `[data-testid="attr-input-password"]` is *not* visible; `prometheus.remote_write` has no
  `password` attribute in the fixture, so the locator can never match. The real secret rendering
  (`attr-secret-*`) is never asserted.
* `visual-disable.spec.ts` — four tests, all asserting the same two Tailwind classes; the third and
  fourth are duplicates of the first.
* `visual-canvas.spec.ts:20-24` — "palette search narrows" asserts a positive and a negative on the
  9-component fixture; it cannot detect the substring-only, unranked behaviour of F12.

**No component-level tests at all.** There is no `*.test.tsx` in `web/src`; the unit suite covers only
pure logic (`l1`, `renderTS`, `store`, `matcher`, `schemaAdapter`, `draft`). The store tests are good
(atomic paste undo, history cap, `selectConnectionState` reference stability) but no test renders
`InspectorPanel` against a realistic `ComponentDef` — which is exactly where F4/F6/F9 live.

**No a11y coverage of the builder.** `tests/specs/a11y.spec.ts` visits `/pipelines` and
`/pipelines/new` only; the visual builder is not checked for accessible names, focus order or live
regions. Spot-check: the canvas is a bare `tabIndex={0}` div with no role or label
(`CanvasPane.tsx:462-471`); inspector fields use `<label>` elements not associated with their inputs
(no `htmlFor`/`id`, `InspectorPanel.tsx:65`); the palette's category disclosure and drag-drop have no
keyboard equivalent beyond click-to-place.

**No fullstack coverage of the builder.** `tests/fullstack/` has `wizard`, `pipelines`, `walkthrough`,
etc., but nothing driving `/pipelines/visual/new` against the real schema — which is exactly the
configuration that exposes every finding above. One fullstack spec that places
`discovery.kubernetes` + `prometheus.scrape` + `prometheus.remote_write`, wires them, and asserts the
saved contents pass `alloy validate` would have caught F3, F4, F6 and F7 in a single test.

**Recommended additions, in priority order.** (1) A fullstack "build a metrics pipeline" spec that
ends in `alloy validate`. (2) Regenerate `schema-fixture.ts` as a *subset of the real artifact*
(e.g. 8 real components copied verbatim) so mocked tests inherit real shapes. (3) Component tests for
`InspectorPanel` against `prometheus.remote_write` and `prometheus.scrape` asserting field counts and
widget types per attribute type. (4) Delete/selection tests. (5) Extend `a11y.spec.ts` to the builder.

---

## Open questions for the maintainers

1. **Is block editing on the roadmap, and what is the interim answer for `endpoint.url`?** 69.6 % of
   the configuration surface and 61 components' required fields are behind blocks. Without a plan
   here, the builder is a topology sketcher, not a pipeline builder. Is the intended answer "build the
   topology visually, finish it in the text editor" — and if so, is that round-trip safe given
   `source: 'visual'` locks a pipeline out of visual editing (`VisualBuilderPage.tsx:109-115`)?
2. **What was `input_type` meant to be, and who was supposed to populate it?** `l1.ts:61` depends on
   it; the schema generator never emits it. Was the intent "this attribute is fed by a port"? If so it
   should be generated for all 51 port-backed required attributes.
3. **Do config nodes / bindings exist as a design?** `InspectorPanel.tsx:71` and `l1.ts:58` both tell
   the user to use one, and `renderTS`/`render.go` can emit them, but no UI can create one. Is this a
   dropped task or a not-yet-started one?
4. **Should wire direction follow data flow or reference direction?** The current model makes
   destinations upstream of sources on the canvas (F11). Fixing it visually is cheap; fixing it in the
   model is not. Which is intended?
5. **Is `allowExperimental` supposed to be an org setting?** `l1.ts:43` promises a toggle; there is no
   setter in the store and no control in the UI. 17 % of components are unreachable meanwhile.
6. **Should Save be blocked on client-side errors?** Today a pipeline that Alloy rejects can be saved
   and served to collectors. Where is the intended enforcement point — client diagnostics, server
   render, or a real `alloy validate` in the save path?
7. **What is the target viewport?** At 1280×800 the canvas is 424 px wide (1.77 nodes). Is a
   collapsible palette/inspector planned, or is the builder explicitly a ≥1600 px tool?
8. **How is the 184-component palette meant to be navigated?** Is the compatibility filter intended as
   the primary path (in which case it needs to work from an empty canvas too), or is search meant to
   carry it (in which case it needs ranking and fuzzy matching)?
9. **Is Simulation intended to grow beyond `prometheus.relabel` and `loki.process`?** Today the tab is
   a dead end for 182 of 184 components, yet it occupies a third of the drawer's tab bar.
10. **Why does `schema-fixture.ts` diverge from the real artifact?** If it predates the generator, is
    there an owner for keeping mocked-suite fixtures derived from `internal/schema/artifacts/`?

---

*Reviewed against `main` @ `11f4e16`, Alloy schema `v1.18.1` (184 components), Alloy binary v1.18.1.
No source files were modified in the course of this review.*
