# Is React Flow the right graphing framework? — evaluation (2026-08-19)

Asked because the canvas has been the most defect-prone area of the project and the framework
was the obvious suspect. Short answer: **the framework is right, and it is not the source of the
bugs.** One integration decision is, and it accounts for every canvas defect we have had.

---

## Verdict

| Question | Answer |
|---|---|
| Is `@xyflow/react` the most functional choice for this UI? | **Yes.** Nothing else covers the requirement set in a React SPA. |
| Have we hit actual React Flow bugs? | **No.** Zero of the defects trace to a library fault. |
| Is the canvas unusually bug-prone? | **Yes** — but for one specific, fixable reason. |
| Should we migrate? | **No.** Migration would carry the bug with us. |

---

## The evidence: defect census by true root cause

Every canvas/builder defect recorded in this repo, classified by what actually caused it.

### Caused by our React Flow integration — 4

All four are the *same* root cause (see next section).

| # | Defect | Status |
|---|---|---|
| 1 | Nothing can be deleted — no node or wire removable by any gesture | fixed |
| 2 | Minimap renders an empty rectangle regardless of node count (`B-MINIMAP`) | open |
| 3 | Multi-input nodes render every handle at one coordinate, so precise drops fail | fixed |
| 4 | Viewport fit fires at the wrong moment — once never at all, once mid-drag | fixed |

### Not React Flow — 11

| # | Defect | Actual cause |
|---|---|---|
| 5 | 0/9 corpus graphs match goldens; 8/9 rejected by real `alloy validate` | fixture schemas invert the shipped port model |
| 6 | 69.6% of the config surface unreachable — no nested blocks | schema model + inspector |
| 7 | Form values all strings; renderer emits `expected array, got string` | inspector forms |
| 8 | OTel entirely un-wirable, 84 ports collapse to `otel.any` | schema extractor |
| 9 | Saved graphs do not round-trip — ids, positions, notes all lost | save/load path |
| 10 | L1 false-positive on 42 components | validator |
| 11 | 80 `alloy:",squash"` sites dropped, no defaults, no enums | extractor |
| 12 | `make schema-verify` referenced in three docs, did not exist | build process |
| 13 | Canvas showed "Edges: 2" and drew none (`B-SCHEMACACHE`) | HTTP cache header |
| 14 | Port identity synthetic `p0`/`p1` (`F9-b`) | extractor emitted no port names |
| 15 | Canvas 424px wide; palette placed nodes off-screen | our layout |

**4 of 15.** And the earlier review pass explicitly *cleared* React Flow once already: the
reverse-direction edge "is not a React Flow bug" (`README.md:97`) — it was our layout convention.

The framework has been blamed for a schema cache header, a schema extractor, an inspector form
layer and a validator. It caused none of them.

---

## The one root cause: controlled mode without `applyNodeChanges`

React Flow supports two modes. In **uncontrolled** mode it owns the node array. In **controlled**
mode you own it — and you become responsible for every field the library writes back.

We run controlled. The documented pattern for that is:

```js
const onNodesChange = useCallback(
  (changes) => setNodes((nds) => applyNodeChanges(changes, nds)),
  [],
);
```

We use `applyNodeChanges` **nowhere** — verified, zero occurrences in `web/src`. Instead
`CanvasPane.tsx:287` hand-handles the change stream:

```tsx
for (const c of changes) {
  if (c.type === 'position' && c.position) updateNode(c.id, { position: c.position });
  if (c.type === 'remove') removeNode(c.id);
}
```

React Flow emits **six** node change types (`position`, `dimensions`, `select`, `remove`, `add`,
`replace`). We handle two and silently discard four. Here is what the library's own `applyChange`
does with the ones we drop:

```js
case 'select':     element.selected = change.selected;      // → we drop
case 'dimensions': element.measured  = {...change.dimensions}; // → we drop
case 'position':   element.dragging  = change.dragging;     // → we drop
```

That is the complete explanation for defects 1 and 2:

- dropped `select` → React Flow's internal selection stays permanently empty → **nothing is
  deletable**
- dropped `dimensions` → nodes never carry `measured` → `nodeHasDimensions()` rejects every node
  → **the minimap draws nothing**

### The second half — object identity

`applyNodeChanges` does something less obvious that matters more. Unchanged elements are pushed
through **by reference**; only elements with a change get a shallow copy. That reference stability
is load-bearing, because `adoptUserNodes` runs with `checkEquality: true` by default and its fast
path is strict identity:

```js
if (_options.checkEquality && userNode === internalNode?.internals.userNode) {
  nodeLookup.set(userNode.id, internalNode);   // reuse — keeps cached handle bounds
} else {
  /* full re-adopt: handle bounds re-derived, z recalculated, position re-clamped */
}
```

Our `rfNodes` memo (`CanvasPane.tsx:203`) builds a **brand-new object for every node on every
rebuild**. That fast path never hits. Every node is fully re-adopted on every render.

This is why defect 3 needed a manual `useUpdateNodeInternals` call in `PipelineNode`, and it is
why the *correct* fix for the minimap breaks connection dragging. Because our nodes carry no
`measured`, `parseHandles` returns `undefined` and discards cached handle bounds — forcing a fresh
DOM measurement on every rebuild. **The wire gestures have come to depend on that accidental
re-measure.** Supply `measured` alone and bounds are preserved stale instead: drops land nowhere
and three `visual-linking` specs fail. Measured directly, not inferred — mid-drag the handles were
observed shifting while the connection never resolved.

### Why this is so productive of bugs

This is the important finding, and it explains the *pattern* rather than any single defect:

> **Partial adoption of the controlled-mode contract is unstable.** Each field you fail to
> round-trip breaks a different feature, and the failures are silent — no error, no warning,
> no failing test. Worse, adjacent code adapts to the broken state, so fixing one field in
> isolation breaks whatever had grown to depend on the breakage.

That is exactly what happened. Round-tripping `selected` fixed deletion. Round-tripping `measured`
would fix the minimap — but the drag layer had already grown a dependency on `measured` being
absent. The contract is atomic; we have been adopting it one field at a time, and each step is a
regression risk.

Every one of these failures is also **silent by construction**, which is why the test suite never
caught them: an empty minimap, an empty selection set and a dropped connection all render as
"nothing happened" rather than as an error.

---

## Why React Flow is still the right choice

Requirements: React 18 + TS SPA, custom node bodies with forms, multiple *named* typed ports per
node, per-connection validation, minimap/zoom/pan, undo-redo, serialisable graph.

| Option | Assessment |
|---|---|
| **`@xyflow/react`** (current) | Purpose-built for exactly this. Multiple named handles, custom nodes, `isValidConnection`, minimap/controls/background, controlled mode, MIT, actively maintained. Everything we need is supported — including the four broken things, which it supports correctly today. |
| **Rete.js** | The only serious alternative. A node-editor framework with a dataflow engine. React support is a render *plugin*, not native; much smaller ecosystem; opinionated engine we would fight since our execution model is Alloy, not Rete. A full rewrite for no capability gain. |
| **LiteGraph.js** | Canvas-imperative, not React. Custom node bodies with real form controls are painful. |
| **JointJS / GoJS / yFiles** | Capable, but commercial for the parts we need, and none are React-native. |
| **Drawflow** | Substantially less capable; no typed connection validation. |
| **Cytoscape / Sigma / D3** | Graph *visualisation*, not node editing. Wrong category — no port model. |
| **tldraw / Excalidraw** | Whiteboard SDKs. Wrong category. |

Migration would also be strictly counter-productive: **the bug is in how we drive a controlled
graph library, and every alternative has the same controlled/uncontrolled split.** We would port
the anti-pattern to a smaller ecosystem and lose the four things React Flow already does right.

---

## Recommendation

Keep React Flow. Fix the integration, in this order:

1. **Adopt `applyNodeChanges`/`applyEdgeChanges` as the single change path.** Keep the store as the
   source of truth for *our* fields (component, props, label, position), but let React Flow's own
   reducer own *its* fields (`selected`, `measured`, `dragging`). This is one change that closes
   defects 1–4 together, rather than one field at a time.
2. **Give `rfNodes` stable object identity** — reuse the previous node object when nothing about it
   changed, so `checkEquality` hits and handle bounds stop being thrown away every render. This is
   also a real performance fix; today every node is fully re-adopted on every keystroke.
3. **Re-verify the drag layer afterwards.** It currently depends on the accidental re-measure. Once
   bounds are stable, `PipelineNode`'s manual `useUpdateNodeInternals` becomes the intended
   mechanism rather than a workaround — confirm it fires on port-layout change.
4. **Add the assertions that would have caught these.** All four failed silently; each needs a test
   that asserts the *visible consequence* (a node is deletable, the minimap has N nodes, two
   handles on one node have different coordinates). One such test now exists as a `fixme`.

Do 1 and 2 together — separately, either one regresses the other.

## What this changes about the ledger

`B-MINIMAP` should not be fixed on its own. It is a symptom of the same defect as the already-fixed
`selected` round-trip, and the review's item 3 and item 4. They are one work item, not four.
