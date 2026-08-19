# Review: graph model, connectivity, validation and code generation

Scope: `web/src/visual/{l1,store,renderTS,schemaAdapter,types}.ts`, `web/src/visual/components/{CanvasPane,PipelineNode,Toolbar,VisualBuilderPage,BottomDrawer}.tsx`,
`internal/visual/{render,parse,upgrade}.go`, `internal/visual/testdata/corpus/`, `internal/schema/`, `internal/validate/`,
`internal/mgmtapi/{rpc_visual,rpc_pipeline}.go`, `tools/alloy-schema-gen/extract.go`, and the surrounding tests.

Everything below was verified by running code, not by reading it alone. Commands and outputs are quoted inline.

---

## 1. Verdict

**Not rock solid.** The renderer's *core* is genuinely good — the reference model matches Alloy's real
semantics, the topological sort is deterministic, and against the shipped 184-component schema it emits
Alloy that the real `alloy validate` accepts (proved below). But **the entire test pyramid — Go unit tests,
TypeScript unit tests, and every Playwright spec — runs against a hand-written 8–12-component fixture schema
whose port model is *inverted* relative to the schema the product actually ships.** Eight of the nine golden
corpus files are not valid Alloy; `alloy v1.18.1` rejects them with 30 errors. Consequently the "two
implementations agree, proved by a byte-exact corpus" mechanism proves agreement only on inputs that can
never occur in production, and it has masked at least three defects that make the builder unusable against
the real schema:

1. 46 of 184 components (all `otelcol.*`, `beyla.ebpf`, `faro.receiver`) have unnamed input ports. The canvas
   gives them handle id `p0`; both renderers look them up by `prop` (`""`/`undefined`). **Every wire drawn to
   an OTel component is silently discarded at render time, with zero diagnostics from any layer.**
2. 42 of 184 components declare a required attribute that shadows an input port name (`targets`,
   `forward_to`, …). L1 has no way to tell "satisfied by a wire" from "unset", so a correctly wired pipeline
   shows blocking `required_attr_missing` errors.
3. 128 of 184 components carry nested blocks (`endpoint`, `client`, `output`, …), and neither the graph
   document nor either renderer can represent a block. `prometheus.remote_write` has no `url` you can set;
   the corpus's workaround (`endpoint[0].url = …`) is a syntax error in Alloy.

**Single biggest problem: the test fixtures encode a graph model that is the mirror image of the shipped
schema, so the test suite validates a product that does not exist.** Everything else in this review is
downstream of that.

---

## 2. How it works today

### 2.1 Data flow, canvas action → generated Alloy

```
Palette click / drop
  → store.addNode()                    (web/src/visual/store.ts:176)
Drag handle → handle
  → CanvasPane.isValidConnection()     (CanvasPane.tsx:270-284)  ← the ONLY type check
  → CanvasPane.onConnect() cycle guard (CanvasPane.tsx:319-343)
  → store.addEdge()                    (store.ts:229-257)        ← self-loop + dup + cycle only
  → revalidate() → l1.validateGraph()  (store.ts:152-158, l1.ts:11)
Inspector edit
  → store.updateNode() → revalidate
Live preview
  → renderTS(doc, schema)              (BottomDrawer.tsx:26, renderTS.ts:46)
Save (Toolbar.tsx:53-78)
  → POST /visual/render (server)       (rpc_visual.go:79-89 → visual.Render, render.go:156)
  → contents = server render
  → CreatePipeline/UpdatePipeline with { contents, wizard_state: doc }
      → checkVisualRenderMatch()       (rpc_pipeline.go:144-174)  re-renders wizard_state, must equal contents
      → validate.WrapForValidation + Stages12  (rpc_pipeline.go:300-303)
Enable
  → stage3Check() merge dry-run        (rpc_pipeline.go:593+)
Reload for editing
  → GET .../pipelines/{id}/graph → ParseAlloy(contents)  (parse.go:27)   ← NOT wizard_state
  → store.importGraph()                (VisualBuilderPage.tsx:116-125)
```

### 2.2 The graph document

`alloy-graph/v1` (`web/src/visual/types.ts:33-41`, mirrored in `internal/visual/render.go:12-61`):
nodes (`id`, `component`, `label`, `position`, `props`, `disabled`, `notes`), edges
(`{from:{node,port}, to:{node,port}, order?}`), bindings (`{node, prop, ref:{node, export, expr}}`),
viewport, meta. `props` is a **flat** `map[string]any` — there is no representation for a nested block.

### 2.3 Codegen

`internal/visual/render.go:156` and `web/src/visual/renderTS.ts:46` are independent implementations of:
label sanitize → collision check → Kahn topological sort with a
`(categoryOrder, component, label)` tie-break → header comment → `// node "x" disabled` markers →
per node: attributes in **schema** order, then wired inputs as reference lists, then bindings.

