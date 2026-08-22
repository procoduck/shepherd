# Visual builder refinement — review findings and implementation spec

> Review date: 2026-08-19. Sources: full code review of `web/src/visual/` and
> `tools/alloy-schema-gen/`, the design-pass mockups (artifact "Visual Builder
> Refinement" — refined layout + connection-drag states), and the open HIGH
> findings from `docs/archive/vb1-progress.md` (R1-H1, R1-H2, R3-H5).
> Companion tracking: `docs/project-status.md`.

## A. Canvas & connection UX — draw.io-grade drag affordance

**A1 — Handle-ID mismatch makes some ports unconnectable (R1-H1, bug).**
`PipelineNode.tsx` renders handles with `id={p.prop ?? String(i)}` /
`id={p.export ?? String(i)}`, but `isValidConnection` (CanvasPane), edge
building, and `l1.ts` validation all look ports up by `p.export`/`p.prop`
alone. A port whose schema entry has no `prop`/`export` name gets handle id
`"0"` which never matches — the wire is silently rejected. Fix: one exported
resolver `portHandleId(port, index)` (in `schemaAdapter.ts`), used by handle
rendering, `isValidConnection`, `onConnectStart` wire-type lookup, edge
`sourceHandle`/`targetHandle` construction, and `l1.ts` lookups. Add a vitest
regression with a schema fixture whose port has no name.

**A2 — Node-level green target affordance (the draw.io ask).**
Today only the 10px handle dot gets a green ring; the node body of a valid
target gives no positive signal. Per the "Connection drag states" artboard:
- While dragging from a port, every node with ≥1 compatible port gets a
  **green outline** (`border-color #22c55e` + `box-shadow 0 0 0 2px
  rgba(34,197,94,.3)`), its compatible port(s) **enlarge** (~16px) with a
  green ring, and the port label turns green.
- Hovering within snap range of a valid port snaps the wire
  (`connectionRadius={30}` on `<ReactFlow>`), fills the card with a green
  tint (`#131f17`), thickens the outline, and shows `✓` on the port label.
- Nodes with **no** compatible port dim (opacity ~0.35) — exists today, keep.
- The in-flight connection line takes the source port's wire color (custom
  `connectionLineComponent` or `connectionLineStyle`), replacing the default
  gray bezier.

**A3 — Drag-state performance (R1-H2, prereq for A2).**
`connectingFrom` currently rides inside every node's `data` and sits in the
`rfNodes` useMemo deps — every drag start/end re-renders every node, and A2
would make that worse. Move the drag state into the zustand store
(`connectingFrom`), drop it from node data entirely, and have `PipelineNode`
subscribe via a narrow selector that computes only two booleans for itself
(`isValidTarget`, `isDimmed`) so a drag re-renders only nodes whose booleans
change. `rfNodes` memo must no longer depend on the drag state.

**A4 — One source of truth for wire/category colors.**
Three hand-maintained tables exist: `WIRE_COLOR` (tailwind classes,
schemaAdapter.ts), `FLOW_COLORS` (hex, CanvasPane.tsx — same data, different
format), `CATEGORY_BORDER` (schemaAdapter.ts). Move colors into the overlay
(`wire_types.<id>.color` hex, new `categories.<id>.color` section), serve
them through `GET /api/schema/*` (merge already passes overlay through), and
derive all frontend styling from the schema payload. Delete all three tables.

## B. Look & feel — design-pass findings (see the mockup artifact)

