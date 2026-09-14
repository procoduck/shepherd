import deepEqual from 'fast-deep-equal';
import { nanoid } from 'nanoid';
import { temporal } from 'zundo';
import { create } from 'zustand';
import { deleteAtPath, EXPR_KEY, setAtPath } from './bindings';
import { portsCompatible, validateGraph } from './l1';
import { portHandleId } from './schemaAdapter';
import { currentSchemaVersion } from './schemaVersion';
import type {
  ComponentDef,
  GraphDocument,
  GraphEdge,
  GraphNode,
  L1Diagnostic,
  SchemaPayload,
} from './types';
import { scalarConflicts } from './wireOrient';

/** Info about the in-flight connection drag (A3). Lives in the store — not in
 * PipelineNodeData / rfNodes — so starting/ending a drag doesn't force the
 * `rfNodes` useMemo (CanvasPane) to recompute and hand every node a new data
 * object; see `selectConnectionState` for how PipelineNode reads it back out
 * via a narrow, per-node selector instead. */
export interface ConnectingFrom {
  nodeId: string;
  handleId: string;
  handleType: 'source' | 'target';
  wireType: string | null;
}

/** A node's projection of `connectingFrom` onto its own ports (A2/A3). */
export interface ConnectionDragState {
  /** A connection drag is active and this node isn't the node the drag started from. */
  dragActive: boolean;
  /** This node has >=1 port compatible with the in-flight wire's type. */
  isValidTarget: boolean;
  /** dragActive && !isValidTarget — node has no compatible port, dim it. */
  isDimmed: boolean;
  /** Handle ids (per portHandleId) of this node's ports compatible with the in-flight wire. */
  validPortIds: string[];
}

// Reused whenever a node is unaffected by the current drag (or there is no
// drag at all), so two different drags that both leave a node with zero
// compatible ports resolve to this SAME object rather than two separately
// -allocated `{ validPortIds: [] }`s — the strongest form of "stable
// reference" a shallow/reference-equality check (or a `useMemo` depending on
// this value) can detect.
const IDLE_CONNECTION_STATE: ConnectionDragState = {
  dragActive: false,
  isValidTarget: false,
  isDimmed: false,
  validPortIds: [],
};
const DIMMED_CONNECTION_STATE: ConnectionDragState = {
  dragActive: true,
  isValidTarget: false,
  isDimmed: true,
  validPortIds: [],
};

/**
 * Pure, node-scoped projection of `connectingFrom` onto one node's schema def
 * (A3). PipelineNode subscribes to the raw `connectingFrom` value (stable
 * except at drag start/end) and wraps this in a `useMemo`, so the actual
 * per-node computation only reruns on those two transitions rather than on
 * every store tick. Exported as a standalone pure function so it's directly
 * unit-testable without rendering React (see store.test.ts).
 */
export function selectConnectionState(
  connectingFrom: ConnectingFrom | null,
  nodeId: string,
  def: ComponentDef | undefined,
): ConnectionDragState {
  if (!connectingFrom || connectingFrom.nodeId === nodeId || connectingFrom.wireType == null) {
    return IDLE_CONNECTION_STATE;
  }
  const wireType = connectingFrom.wireType;
  const ports = connectingFrom.handleType === 'source' ? (def?.inputs ?? []) : (def?.outputs ?? []);
  const validPortIds = ports
    .map((p, i) => ({ p, id: portHandleId(p, i) }))
    .filter(({ p }) =>
      connectingFrom.handleType === 'source'
        ? portsCompatible(wireType, p.type)
        : portsCompatible(p.type, wireType),
    )
    .map(({ id }) => id);
  if (validPortIds.length === 0) return DIMMED_CONNECTION_STATE;
  return { dragActive: true, isValidTarget: true, isDimmed: false, validPortIds };
}

/** Per-node health from the most recently viewed S3 sandbox run (design doc
 * §6.4 step 4 — "the single best debugging affordance in the whole
 * feature"). `health_state` mirrors Alloy's own component-health values
 * (healthy/unhealthy/unknown/exited) verbatim. */
export interface SimHealthEntry {
  health_state: string;
  message: string;
}

