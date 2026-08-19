# Visual builder — deep review (2026-08-19)

Three independent reviews of the visual pipeline builder and the schema pipeline that feeds
it. Each was written by a separate reviewer working in a **fresh context**, deliberately not
told what the others (or earlier in-repo analyses) had concluded, and instructed to verify by
execution rather than inspection. No source was changed; these are assessments only.

| Document | Scope |
|---|---|
| [`schema-generation.md`](schema-generation.md) | Extractor → artifact → overlay → serving → consumption; the `make schema` bump story |
| [`graph-model-and-validation.md`](graph-model-and-validation.md) | Port identity, connectivity rules, pipeline construction, all validation layers, both code generators, text→graph parsing |
| [`canvas-ux-and-forms.md`](canvas-ux-and-forms.md) | Canvas layout and interaction, palette/search, inspector forms, defaults and prefill, diagnostics feedback |
| [`canvas-framework-evaluation.md`](canvas-framework-evaluation.md) | **Is React Flow the right library?** Defect census by true root cause, the single integration anti-pattern behind every canvas bug, alternatives assessed |

---

## Combined verdict

**The visual builder cannot currently produce a working pipeline, and the test suite cannot
tell you that.** Generation of the Alloy component schema is sound — reproducible, complete,
184/184 components. Everything downstream of it was built and tested against a different,
fictional component model, so the two halves of the system have never actually met.

The three reviews were independent and converged on the same root cause from opposite ends.
That convergence is the most important signal in this set.

---

## The root cause: three fixture schemas that contradict the shipped one

`internal/visual/render_test.go:23`, `web/src/visual/renderTS.test.ts:32` and
`web/tests/fixtures/schema-fixture.ts` each hand-write a small component schema, and all three
**invert the port model for terminal components**:

| Component | Fixture says | Shipped artifact (and real Alloy) says |
|---|---|---|
| `prometheus.remote_write` | input `receiver` | **output** `receiver`; no inputs |
| `prometheus.scrape` | output `metrics` | **no outputs**; inputs `targets`, `forward_to` |

`schema-fixture.ts` is imported by 13 files, including all ten Playwright visual specs. The
consequences measured by the reviewers:

- **0 of 9** corpus graphs render to their committed golden when run against the real artifact
- **8 of 9** goldens are rejected by real `alloy validate` in the v1.18.1 container — 30 errors
- Corrupting port names **in the committed artifact** leaves the Go schema, visual and mgmtapi
  suites **fully green**
- **8 of 13** deliberate mutations survive the test suite
- All 52 mocked Playwright visual specs and 155 unit tests pass with four critical UX blockers present

Nothing anywhere loads the shipped schema, and nothing ever runs `alloy validate` on renderer
output. The single existing counter-example — `internal/cli/dev_test.go:142` — is the model the
rest should follow.

---

## What is actually broken, in priority order

Severities are the reviewers'; the ordering below is by what unblocks the most.

1. **Fixture schemas replaced by the real artifact** (root cause). Until this changes, no fix
   below can be proven and any of them can silently regress.
2. **Nested blocks are unrepresentable.** 128/184 components declare blocks; 2 347 of 3 371
   attributes (69.6 %) live inside them and **0 %** are rendered. `prometheus.remote_write` has
   no settable `url`. A pipeline can be green at L1, render, *and* `alloy validate`, and still
   drop every sample.
3. **Nothing can be deleted.** React Flow runs controlled but `selected` is never round-tripped,
   so its internal selection is permanently empty; no node or wire can be removed by any
   gesture. Undo is the only escape.