---

## 3. The connectivity model, precisely

### 3.1 Port identity

| Layer | How a port is identified |
|---|---|
| Schema artifact | `inputs: [{prop?, type, cardinality}]`, `outputs: [{export?, type}]` (`extract.go:74-115`) |
| React Flow handle | `portHandleId(port, i) = port.prop ?? port.export ?? "p"+i` (`schemaAdapter.ts:28-30`), rendered at `PipelineNode.tsx:204,231` |
| Edge in the document | the raw handle id: `{ node: c.source, port: c.sourceHandle ?? '' }` (`CanvasPane.tsx:337-340`) |
| L1 validation | `portHandleId` (`l1.ts:83-84, 121, 128`) |
| Go renderer | `in.Prop` / `e.From.Port` — **no fallback** (`render.go:280, 325-327`) |
| TS renderer | `input.prop` / `e.from.port` — **no fallback** (`renderTS.ts:137, 141`) |

Port identity is stable across save/load **only for named ports**. For the 46 components with unnamed
inputs the canvas writes `"p0"` and the renderers look for `""` — the identity is not shared, and the wire
evaporates. The schema extractor's own comment predicted this
(`extract.go:71-73`: *"Named ports are what let a stored graph reference a port stably; without them every
handle falls back to a synthetic index and saved graphs cannot round-trip"*) and then the metadata fallback
at `extract.go:206-212` creates exactly that condition.

### 3.2 Type compatibility

```ts
// web/src/visual/l1.ts:191-198
export function portsCompatible(fromType, toType) {
  if (fromType === toType) return true;
  return (fromType === 'otel.any' || toType === 'otel.any')
      && fromType.startsWith('otel') && toType.startsWith('otel');
}
```

Eight closed wire types (`types.ts:1-9`), matching the overlay's `wire_types` (which even records the Go type
each maps to: `[]discovery.Target`, `prometheus.Appendable`, `loki.LogsReceiver`, `otelcol.Consumer`,
`pyroscope.Appendable`). This is faithful to Alloy. Note that `otel.any` wildcarding is *broader* than the
design doc's stated rule (`docs/visual-builder-design-VB1.md:147` — "exactly"); in practice it is moot,
because the extractor's metadata fallback collapses every OTel port to `otel.any` (46 of 46 unnamed inputs;
38 of 121 outputs), so the per-signal discipline promised at design-doc line 152 does not exist in the
shipped artifact.

### 3.3 Direction — Alloy's real model, and the inversion

Alloy has no "wires". A component's *arguments* reference another component's *exports*:

```alloy
prometheus.scrape "app" {
  targets    = [discovery.kubernetes.k8s.targets]        // arg → export
  forward_to = [prometheus.remote_write.sink.receiver]   // arg → export
}
```

The schema models this correctly: `inputs` are reference-accepting **arguments**, `outputs` are **exports**
(design doc line 141 states this explicitly). The renderer emits `<input.prop> = [<from.component>.<from.label>.<from.port>]`
inside the **`to`** node's block. **So a graph edge points export → argument, which for the metrics/logs/profiles
path is the opposite of data flow**: data flows `scrape → remote_write`, but the edge is `remote_write → scrape`.

`PipelineNode.tsx:212,243` puts inputs on the **left** (target handles) and outputs on the **right** (source
handles). So to build a working metrics pipeline the user must drag from `prometheus.remote_write`'s right
edge leftwards to `prometheus.scrape`'s left edge — a right-to-left, backwards wire, with the destination
placed left of the source. The dev seed demonstrates it: `internal/cli/dev.go:85` places `discovery` at
x=40, `scrape` at x=420, `remote_write` at x=800, then draws edge `e2` from n3 (x=800) to n2 (x=420).

Verified with the real schema (temporary harness over `visual.Render` + `internal/schema/artifacts/alloy-v1.18.1.json`
merged with the overlay):

```
---- CASE A (export→arg edges, real schema)      →  valid Alloy, alloy validate: no output (PASS)
prometheus.scrape "app" {
  job_name = "app"
  targets = [discovery.kubernetes.k8s.targets]
  forward_to = [prometheus.remote_write.sink.receiver]
}

---- CASE B (data-flow direction, real schema)   →  the forward_to wire is silently dropped
prometheus.scrape "app" { job_name = "app"  targets = [...] }
prometheus.remote_write "sink" { }
```

The canvas cannot produce Case B (React Flow normalises source/target and `isValidConnection` requires
source∈outputs, target∈inputs), so this is a correctness issue only for the paste/import/HTTP-API paths —
but it is a serious usability inversion, and it is the reason `terminal_ok` is applied backwards (§4, F5).

### 3.4 Cardinality and ordering