interface VisualStore {
  doc: GraphDocument;
  selected: string[];
  diagnostics: L1Diagnostic[];
  schema: SchemaPayload | null;
  allowExperimental: boolean;
  flowCheckActive: boolean;
  /** Set while a connection is being dragged from a handle; null otherwise. See ConnectingFrom. */
  connectingFrom: ConnectingFrom | null;
  /** Keyed by node id, from the sandbox run currently being viewed; null when
   * no run's results are on screen. Lives here — not on rfNodes/React Flow's
   * own fields — so it reaches PipelineNode through the same document-side
   * projection as `diagnostics` (see CanvasPane's "controlled-mode
   * contract"), rather than by mutating React Flow's node objects directly. */
  simHealthByNode: Record<string, SimHealthEntry> | null;
  /** Toolbar name field (B4) — used as the pipeline's name on save, seeded
   * from the loaded pipeline when editing an existing one. */
  pipelineName: string;
  /** Toolbar matcher chips (B4) — `key="value"` / `key=~"regex"` strings,
   * seeded from the loaded pipeline; required (non-empty) to save. */
  matchers: string[];

  setSchema: (s: SchemaPayload) => void;
  /** `refitView` asks the canvas to re-fit after placing, so a click-placed node is
   * always visible. Drag-drop placement passes nothing: the user chose that spot. */
  addNode: (
    component: string,
    position: { x: number; y: number },
    opts?: { refitView?: boolean },
  ) => void;
  /** Paste-specific variant: caller supplies the id and label. */
  addNodeWithId: (
    id: string,
    component: string,
    position: { x: number; y: number },
    label: string,
  ) => void;
  updateNode: (id: string, patch: Partial<GraphNode>) => void;
  /** Writes `{"$expr": expr}` at `path` inside node `id`'s props (W5-01) — the
   *  prop's own instance path, with a numeric segment per repeatable-block
   *  index (matches `L1DiagnosticEx.path`, e.g.
   *  `["endpoint", "0", "basic_auth", "password"]`). One undo step, like every
   *  other mutation here. See bindings.ts's module doc for why this writes
   *  into `props` rather than `doc.bindings[]`. */
  setBinding: (nodeId: string, path: string[], expr: string) => void;
  /** The inverse of `setBinding`: deletes the value at `path`, restoring the
   *  unset state a literal or a wire could then fill. */
  removeBinding: (nodeId: string, path: string[]) => void;
  removeNode: (id: string) => void;
  /** Adds a wire, or — when `to`'s port is `cardinality: scalar` (W5-03,
   *  design §3.2) — REPLACES whatever wire already lands there, as one undo
   *  step. `added` is false for a self-connection, an exact duplicate, or a
   *  cycle (the pre-existing checks); `replaced` lists the edge(s) removed to
   *  make room, `[]` otherwise — CanvasPane's onConnect uses it to show an
   *  Undo toast only when something was actually replaced. */
  addEdge: (
    from: { node: string; port: string },
    to: { node: string; port: string },
  ) => { added: boolean; replaced: GraphEdge[] };
  /** Swaps `edgeId`'s `order` with its previous/next sibling among the SAME
   *  (to.node, to.port)'s wires (W5-08's minimal fan-in reorder control) —
   *  one undo step. A no-op at either end of the list. */
  moveEdge: (edgeId: string, direction: 'up' | 'down') => void;
  /** Deletes every currently-selected node and edge (selected ids may name either)
   * plus every edge attached to a deleted node, as ONE atomic history entry — so a
   * single undo restores the whole selection, node(s), cascaded wires and all. */
  removeSelected: () => void;
  pasteNodesAndEdges: (nodes: GraphNode[], edges: GraphEdge[]) => void;
  /** Bumped whenever a whole document is swapped in, so the canvas can re-fit the
   * view. Without it a graph loaded after mount keeps the stored viewport and the
   * nodes render clipped under the toolbar until the fit control is pressed. */
  importSeq: number;
  /** Registered by the canvas: returns a free flow-position inside the CURRENTLY
   * VISIBLE viewport. Palette click-placement uses it because fixed grid
   * coordinates put nodes outside a narrow canvas, and an off-screen port cannot
   * be wired. Null before the canvas mounts (tests, graph-view). */
  getPlacement: ((index: number) => { x: number; y: number }) | null;
  setPlacementProvider: (fn: ((index: number) => { x: number; y: number }) | null) => void;
  importGraph: (doc: GraphDocument) => void;
  resetDoc: () => void;
  removeEdge: (id: string) => void;
  updateViewport: (vp: { x: number; y: number; zoom: number }) => void;
  /** Idempotent — no-op when ids array is deeply equal to current. */
  setSelected: (ids: string[]) => void;
  setLabel: (id: string, label: string) => void;
  setDisabled: (id: string, disabled: boolean) => void;
  toggleFlowCheck: () => void;
  /** Undo/redo, with diagnostics recomputed for the restored document.
   *
   * Always go through these rather than `temporal.getState().undo()` directly:
   * the temporal store partializes to `doc` alone, so a bare undo swaps the
   * graph while leaving `diagnostics` describing the graph the user just left. */
  undo: () => void;
  redo: () => void;
  setConnectingFrom: (cf: ConnectingFrom | null) => void;
  setPipelineName: (name: string) => void;
  /** No-op if `matcher` is already present. */
  addMatcher: (matcher: string) => void;
  removeMatcher: (index: number) => void;
  /** Seeds name+matchers together, e.g. after loading an existing pipeline. */
  setPipelineMeta: (name: string, matchers: string[]) => void;
  setSimHealthByNode: (health: Record<string, SimHealthEntry> | null) => void;
}