**B1 — CRITICAL: the builder's styling classes don't exist.**
`bg-card`, `bg-background`, `bg-accent`, `text-muted-foreground` are used
throughout PipelineNode/Palette/Toolbar/InspectorPanel/BottomDrawer but no
`@theme` defines them (Tailwind v4, `web/src/index.css` is 13 lines,
`components/ui/` is empty) — the built CSS contains **zero** rules for them.
Node cards render transparent over the dot grid; palette hover does nothing.
Fix: add a `@theme` block in `index.css` defining the token layer used by the
mock — background `#09090b`, panel `#0e0e11`, card `#18181b`, borders
`#27272a`/`#3f3f46`, muted text `#a1a1aa`/`#71717a`, accent `#6366f1` — as
`--color-background/-panel/-card/-border/-border-strong/-muted/-accent` and
migrate the phantom classes onto them. Add a CI-able guard: a vitest that
compiles the CSS and asserts the classes used in `web/src/visual/` resolve
(or a simpler grep-based check in `make lint`'s web step).

**B2 — Node card anatomy** (per "Refined builder layout" artboard): 8px
radius, card background + shadow, 3px category-colored left border, header
row with a small stroke SVG category icon + mono component name, label row,
port dots 12px with 2px page-background ring, selected = indigo border +
soft indigo shadow. Error state keeps the red left border.

**B3 — Palette**: category color dot per row, real hover background, section
headers 10px/600/tracked, search input styled per token layer.

**B4 — Toolbar is non-functional (critical UX gap).** The name input is dead
and **Save does not save** — it calls `renderVisual` and toasts "Render
verified"; no pipeline is created or updated. Rework per mock:
- Wire the name field to store state (used as pipeline name).
- **Matchers chip editor** in the toolbar (add/remove `key="value"` chips) —
  a visual pipeline saved without matchers serves nothing, so the editor must
  make matchers visible and require ≥1 before enabling Save.
- Save = render server-side, then create/update the pipeline through the
  generated `PipelineService` Connect client with `contents` from render,
  `wizard_state` = graph document, `source='visual'`, matchers from the chip
  editor; navigate to the pipeline on success; org from the active-org hook,
  not `me.orgs[0]`.
- Load: opening `/pipelines/$id/visual` must fetch the pipeline and
  `importGraph` its `wizard_state` (today only the sessionStorage recreate
  path imports anything).
- Persistent validity summary chip (green check / n problems) instead of the
  error count appearing only when nonzero.

**B5 — Bottom drawer**: collapsed state is a 32px status bar showing
"Problems n · Generated config · Simulation" as tabs (drawer already
defaults collapsed; refine the bar content + `⌃\`` hint).

## C. Schema generation — version bump must be one edit + one command

**C1 — `make schema` target is missing.** `tools/alloy-schema-gen/run.sh`
documents "Usage: make schema" but no such target exists. Add it (runs
run.sh; document that it needs network + git).

**C2 — The Alloy version is pinned in five places; three are stale.**
`deploy/versions.env` (v1.18.1, canonical) vs `internal/version/alloy.go`
(hand-edited const `alloy-v1.18.1`), Dockerfile/Dockerfile.local/
Dockerfile.goreleaser ARG defaults (`v1.12.2`, stale — masked because make
passes build-args), e2e compose alloy image (`v1.12.2`, hardcoded — the e2e
fleet runs a different Alloy than the schema targets!), AGENTS.md image
table (v1.12.2). Fix: `versions.env` stays the single source; a small
`make generate`-time step writes `internal/version/alloy_gen.go` from it
(replacing the hand const); Dockerfile ARG defaults updated and the
`check-docker` guard extended to diff ARG defaults against versions.env;
e2e compose uses `${ALLOY_IMAGE:-grafana/alloy:v1.18.1}`; AGENTS.md table
refreshed.

**C3 — Overlay reconciliation on bump.** The 184-entry overlay is editorial
(docs/icons/categories) but nothing scaffolds it on a version bump: new
components silently land in "advanced" with no docs; removed ones fail CI.
Extend the pipeline: after generating the artifact, `run.sh` (or a small Go
tool it calls) reconciles `overlay.json` — appends skeleton entries for new
components (category heuristic from the component path: `*.exporter.*`/
`discovery.*`/`*.source.*` → sources; `*.relabel`/`*.process*` → transform;
`*.remote_write`/`*.write`/`otelcol.exporter.*` → destinations;
`remote.*`/`local.*` → config; else advanced) marked `"needs_review": true`,
deletes orphans, and prints a summary. Editorial fields on existing entries
are never touched. The bump procedure then is: edit `ALLOY_VERSION` in
`deploy/versions.env` → `make schema` → review `needs_review` entries →
commit. Document this in `tools/alloy-schema-gen/README.md`.

**C4 — Colors in the overlay (backend half of A4).** Add `color` to each
`wire_types` entry and a `categories` section (id → {color, label}) in the
overlay; schema merge/serving passes them through untouched; the existing
schema Ginkgo suite gains assertions that every wire type and category
carries a color.

## D. Adjacent gaps folded in

**D1 (R3-H5)** — dev seed gains one demo **visual** pipeline (valid graph in
`wizard_state`, `source='visual'`, real matchers) so the builder opens with
an example to explore — reuse a corpus graph (e.g. the minimal
discovery→scrape→remote_write one).

**D2** — Playwright specs updated for all of the above (drag highlight
states via `data-` attributes so specs don't assert raw classes; toolbar
save flow against MSW Connect mocks; token guard).

## Out of scope (unchanged)

S3 sandbox simulation (VB-1 M7), R1 canvas virtualization beyond A3,
mobile/touch support, light theme.