* Every input in the shipped artifact has `cardinality: "list"` (103/103). Scalar inputs exist only in test
  fixtures. `render.go:296-300` / `renderTS.ts:144` emit `[a, b]` for list, `refs[0]` for scalar.
* Fan-in order comes from `edge.order` (`render.go:283-290` `sort.SliceStable`; `renderTS.ts:138` stable sort).
  **Nothing in the application ever sets `order`** — `grep -n "order" web/src/visual/components/*.tsx web/src/visual/store.ts`
  returns no assignment. `store.addEdge` (store.ts:252-255) constructs `{id, from, to}` with no `order`.
  So multi-wire ordering is document-insertion order and is not reorderable, contrary to design doc line 185.
  Only the `fanin-fanout` corpus fixture carries hand-written `order` values, and `render_test.go:247-273`
  tests them — testing a field the product never writes.
* Fan-out is unconstrained (correct: Alloy exports are shareable).

### 3.5 Enforcement points

| Rule | Connect-time (`isValidConnection`) | `store.addEdge` | L1 `validateGraph` | Render | Server |
|---|---|---|---|---|---|
| port exists | yes | **no** | implicitly | drops wire | — |
| type compatible | yes | **no** | yes (`type_mismatch`) | — | — |
| self-loop | yes | yes | — | — | — |
| duplicate edge | — | yes | — | — | — |
| cycle | yes (`onConnect`) | yes | yes | — | — |
| cardinality | — | no | warning only | — | — |

**An invalid wire can be created by three paths that bypass every check:**
`store.pasteNodesAndEdges` (store.ts:259-267 — appends nodes and edges with no validation at all),
`store.importGraph` (store.ts:269-274, fed by `sessionStorage['vb:import-graph']` at
VisualBuilderPage.tsx:38-51 and by GraphView), and `POST /api/orgs/{org}/visual/render` /
`/validate` which accept an arbitrary graph document.

---

## 4. Findings

### F1 — CRITICAL: the golden corpus is not valid Alloy; the schema it is generated against is inverted relative to the shipped schema

**Evidence.** `internal/visual/render_test.go:23-111` defines `corpusSchema()`; `web/src/visual/renderTS.test.ts:32-186`
and `web/tests/fixtures/schema-fixture.ts` are two more hand-maintained copies. All three declare, e.g.:

| component | fixture | shipped artifact (`alloy-v1.18.1.json`) |
|---|---|---|
| `prometheus.scrape` | inputs `[targets]`, outputs `[metrics]` | inputs `[targets, forward_to]`, outputs `[]` |
| `prometheus.remote_write` | inputs `[receiver]`, outputs `[]` | inputs `[]`, outputs `[receiver]` |
| `loki.source.file` | inputs `[]`, outputs `[logs]` | inputs `[targets, forward_to]`, outputs `[]` |
| `loki.write` | inputs `[receiver]` | outputs `[receiver]` |
| `otelcol.processor.batch` | 3 named in / 3 named out (`input.metrics`…) | 1 **unnamed** `otel.any` in, 1 out `input` |

Running the real validator on the goldens:

```
$ for f in internal/visual/testdata/corpus/*.golden.alloy; do docker run --rm -v ...:/c:ro \
    --entrypoint /bin/alloy grafana/alloy:v1.18.1 validate --stability.level=experimental /c/$(basename $f); done
FAIL bindings-secret      FAIL fanin-fanout    FAIL kitchen-sink
FAIL label-edgecases      FAIL logs-chain      FAIL minimal-scrape
FAIL nested-blocks        FAIL otel-three-signals
PASS disabled-node   (passes only because its one wired node is disabled)
```

Representative errors:

```
minimal-scrape.golden.alloy:7:1:  missing required attribute "forward_to"
minimal-scrape.golden.alloy:12:3: unrecognized attribute name "receiver"
otel-three-signals.golden.alloy:7:3: attribute names may only consist of a single identifier with no "."
bindings-secret.golden.alloy:7:11: expected block label, got [        // endpoint[0].url = ...
```

8/9 goldens, 30 real errors. Every "byte-exact corpus" assertion in `render_test.go:149-166` and
`renderTS.test.ts:220-228` is therefore an assertion that the renderers reproduce config Alloy rejects.

**Why it matters.** This is the load-bearing artifact of the whole subsystem: `Makefile:257-262`
(`generate-corpus`) copies the Go goldens into `web/src/visual/__fixtures__/corpus/`, and the TS test asserts
against those same bytes. That is the *only* mechanism proving the two renderers agree. It is a real
mechanism (§7), but it certifies behaviour on inputs that cannot occur.

**Direction.** Generate the corpus from the shipped artifact + overlay (the same payload the server serves),
and add a CI step that pipes every golden through the pinned `grafana/alloy` image's `validate`. Delete the
three hand-maintained fixture schemas in favour of one derived from `internal/schema/artifacts/`.