// A fresh document carries the SERVED schema's version, stamped by setSchema
// once the schema has loaded; before that it is empty — never a literal
// version, which would go stale at the next fleet bump and make every new
// pipeline read as one that needs upgrading (see schemaVersion.ts). The
// server treats an empty schema_version as "current" for the same reason.
function makeDefaultDoc(schemaVersion = ''): GraphDocument {
  return {
    kind: 'alloy-graph/v1',
    schema_version: schemaVersion,
    nodes: [],
    edges: [],
    bindings: [],
    viewport: { x: 0, y: 0, zoom: 1 },
    meta: { created_with: 'shepherd-vb/1.0' },
  };
}

/**
 * Exactly-one-selected-node projection (W5-10). Reference-stable across an
 * UNRELATED mutation: every store action that touches `doc.nodes` does so
 * with `.map((n) => (n.id === id ? { ...n, ...patch } : n))` (or an
 * equivalent), so every node OTHER than the one just changed keeps its exact
 * object reference — meaning this function returns the SAME object across
 * two calls that bracket an unrelated update, and zustand's default
 * `Object.is` output comparison (`useVisualStore(selectSelectedNode)`) skips
 * the re-render. `InspectorPanel` subscribes with it instead of the whole
 * `doc`, which changes reference on every single mutation.
 */
export function selectSelectedNode(
  s: Pick<VisualStore, 'selected' | 'doc'>,
): GraphNode | undefined {
  return s.selected.length === 1 ? s.doc.nodes.find((n) => n.id === s.selected[0]) : undefined;
}

function revalidate(
  state: Pick<VisualStore, 'doc' | 'schema' | 'allowExperimental'>,
): L1Diagnostic[] {
  return state.schema
    ? validateGraph(state.doc, state.schema, { allowExperimental: state.allowExperimental })
    : [];
}