4. **The core wire cannot be drawn on the port.** Multi-input nodes render every handle at the
   same coordinate (measured: both of `prometheus.scrape`'s at `(736,512)`), so a precise drop
   silently fails. 14 components affected, including every scraper.
5. **Form values are untyped.** Every field is a text box storing a string; the renderer quotes
   it regardless of declared type. 200 top-level attributes are list/map/capsule. A pipeline
   built in the UI is rejected with `expected array, got string` — and saves anyway.
6. **OTel is entirely un-wirable.** 46/184 components have unnamed input ports and all 84 OTel
   ports collapse to `otel.any`; edges to them are discarded by both renderers with **zero
   diagnostics** from any layer.
7. **Graphs do not round-trip.** Save writes `wizard_state`; load ignores it and re-parses the
   generated text, losing node ids, positions, notes, disabled flags, bindings and every
   non-scalar prop.
8. **L1 is false-positive on 42 components.** The `input_type` exemption it relies on is emitted
   by zero attributes in the artifact, so a correctly wired `prometheus.scrape` shows two
   permanent blocking errors — and the obvious user "fix" emits a duplicate attribute.
9. **Extraction gaps.** 80 `alloy:",squash"` sites dropped (so `loki.write`'s endpoint block has
   no `basic_auth`/`tls_config`), no defaults, no enums, `maxDepth=4` truncation.
10. **Process gaps.** `make schema-verify` is referenced as CI-enforced in three documents and
    does not exist; CI's `go test ./internal/...` excludes `./tools/...`, so the extractor's own
    tests never run — which is how "`make schema` never worked" survived from the initial commit.

---

## Notable corrections to earlier in-repo conclusions

Recorded because they show where previous review passes were wrong, not to reopen them:

- **The load path was accepted during implementation as "equivalent but safer".** It is lossy
  (item 7). An in-flight reviewer rationalised it; a fresh-context reviewer measured it.
- **The reverse-direction edge that could not be made to render is not a React Flow bug.** The
  graph model correctly encodes Alloy's "arguments reference exports"; the *canvas* inverts it
  by drawing inputs left and outputs right, so a metrics pipeline requires dragging a
  destination's right edge leftwards into its source. This is a layout-convention decision, not
  a defect to chase.
- **`nested-blocks.golden.alloy` contains no nested blocks.** The fixture name promises coverage
  that does not exist.
- **`demoVisualContents` (`internal/cli/dev.go:92`) is a render of the old model** and now
  contradicts the graph beside it, so the drift check flags every seeded demo pipeline.

---

## What is good and should be preserved

- **Schema generation itself**: reproducible (byte-identical on regeneration bar a timestamp),
  complete (184/184 registered components), hermetic, with a clean generated/hand-maintained
  split and a fully curated overlay (184/184 docs and icons, 0 `needs_review`).
- **`reconcile.go`**: idempotent, never clobbers editorial fields, 17 real specs.
- **The renderer's reference model**: correct against real Alloy; given the shipped schema it
  produces config the real binary accepts.
- **Typed, colour-coded wires** with the four-state drag affordance; **cycle rejection**; the
  **palette compatibility filter** (153 → 13 components); the **live generated-config preview**
  with client/server diff; the zundo history tuning; `VisualBuilderPage`'s load-error paths.

---

## Status (updated 2026-08-19)

Items 1–8 of the priority list are implemented (commits `2f99b21`…`436bfc2`); the four open
questions below were answered as recorded in each commit. Still outstanding:

- ~~Canvas width~~ — done. Both builder panels collapse and the app nav renders as a rail on
  builder routes: **424px → 700px by default, 1152px fully collapsed** at 1280 wide.
- ~~CI runs no Playwright~~ — done. The mocked suite (`test-ui`) and the fullstack suite
  (`test-fullstack`, the only layer touching the real server and real schema) both run on PRs.
- ~~Hand-written stand-in schema in renderer specs~~ — done; every spec now loads the embedded
  artifact through the registry.
- ~~Stale schema served to the browser~~ — done. `/api/schema/current` was sent as
  `immutable, max-age=86400`, so the page kept reading a pre-`prop`/`role` copy from disk cache
  and every handle fell back to a synthetic `p0` index; the seeded `demo-visual` graph showed
  three nodes and no wires. Now `no-cache` with the ETag retained (B-SCHEMACACHE in the ledger).
  Verified in Chrome: `targets → targets`, `forward_to → receiver`, both edges drawn, Problems 0.
- ~~Controlled-mode contract~~ — done. The canvas hand-handled 2 of the 6 node change types React
  Flow emits and rebuilt every node object on every render, which is the single root cause behind
  items 3 and 4, the `selected` gap and the empty minimap. It now routes the whole change stream
  through `applyNodeChanges`/`applyEdgeChanges` and reconciles the arrays, preserving object
  identity so cached handle bounds survive. `GraphViewPage` got the same treatment. Full analysis
  and the alternatives assessment: [`canvas-framework-evaluation.md`](canvas-framework-evaluation.md).
- Still open: **F9-a** (the `ssh` auth kind fails in the compose stack), **F5** (VB-1 M7 sandbox
  simulation, not started), five self-skipping mocked specs that mask gaps
  (`editor-autocomplete`, `git`, `revisions`, `states` ×2), and the overlay's `needs_review`
  entries pending an editorial pass.
- Item 9 (extraction gaps) is done; item 10's `make schema-verify` now exists.

## Open questions for the maintainers

1. Should the canvas keep Alloy's export→argument direction (destinations as graph roots, wires
   running right-to-left) or present a left-to-right dataflow metaphor and invert at render
   time? This decides the node layout, the drag affordance and what `output_nowhere` should mean.
2. How should nested blocks be modelled in `props` and presented in the inspector — inline
   sub-forms, a block list, or a typed editor per block?
3. Is `wizard_state` or the generated text the source of truth on load? The design doc says the
   graph is; the code says the text is.
4. What is the minimum component set the builder must support well, given 184 is unrealistic to
   curate at once?