---

### F2 — CRITICAL: wires to unnamed input ports are silently discarded (46/184 components — all of OTel)

**Evidence.** `schemaAdapter.ts:28-30` resolves an unnamed port to `p0`; `l1.ts:83,121` uses the same rule;
both renderers use the raw `prop`:

```go
// internal/visual/render.go:280
if e.To.Node == id && e.To.Port == in.Prop {     // in.Prop == "" for 46 components
```
```ts
// web/src/visual/renderTS.ts:137
.filter((e) => e.to.node === id && e.to.port === input.prop && ids.has(e.from.node))
```

Probe with the real schema (three-node OTel chain, handles as the canvas writes them):

```
---- CASE C (otel, edges use handle ids p0)
Go  diags=[]                      TS diagnostics: []          L1 diagnostics: []
otelcol.exporter.otlp "remote" { }
otelcol.processor.batch "batcher" { }
otelcol.receiver.otlp "ingest" { }          ← both wires gone, no warning anywhere
```

If the port id were `""` instead, the output is syntactically broken rather than empty
(Go: `   = [otelcol.exporter.otlp.remote.input]`; TS: `  undefined = [...]`), so there is no correct
behaviour reachable — the schema simply lacks the name, because in real Alloy the consumer list lives in an
`output { metrics = [...] }` **block**, not an attribute.

Quantified: `components with >=1 unnamed input: 46` of 184; all 46 unnamed inputs are `otel.any` except
`faro.receiver`'s `loki.logs`.

Downstream, `alloy validate` does catch the empty result (`missing required block "output"` /
`missing required block "client"`), so the user gets a Stage-2 error at save time that they cannot fix from
the canvas. **The OTel half of the product is unbuildable.**

**Direction.** Either give the extractor a way to name block-nested consumer ports (e.g. synthesise
`prop: "output"` with a block-typed marker) plus block emission in the renderers, or have the renderers use
the same `portHandleId` rule and refuse to render a graph with unresolvable ports (a loud diagnostic beats
silent loss).

---

### F3 — CRITICAL: nested blocks are unrepresentable, so most destinations render as no-ops that pass validation

**Evidence.** `props` is a flat map (`types.ts:17`, `render.go:26`). `render.go` declares `BlockSchema`
(render.go:87-91) and `ComponentSchema.Blocks` (render.go:73) and **never reads them**; `renderTS.ts` has no
block handling at all. `InspectorPanel.tsx` only iterates `attributes` (lines 77, 91, 101). 128 of 184
components have nested blocks (423 block definitions total).

`prometheus.remote_write`'s `url` lives in an `endpoint` block. There is no way to set it. The corpus's
attempt — a binding with `prop: "endpoint[0].url"` — is a **syntax error** (proved in F1).

Consequence, verified end to end: a graph containing a lone unwired `prometheus.remote_write` produces

* `l1.validateGraph(...)` → `[]` (zero diagnostics — see F5 for why),
* render → `prometheus.remote_write "sink" {\n}\n`,
* `alloy validate` → **passes**,
* runtime → drops every sample.

That is the worst failure mode available: a pipeline that is green at every layer and does nothing.

**Direction.** Extend the graph document with a block representation (`props` values keyed by block name
holding objects/arrays), emit them in both renderers, and surface them in the inspector. Until then, mark
block-requiring components as unavailable in the palette rather than shipping a component that cannot work.

---

### F4 — HIGH: L1's `required_attr_missing` fires on 42/184 components even when the input is correctly wired

**Evidence.** `l1.ts:61` skips the required check only for `secret` type or a truthy `attr.input_type`.
**Zero attributes in the shipped artifact carry `input_type`** (`attrs with input_type: 0`), and 51
(component, attribute) pairs are `required` attributes whose name is also an input `prop`. Probe against the
real schema with a *correct*, fully wired pipeline:

```
WIRED: [
 "error:required_attr_missing:prometheus.scrape \"app\" is missing required attribute \"targets\"",
 "error:required_attr_missing:prometheus.scrape \"app\" is missing required attribute \"forward_to\""
]
components that ALWAYS emit a false required_attr_missing: 42 of 184
  database_observability.*, discovery.relabel, local.file_match, loki.enrich, loki.process,
  loki.relabel, loki.secretfilter, loki.source.*, prometheus.scrape, pyroscope.scrape, ...
```

**Why it matters.** The Problems panel is permanently red for essentially every real pipeline, which trains
users to ignore it — and it makes the genuinely useful L1 errors invisible. It also means the `input_type`
escape hatch is dead code: someone designed for it, and the extractor never emits it.

**Direction.** Compute the required-attribute check against `props ∪ wired inputs ∪ bindings`, not `props`
alone (the information is already in the same function — `incoming` is built 12 lines later).

---