export const useVisualStore = create<VisualStore>()(
  temporal(
    (set, get) => ({
      doc: makeDefaultDoc(),
      importSeq: 0,
      getPlacement: null,
      setPlacementProvider: (fn) => set({ getPlacement: fn }),
      selected: [],
      diagnostics: [],
      schema: null,
      allowExperimental: false,
      flowCheckActive: false,
      connectingFrom: null,
      pipelineName: '',
      matchers: [],
      simHealthByNode: null,

      setSchema: (schema) =>
        set((state) => {
          // Stamp a document whose version is still UNKNOWN (a fresh one,
          // never saved or imported) with the served version. A document
          // that carries any version — even an empty graph imported or
          // loaded from a save — keeps it, so the upgrade review can still
          // tell what it was authored against.
          const version = currentSchemaVersion(schema);
          const doc =
            version && state.doc.schema_version === ''
              ? { ...state.doc, schema_version: version }
              : state.doc;
          return { schema, doc, diagnostics: revalidate({ ...state, schema, doc }) };
        }),

      addNode: (component, position, opts) =>
        set((state) => {
          const node: GraphNode = {
            id: `n_${nanoid(8)}`,
            component,
            label: component.split('.').pop() ?? component,
            position,
            props: {},
            disabled: false,
            notes: '',
          };
          const doc = { ...state.doc, nodes: [...state.doc.nodes, node] };
          return {
            doc,
            // The canvas is often narrower than the placement grid, so a click-placed
            // node can land outside the viewport — and an off-screen port cannot be
            // wired. Re-fitting keeps every placed node reachable.
            importSeq: opts?.refitView ? state.importSeq + 1 : state.importSeq,
            diagnostics: revalidate({ ...state, doc }),
          };
        }),

      addNodeWithId: (id, component, position, label) =>
        set((state) => {
          const node: GraphNode = {
            id,
            component,
            label,
            position,
            props: {},
            disabled: false,
            notes: '',
          };
          const doc = { ...state.doc, nodes: [...state.doc.nodes, node] };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      updateNode: (id, patch) =>
        set((state) => {
          // Spreading an explicitly-undefined patch value (e.g. the
          // block_order InspectorPanel passes through for plain attributes)
          // would plant an own `key: undefined` on the node — which the
          // protobuf Struct conversion in the save path rejects with
          // "google.protobuf.Value must have a value". Absent means no change.
          const defined = Object.fromEntries(
            Object.entries(patch).filter(([, v]) => v !== undefined),
          );
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.map((n) => (n.id === id ? { ...n, ...defined } : n)),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      setBinding: (nodeId, path, expr) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.map((n) =>
              n.id === nodeId
                ? { ...n, props: setAtPath(n.props ?? {}, path, { [EXPR_KEY]: expr }) }
                : n,
            ),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      removeBinding: (nodeId, path) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.map((n) =>
              n.id === nodeId ? { ...n, props: deleteAtPath(n.props ?? {}, path) } : n,
            ),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      removeNode: (id) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.filter((n) => n.id !== id),
            edges: state.doc.edges.filter((e) => e.from.node !== id && e.to.node !== id),
          };
          return {
            doc,
            diagnostics: revalidate({ ...state, doc }),
            selected: state.selected.filter((x) => x !== id),
          };
        }),

      addEdge: (from, to) => {
        let result: { added: boolean; replaced: GraphEdge[] } = { added: false, replaced: [] };
        set((state) => {
          if (
            from.node === to.node ||
            state.doc.edges.some(
              (e) =>
                e.from.node === from.node &&
                e.from.port === from.port &&
                e.to.node === to.node &&
                e.to.port === to.port,
            )
          )
            return state;
          const adj = new Map(state.doc.nodes.map((n) => [n.id, [] as string[]]));
          for (const e of state.doc.edges) adj.get(e.from.node)?.push(e.to.node);
          adj.get(from.node)?.push(to.node);
          if (
            hasCycle(
              state.doc.nodes.map((n) => n.id),
              adj,
            )
          )
            return state;
          const replaced = scalarConflicts(state.schema, state.doc, { from, to });
          const replacedIds = new Set(replaced.map((e) => e.id));
          // W5-08: `order` is scoped per (to.node, to.port) — insertion order
          // among that port's OTHER wires, matching what renderTS.ts already
          // falls back to (its `seq` tiebreak) for a document saved before
          // this field was stamped.
          const siblingOrders = state.doc.edges
            .filter((e) => !replacedIds.has(e.id) && e.to.node === to.node && e.to.port === to.port)
            .map((e) => e.order ?? -1);
          const order = 1 + Math.max(-1, ...siblingOrders);
          const doc = {
            ...state.doc,
            edges: [
              ...state.doc.edges.filter((e) => !replacedIds.has(e.id)),
              { id: `e_${nanoid(8)}`, from, to, order },
            ],
          };
          result = { added: true, replaced };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        });
        return result;
      },

      moveEdge: (edgeId, direction) =>
        set((state) => {
          const edge = state.doc.edges.find((e) => e.id === edgeId);
          if (!edge) return state;
          const siblings = [...state.doc.edges]
            .filter((e) => e.to.node === edge.to.node && e.to.port === edge.to.port)
            .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
          const idx = siblings.findIndex((e) => e.id === edgeId);
          const swapWith = siblings[direction === 'up' ? idx - 1 : idx + 1];
          if (!swapWith) return state;
          const [orderA, orderB] = [edge.order ?? 0, swapWith.order ?? 0];
          const doc = {
            ...state.doc,
            edges: state.doc.edges.map((e) => {
              if (e.id === edge.id) return { ...e, order: orderB };
              if (e.id === swapWith.id) return { ...e, order: orderA };
              return e;
            }),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      pasteNodesAndEdges: (nodes, edges) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: [...state.doc.nodes, ...nodes],
            edges: [...state.doc.edges, ...edges],
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      importGraph: (doc) =>
        set((state) => ({
          doc,
          importSeq: state.importSeq + 1,
          diagnostics: revalidate({ ...state, doc }),
        })),

      resetDoc: () =>
        set({
          doc: makeDefaultDoc(currentSchemaVersion(get().schema) ?? ''),
          selected: [],
          diagnostics: [],
          pipelineName: '',
          matchers: [],
          simHealthByNode: null,
        }),

      removeEdge: (id) =>
        set((state) => {
          const doc = { ...state.doc, edges: state.doc.edges.filter((e) => e.id !== id) };
          return {
            doc,
            diagnostics: revalidate({ ...state, doc }),
            selected: state.selected.filter((x) => x !== id),
          };
        }),

      removeSelected: () =>
        set((state) => {
          if (state.selected.length === 0) return state;
          const selIds = new Set(state.selected);
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.filter((n) => !selIds.has(n.id)),
            // Cascade: an edge is removed either because it was itself selected,
            // or because one of its endpoint nodes was.
            edges: state.doc.edges.filter(
              (e) => !selIds.has(e.id) && !selIds.has(e.from.node) && !selIds.has(e.to.node),
            ),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }), selected: [] };
        }),

      updateViewport: (viewport) => set((state) => ({ doc: { ...state.doc, viewport } })),

      // Idempotent: skip the store update when the selection hasn't changed.
      setSelected: (ids) =>
        set((state) => (deepEqual(state.selected, ids) ? state : { selected: ids })),

      setLabel: (id, label) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.map((n) => (n.id === id ? { ...n, label } : n)),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      toggleFlowCheck: () => set((state) => ({ flowCheckActive: !state.flowCheckActive })),

      // The 13 mutations above each end with `diagnostics: revalidate(...)`,
      // which is what keeps the Problems drawer honest. Undo and redo bypass
      // every one of them — zundo restores `doc` straight into the store — so
      // without this the drawer keeps showing the problems of the graph the
      // user just undid: delete a wired node (2 problems appear), undo it, and
      // the graph is whole again while the drawer still says 2. The toolbar,
      // which re-renders server-side, said Valid at the same time.
      undo: () => {
        useVisualStore.temporal.getState().undo();
        set((state) => ({ diagnostics: revalidate(state) }));
      },
      redo: () => {
        useVisualStore.temporal.getState().redo();
        set((state) => ({ diagnostics: revalidate(state) }));
      },

      // Not part of `doc` — mirrors updateViewport's pattern of a `set` that the
      // temporal `equality` fn (below, compares only doc/nodes/edges/bindings)
      // treats as unchanged, so drag start/end never pushes undo history.
      setConnectingFrom: (connectingFrom) => set({ connectingFrom }),

      setDisabled: (id, disabled) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.map((n) => (n.id === id ? { ...n, disabled } : n)),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      setPipelineName: (pipelineName) => set({ pipelineName }),

      addMatcher: (matcher) =>
        set((state) =>
          state.matchers.includes(matcher) ? state : { matchers: [...state.matchers, matcher] },
        ),

      removeMatcher: (index) =>
        set((state) => ({ matchers: state.matchers.filter((_, i) => i !== index) })),

      setPipelineMeta: (pipelineName, matchers) => set({ pipelineName, matchers }),

      setSimHealthByNode: (simHealthByNode) => set({ simHealthByNode }),
    }),
    {
      partialize: (state) => ({ doc: state.doc }),
      limit: 100,
      equality: (a, b) => {
        const da = (a as { doc?: GraphDocument }).doc;
        const db = (b as { doc?: GraphDocument }).doc;
        if (!da || !db) return deepEqual(a, b);
        return deepEqual(
          { nodes: da.nodes, edges: da.edges, bindings: da.bindings },
          { nodes: db.nodes, edges: db.edges, bindings: db.bindings },
        );
      },
    },
  ),
);

function hasCycle(nodeIds: string[], adj: Map<string, string[]>): boolean {
  const color = new Map(nodeIds.map((id) => [id, 0]));
  function dfs(u: string): boolean {
    color.set(u, 1);
    for (const v of adj.get(u) ?? []) {
      if (color.get(v) === 1) return true;
      if (color.get(v) === 0 && dfs(v)) return true;
    }
    color.set(u, 2);
    return false;
  }
  return nodeIds.some((id) => color.get(id) === 0 && dfs(id));
}