### F5 — HIGH: `terminal_ok` is applied to the wrong end of the wire, so an orphan destination raises nothing

**Evidence.** `l1.ts:150` suppresses `output_nowhere` for `terminal_ok` components. The overlay sets
`terminal_ok: true` on 24 components — every one of them a *destination* (`prometheus.remote_write`,
`loki.write`, `pyroscope.write`, all `otelcol.exporter.*`). But under the reference model (§3.3) a
destination is a graph **root**: its `receiver` export going nowhere means *nothing forwards data to it*.
Suppressing that warning suppresses exactly the useful signal. The design doc's rationale
(line 157: "`prometheus.exporter.self` legitimately exports without local consumption") describes a
data-flow model, not the model that shipped.

Meanwhile `no_destination` (`l1.ts:164-173`) only checks that *a node of category `destinations` exists* —
not that it is reachable. Probe: `validateGraph(doc([prometheus.remote_write]))` → `[]`.

**Direction.** Under the export→argument model, the meaningful checks are "every destination's export is
consumed by at least one wire" and "every source's export reaches a destination transitively". `terminal_ok`
should gate *inputs* with no wires, or be redefined.

---

### F6 — HIGH: L1 diagnostics do not block saving; the save gate catches almost nothing the builder produces

**Evidence.** `Toolbar.tsx:95`: `const canSave = !matchersRequired && !saveMutation.isPending;` — the
diagnostics count is displayed (Toolbar.tsx:176) but never gates. The save path (`Toolbar.tsx:56-59`) only
rejects on **render** diagnostics, and `visual.Render` emits exactly four codes: `label_collision`,
`secret_by_value`, `unknown_component`, `empty_binding_*`. So `type_mismatch`, `cycle`, `dangling_input`,
`no_destination`, `required_attr_missing`, `experimental_gated` are all advisory.

Layer map:

| Layer | Where | Catches | Blind to |
|---|---|---|---|
| Connect-time | `CanvasPane.tsx:270-284,319-343` | port existence, type, self-loop, cycle | everything on paste/import/API |
| L1 | `l1.ts:11-175` | labels, secrets, experimental, cycle, dangling, type, cardinality, destination-presence | blocks, unnamed ports (thinks they're fine), reachability; **and is advisory** |
| Render | `render.go:156` / `renderTS.ts:46` | label collision, literal secret, unknown component, empty binding (Go only) | dropped wires, missing blocks, invalid durations |
| Stage 1 | `validate.go:72` | Alloy syntax | semantics |
| Stage 2 | `validate.go:99` | full `alloy validate` — the real gate | **no-op when `alloy_binary` is unset** (validate.go:100-102); no viper default (config.go:157-172), so a self-hosted deployment that doesn't use the Helm chart silently loses it |
| Stage 3 | `rpc_pipeline.go:593` | merged-config validation across affected collectors | only runs on **enable/disable**, not create/update |

Net answer to "can a user save a pipeline that cannot work?" — **yes, trivially**: an empty graph, an
unconnected destination, or a destination with no endpoint block all save cleanly and validate cleanly.

**Direction.** Gate save on L1 errors (after fixing F4, which currently makes that impossible), and add a
reachability rule. Give `validate.alloy_binary` a default and fail loudly at startup if the binary is absent.

---

### F7 — HIGH: saved graphs do not round-trip; the builder reloads from parsed text, not from `wizard_state`

**Evidence.** Save writes the graph: `Toolbar.tsx:73` `wizardState: doc`. Load ignores it —
`VisualBuilderPage.tsx:63-71` says so in a comment ("this intentionally does not read `wizard_state` … there
is no read path for it today") and line 116 calls `graphView`, which is `ParseAlloy(contents)` (parse.go:27).

What the text→graph→canvas trip destroys:

* **node ids** — regenerated as `n_<component>_<label>` (parse.go:63), so every reload renames every node;
* **positions** — regenerated on a 280×200 grid (parse.go:52, 82-86);
* **notes, `disabled` flags, viewport, `meta.created_with`** — dropped (`disabled` nodes exist only as a
  `// node "x" disabled` comment which the parser ignores);
* **bindings** — `extractProps` keeps only `*ast.LiteralExpr` (parse.go:148), so a binding expression such as
  `convert.nonsensitive(remote.kubernetes.secret.s.data["url"])` (a `CallExpr`) is dropped entirely and does
  not come back as a binding either;
* **non-scalar props** — maps, arrays, and any nested block are dropped by the same filter;
* **edge `order`**.

So the answer to "does a saved graph reload byte-faithfully?" is **no** — not even close. Because
`checkVisualRenderMatch` (rpc_pipeline.go:169-173) compares the *re-render of `wizard_state`* to `contents`,
a load→save cycle can also produce `AlreadyExists`/render-mismatch failures once the reparsed graph differs
from the original.

**Direction.** Return `wizard_state` from `GetPipeline` (or a dedicated read RPC) and load from it, keeping
`ParseAlloy` for the read-only "view as graph" of hand-written pipelines only.

---

### F8 — HIGH: `ParseAlloy` never marks anything opaque, so the "couldn't be fully mapped" guard never fires

**Evidence.** The doc comment (parse.go:22) promises *"Anything that can't be mapped is left in the graph with
an `opaque` component name"*. `grep` shows the only mention of `opaque.` in the file is the **check**
(parse.go:132); nothing ever produces such a node. `Opaque` is therefore `true` only for a syntax error
(parse.go:36). `VisualBuilderPage.tsx:118-124` relies on `gv.opaque` to refuse loading a partially-mapped
pipeline — that safety net is dead code.

Additional drops: `declare` blocks, `import.*`, `logging`/`tracing` config blocks, `foreach`, any nested
block, and any expression that is not a bare literal or an access-expression chain, all silently become
either a node with empty props or nothing at all. Combined with F7 this means opening a hand-written
pipeline in the graph view shows a plausible-looking but materially incomplete picture with no warning.

**Direction.** Emit an explicit `opaque.*` node (or a warning) for every statement, block, or attribute the
parser cannot represent, and set `Opaque` accordingly.

---

### F9 — MEDIUM: the two renderers disagree on every path the corpus does not exercise

Measured, not inferred (Go via a `visual.Render` harness, TS via `renderTS` in vitest, both with the real schema):

| input | `internal/visual/render.go` | `web/src/visual/renderTS.ts` |
|---|---|---|
| `props: {x: null}` | `x = "<nil>"` | `x = "null"` |
| label `"a😀b"` | `"a_b"` (per-rune, render.go:111) | `"a__b"` (per-UTF-16-unit, renderTS.ts:24) |
| binding with blank `prop`/`expr` | `empty_binding_expr` diagnostic, line skipped (render.go:305-313) | emits `   =   `, **no diagnostic** (renderTS.ts:146-147) |
| component not in schema | `unknown_component` diagnostic (render.go:253-257) | none |
| cyclic graph (leftover nodes) | deterministic — `sort.Slice(rest, less)` (render.go:235) | node-array order; permutation-dependent (renderTS.ts:105) |
| map prop key order | `sort.Strings` (byte order, render.go:351) | `localeCompare` (renderTS.ts:39) |

The cyclic case, measured:

```
Go  CYCLE-A and CYCLE-B (nodes reversed): identical output
TS  === G order ===  zzz then aaa      === G reversed ===  aaa then zzz
```

None of these are covered (§7). The Go/TS agreement is real on the 9 corpus graphs and unverified everywhere else.

**Direction.** Drive parity from a shared property test (random graphs → both renderers → assert equal),
not from 9 curated fixtures. `renderTS.ts:105` should also sort leftovers with the same comparator.

---

### MEDIUM: `serialize` coerces `duration` values to quoted strings without unit validation

`render.go:330-332` / `renderTS.ts:32`: `if type === 'duration'` → `quote(String(value))`. A numeric
`scrape_interval: 30` becomes `scrape_interval = "30"`. 125 attributes across the artifact are duration-typed.

```
$ alloy validate caseE.alloy
Error: /c/caseE.alloy:4:21: time: missing unit in duration "30"
```

Stage 2 catches it, so it is not a production outage — but the message points at a value the user never typed
in that form, and this branch is completely untested (§7, mutation M5b/T4 both survive).

### MEDIUM: `props: {x: null}` renders as the literal string `"<nil>"` / `"null"`

Both renderers stringify a JSON `null` into a quoted string rather than omitting the attribute. Alloy accepts
`job_name = "<nil>"` happily. Nothing catches it.

### MEDIUM: `store.pasteNodesAndEdges` and `importGraph` bypass every connectivity rule

`store.ts:259-274` append arbitrary nodes and edges with no type check, no cycle check, no duplicate check,
no port-existence check — the only paths that reach `addEdge`'s guards are `CanvasPane.onConnect`. Paste is
reachable from ⌘V; import is reachable from `sessionStorage['vb:import-graph']` (VisualBuilderPage.tsx:38-51)
and from GraphView.

### MEDIUM: the dev seed ships an invalid pipeline and a graph that contradicts its own contents

`internal/cli/dev.go:85` stores a `wizard_state` that is **correct for the real schema**
(edge `e2`: `remote_write.receiver → scrape.forward_to`). `dev.go:87-104` stores contents that were
*"verified via visual.Render against the corpus test schema"* — i.e. `receiver = [prometheus.scrape.demo.metrics]`,
no `forward_to`. Three consequences: (a) the contents fail `alloy validate` (same shape as `minimal-scrape`);
(b) `checkVisualRenderMatch` will reject any re-save of the seeded pipeline; (c) the graph view re-parses the
contents and shows an edge in the **opposite** direction from the stored graph. The comment's own hedge —
"stage-1 valid on its own" — concedes that stage-2 validity was never checked.

### LOW: header comment says "graph revision N" but N is `len(doc.Nodes)`

`render.go:243` / `renderTS.ts:108`. Adding a node changes the "revision"; toggling `disabled` does not.
It is a node count with a misleading name, and it is embedded in every served config.

### LOW: `UpgradeCheck` compares ports positionally

`upgrade.go:169-190` compares `oldDef.Inputs[i]` with `newDef.Inputs[i]`. Inserting a port anywhere but the
end produces spurious `port_type_changed` items for everything after it. Given that 46 components have
unnamed ports there is no name to compare on either — but position is the wrong key regardless.

### LOW: an empty graph saves

`l1.ts:164` guards `no_destination` behind `doc.nodes.length > 0`. An empty graph renders to just the header
comment, which Stage 1 parses and Stage 2 accepts.

---

## 5. What is genuinely good and should be preserved

* **The reference model is right.** `inputs = reference-accepting arguments`, `outputs = exports`, edge =
  export→argument, emitted inside the consumer's block. This matches Alloy exactly, and Case A proves it end
  to end against the real binary. Do not "fix" the direction by inverting the model; fix the *presentation*.
* **Determinism.** The Kahn sort with the `(category, component, label)` tie-break is genuinely
  permutation-invariant, and the tests for it are real: removing the tie-break (`M6`) and removing the fan-in
  `SliceStable` (`M7`) both fail the Go suite.
* **`render` is total.** It never panics on malformed input; missing components, missing ports, empty props
  all produce output plus (sometimes) a diagnostic.
* **`node_map`.** Both implementations compute identical 1-based line ranges (verified: `startLine=3, endLine=4`
  for the first block in both), and `rpc_visual.go:118-125` uses it to attribute Stage-2 diagnostics back to
  nodes. That is a nice piece of engineering.
* **Server-authoritative contents.** `Toolbar.tsx:56` renders server-side and saves *that*; the client render
  is preview only. `checkVisualRenderMatch` (rpc_pipeline.go:144-174) then re-derives the contents from
  `wizard_state` and refuses a mismatch. Good defence in depth.
* **The "Verify render" button** (BottomDrawer.tsx:45-51) diffs client vs server output on demand — a real
  parity check, just a manual one.
* **`GEN_GOLDENS` discipline.** `gen_goldens_test.go:23-25` skips unless explicitly enabled, with a comment
  that says "Do not run this to make failing corpus tests pass — fix the renderer instead." Right instinct;
  it just cannot help when the *schema* the goldens are generated against is the thing that's wrong.
* **The overlay/artifact split** and the CI check that overlay keys must exist in the artifact
  (`registry.go:118-150`) are a sound way to keep hand-curation from drifting.

---

## 6. Test-coverage assessment

Baseline: `go test ./internal/visual/` → ok; `npx vitest run src/visual` → 65 tests, 6 files, all passing.

### Mutation results (each mutation applied, suite run, then `git checkout` to restore)

| # | Mutation | Result |
|---|---|---|
| M1 | `render.go:280` also matches unnamed input ports (`\|\| in.Prop == ""`) | **survives** |
| M2 | `render.go:305-313` empty-binding diagnostics removed | **survives** |
| M3 | `render.go:235` deterministic sort of cyclic leftovers removed | **survives** |
| M4 | `render.go:253-257` `unknown_component` diagnostic removed | fails ✔ |
| M5b | `render.go:330` duration branch disabled | **survives** |
| M6 | `render.go:125-137` category order changed | fails ✔ |
| M7 | `render.go:290` fan-in `SliceStable` removed | fails ✔ |
| T1 | `renderTS.ts:43` null → `'<nil>'` (i.e. made to match Go) | **survives** |
| T2 | `renderTS.ts:54` label-collision early return disabled | **survives** |
| T3 | `renderTS.ts:119` `secret_by_value` guard disabled | **survives** |
| T4 | `renderTS.ts:32` duration quoting removed | **survives** |
| T5 | `l1.ts:131` `dangling_input` rule removed | fails ✔ (2 tests) |
| T6 | `l1.ts:192` `portsCompatible` always true | fails ✔ (3 tests) |

8 of 13 mutations survive. Notably **the TS renderer has no test for label collision, secret-by-value,
duration handling, or null handling** — its only coverage is the 9 corpus goldens, so every diagnostic path
and every non-string value type is unguarded. The Go side has unit tests for collision (render_test.go:278)
and secrets (render_test.go:314) but none for bindings, unnamed ports, durations, or cyclic ordering.

### What is proven

* Both renderers reproduce 9 specific graphs byte-for-byte against a specific fixture schema.
* Determinism under node/edge shuffling for 3 of those graphs (Go seed 42; TS uses a hand-rolled LCG at
  `renderTS.test.ts:189-202` — a *different* shuffle, so the two suites do not even test the same permutations).
* `node_map` ranges are well-formed and non-overlapping (render_test.go:199-244).
* L1's rule table fires for each of its 12 codes (l1.test.ts) — against the fixture schema.
* `portHandleId`'s three branches (schemaAdapter.test.ts).

### What only appears proven

* **"Codegen is correct."** It reproduces goldens that `alloy validate` rejects (F1).
* **"Go and TS agree."** Only on 9 happy-path graphs; six measured divergences elsewhere (F9).
* **"L1 catches invalid pipelines."** Against the real schema it produces false errors on 42 components (F4)
  and misses orphaned destinations entirely (F5).
* **`render_test.go:146-148, 169-171, 276-277, 313-314`** carry "RED-RUN PROOF" comments claiming specific
  mutations fail. M6 and M7 confirm two of them. The comments do not cover the eight surviving mutations, and
  a reader could easily take the presence of such comments as evidence of broader mutation discipline than exists.
* **`parse_test.go`** asserts against `receiver = [prometheus.scrape.app.metrics]` (parse_test.go:38) — content
  that cannot exist in a real Alloy file. The parser is schema-agnostic so it passes either way, but the test
  documents the wrong world.

### What is unverified

* Anything involving the shipped 184-component schema. `grep` shows no test anywhere loads
  `internal/schema/artifacts/alloy-v1.18.1.json` into the renderers or into L1. All 10 Playwright
  `visual-*.spec.ts` files (1085 lines) seed `schemaFixture` via `api.seed({... schema: schemaFixture })`.
* Nested blocks, secrets end to end, bindings in TS, `edge.order` as produced by the UI (never produced),
  paste/import validation, `store.addEdge`'s guards (store.test.ts covers history and
  `selectConnectionState` only — never `addEdge`).
* Whether generated output validates. No test in the repo runs `alloy validate` on renderer output.
* `web/tests/fullstack/walkthrough.spec.ts:170-206` is the only test touching the real schema path, and it
  self-skips: `test.skip(!demo, 'demo-visual pipeline not seeded')` (line 184) — and the pipeline it would
  exercise is itself invalid (F-dev-seed above).

---

## 7. Open questions for the maintainers

1. **Which schema is authoritative for tests?** If it is `internal/schema/artifacts/`, the entire corpus and
   all three fixture schemas need regenerating and most L1 rules need rework. If the fixtures are meant to
   stand in for something, what?
2. **Was the inverted fixture model deliberate at some point?** The design doc (§3.1, line 141) describes the
   reference model correctly, the extractor implements it correctly, and only the fixtures invert it. Was
   there an earlier data-flow-oriented design that the fixtures are a fossil of?
3. **How is the canvas supposed to read?** With export→argument edges, destinations sit at the graph root and
   wires run right-to-left. Is the intent to keep the semantic direction and flip the visual layout
   (destinations on the left, or reversed handle sides), or to keep the layout and reverse edges at render time?
4. **What is the plan for nested blocks?** 70% of components need them, and the design doc promises them
   ("nested blocks in inspector order", line 185). Without them, is the palette supposed to be restricted?
5. **What was `input_type` for?** `l1.ts:61` depends on it and the extractor never emits it.
6. **Should `wizard_state` become the load path?** It is written on every save and read by nothing except the
   render-equality check. Adding it to `GetPipeline`'s response would fix F7 outright.
7. **Is `edge.order` a live feature?** Nothing writes it; the Go test exercises it via a hand-edited fixture.
8. **Should L1 errors block save?** Today they do not. If yes, F4 must be fixed first.
9. **What is the intended behaviour when a port cannot be resolved at render time — drop, or refuse?** Today
   both renderers drop silently, which is how F2 stayed invisible.

---

## Appendix: reproduction notes

* Real-schema renders were produced with a temporary `main.go` under the module root importing
  `shepherd/internal/visual`, loading `internal/schema/artifacts/alloy-v1.18.1.json` deep-merged with
  `overlay.json`. TS equivalents were produced with a temporary vitest file under `web/src/visual/`.
  Both were deleted; `git status` is clean apart from a pre-existing `internal/spa/dist/BUILD_INFO.json`
  modification and untracked files belonging to other work.
* Alloy validation used the pinned image: `docker run --rm -v <dir>:/c:ro --entrypoint /bin/alloy
  grafana/alloy:v1.18.1 validate --stability.level=experimental /c/<file>` — the same binary and stability
  level `internal/validate/validate.go:119-120` invokes.
* Mutations were applied with `perl -pi`, verified with `grep`, and reverted with `git checkout <file>`.
