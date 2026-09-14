import {
  applyEdgeChanges,
  applyNodeChanges,
  Background,
  type Connection,
  Controls,
  type Edge,
  type EdgeChange,
  type FinalConnectionState,
  getViewportForBounds,
  MiniMap,
  type NodeChange,
  type NodeTypes,
  type OnSelectionChangeParams,
  ReactFlow,
  type ReactFlowState,
  useReactFlow,
  useStore,
  useStoreApi,
  type XYPosition,
} from '@xyflow/react';
import {
  type CSSProperties,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import '@xyflow/react/dist/base.css';
import deepEqual from 'fast-deep-equal';
import { nanoid } from 'nanoid';
import { toast } from 'sonner';
import {
  type EdgeInputs,
  type NodeInputs,
  type PipelineFlowNode,
  reconcileEdges,
  reconcileNodes,
} from '../reconcile';
import { getThemedWireColor, portHandleId } from '../schemaAdapter';
import { type ConnectingFrom, useVisualStore } from '../store';
import type { GraphEdge, GraphNode, L1Diagnostic } from '../types';
import { orientConnection } from '../wireOrient';
import { PipelineNode, useTheme } from './PipelineNode';

const nodeTypes: NodeTypes = { pipeline: PipelineNode as NodeTypes[string] };

// Clipboard data is scoped to this canvas instance, preventing cross-pipeline pastes.
type Clipboard = { nodes: GraphNode[]; edges: GraphEdge[] };

// Placement grid, in flow units. PipelineNode's box is `w-60` (240px) wide; its
// height varies with port count, so the row pitch allows for a tall one. These
// are pitches, not sizes — the slack between them is the visible gap.
const PLACE_COL_W = 300;
const PLACE_ROW_H = 150;

// FitOnFirstNodes fits the graph once per loaded document. It re-fits when
// importSeq changes, because a pipeline's graph arrives asynchronously after mount:
// without that the stored viewport wins and the nodes render clipped under the
// toolbar until the user presses the fit control. (A click-placed node bumps
// importSeq too — `refitView` in store.ts — so it lands inside the viewport.)
//
// `maxZoom: 1` (here and on `<ReactFlow>`'s own `fitViewOptions` below) is
// task item 7's fix for the OTHER half of what the review measured: fitting
// around a single small node with no cap snaps to `maxZoom={2}` (the
// canvas's own zoom ceiling), so the very next node placed at a spacing sized
// for 1:1 viewing (Palette's stagger, also fixed in this task) still looks
// overlapped on screen at 2x. Capping the zoom this fit can reach removes
// that amplification; recovering visibility of everything placed after is
// what the canvas's own "fit view" control (`.react-flow__controls-fitview`,
// part of the `<Controls>` below) is for — one click, not the three the
// review had to resort to with the old, uncapped zoom.
//
// The fit is synchronous and instant, from a layout effect, in the very
// commit in which React Flow has measured every node the document holds. An
// earlier version deferred it (a requestAnimationFrame, then React Flow's
// `fitView()` — which is itself queued behind another render and frame) and
// animated it over 200ms. That left a window of several frames in which a
// freshly placed node was already visible, its ports already draggable, but
// the viewport had not yet started moving under it: a wire drag begun right
// after placing a node had its target handle pan away from the pointer
// mid-drag, the drop landed on the pane, and no edge was made. React 18
// mostly won that race by luck of effect timing; React 19 loses it every
// time. Fitting in the same commit that first paints the node closes the
// window — nothing can see, or drag to, a measured node at the old viewport.
function FitOnFirstNodes() {
  const store = useStoreApi();
  const { getNodesBounds, setViewport } = useReactFlow();
  const docNodes = useVisualStore((s) => s.doc.nodes);
  const importSeq = useVisualStore((s) => s.importSeq);
  // Readiness is asked of React Flow's node lookup for the DOCUMENT's nodes,
  // not of `useNodesInitialized`: right after an import or a placement that
  // hook still answers for the nodes React Flow held before the change, and
  // a fit taken on that answer frames the previous document.
  const measured = useStore(
    useCallback(
      (s: ReactFlowState) => {
        if (docNodes.length === 0 || s.width === 0 || s.height === 0) return false;
        for (const n of docNodes) {
          const internal = s.nodeLookup.get(n.id);
          if (
            !internal?.measured.width ||
            !internal.measured.height ||
            !internal.internals.handleBounds
          ) {
            return false;
          }
        }
        return true;
      },
      [docNodes],
    ),
  );
  const fittedForRef = useRef<number | null>(null);

  useLayoutEffect(() => {
    if (docNodes.length === 0) {
      fittedForRef.current = null;
      return;
    }
    if (!measured || fittedForRef.current === importSeq) return;
    fittedForRef.current = importSeq;
    const { width, height, minZoom } = store.getState();
    // Same framing as `fitViewOptions` on <ReactFlow> below. A viewport set
    // without a duration is applied synchronously; the promise only reports.
    void setViewport(
      getViewportForBounds(
        getNodesBounds(docNodes.map((n) => n.id)),
        width,
        height,
        minZoom,
        1,
        0.15,
      ),
    );
  }, [measured, docNodes, importSeq, store, getNodesBounds, setViewport]);

  return null;
}

function FlowApiBridge({
  screenToFlowRef,
}: {
  screenToFlowRef: React.MutableRefObject<((position: XYPosition) => XYPosition) | null>;
}) {
  const { screenToFlowPosition } = useReactFlow();
  screenToFlowRef.current = screenToFlowPosition;

  // Publish a placement provider so click-placed nodes land inside the visible
  // canvas. The canvas is frequently narrower than the old fixed grid (measured
  // at 424px with both side panels open), which put the second node off-screen —
  // and a port that is off-screen cannot be dragged to, so no wire could be made.
  //
  // Laid out on a grid sized by the real node box, in FLOW space. The previous
  // version cascaded each node 48px down-right of the last, which overlaps a
  // 240px-wide node by about 80%: a selected node is elevated to z-index 1000
  // and then covers its neighbour, so the neighbour cannot be clicked at all.
  // Screen-space offsets were also unstable — they are read through the live
  // viewport, so a fit landing between two placements moved where the next node
  // went.
  //
  // The grid is anchored ONCE, at the visible area as of the first placement
  // this provider serves; every later cell is laid out from that anchor.
  // Re-reading the visible area on every click looked more adaptive but was
  // not: each placement also re-fits the view (`refitView` in store.ts →
  // FitOnFirstNodes), so a grid re-anchored to the post-fit view walked the
  // next node diagonally away from the last — and whether a click saw the
  // pre- or post-fit view depended on whether that fit had landed yet, so
  // the same three clicks produced different layouts, one of them with the
  // third node dropped on top of the first. Keeping the whole set in view is
  // the refit's job, not the grid's.
  const setPlacementProvider = useVisualStore((s) => s.setPlacementProvider);
  useEffect(() => {
    let anchor: { topLeft: XYPosition; columns: number; rows: number } | null = null;
    setPlacementProvider((index: number) => {
      if (anchor === null) {
        const pane = document.querySelector('.react-flow__pane');
        const r = pane?.getBoundingClientRect();
        if (!r || r.width === 0) return { x: 80 + index * PLACE_COL_W, y: 80 };
        const inset = 32;
        // Both corners through the same transform, so the available area is in
        // flow units and the grid stays correct at any zoom.
        const topLeft = screenToFlowPosition({ x: r.x + inset, y: r.y + inset });
        const bottomRight = screenToFlowPosition({
          x: r.x + r.width - inset,
          y: r.y + r.height - inset,
        });
        anchor = {
          topLeft,
          columns: Math.max(1, Math.floor((bottomRight.x - topLeft.x) / PLACE_COL_W)),
          rows: Math.max(1, Math.floor((bottomRight.y - topLeft.y) / PLACE_ROW_H)),
        };
      }
      const { topLeft, columns, rows } = anchor;
      // Wrap within the anchored area rather than marching off the bottom;
      // past capacity, nudge each cycle so nodes stay distinguishable instead
      // of landing exactly on top of an earlier one.
      const cell = index % (columns * rows);
      const cycle = Math.floor(index / (columns * rows));
      return {
        x: Math.round(topLeft.x + (cell % columns) * PLACE_COL_W + cycle * 24),
        y: Math.round(topLeft.y + Math.floor(cell / columns) * PLACE_ROW_H + cycle * 24),
      };
    });
    return () => setPlacementProvider(null);
  }, [screenToFlowPosition, setPlacementProvider]);

  return null;
}

// getSourceReachableEdges, reconcileNodes and reconcileEdges (with their
// NodeInputs/EdgeInputs/EMPTY_DIAGNOSTICS support types and the controlled-mode
// contract they implement) live in ../reconcile.ts (W5-06) — imported above.

export function CanvasPane() {
  const doc = useVisualStore((s) => s.doc);
  const schema = useVisualStore((s) => s.schema);
  const theme = useTheme();
  const selected = useVisualStore((s) => s.selected);
  const diagnostics = useVisualStore((s) => s.diagnostics);
  const simHealthByNode = useVisualStore((s) => s.simHealthByNode);
  const flowCheckActive = useVisualStore((s) => s.flowCheckActive);
  const addEdge = useVisualStore((s) => s.addEdge);
  const removeEdge = useVisualStore((s) => s.removeEdge);
  const removeNode = useVisualStore((s) => s.removeNode);
  const removeSelected = useVisualStore((s) => s.removeSelected);
  const setSelected = useVisualStore((s) => s.setSelected);
  const updateViewport = useVisualStore((s) => s.updateViewport);
  const addNode = useVisualStore((s) => s.addNode);
  const updateNode = useVisualStore((s) => s.updateNode);
  const pasteNodesAndEdges = useVisualStore((s) => s.pasteNodesAndEdges);
  // A3: connectingFrom lives in the store (not local state feeding node data) so
  // starting/ending a drag doesn't force `rfNodes` below to recompute — see
  // PipelineNode's narrow `selectConnectionState` selector for how nodes read
  // it back out without all re-rendering together.
  const connectingFrom = useVisualStore((s) => s.connectingFrom);
  const setConnectingFrom = useVisualStore((s) => s.setConnectingFrom);
  const clipboardRef = useRef<Clipboard | null>(null);
  const pasteOffsetRef = useRef(0);
  const screenToFlowRef = useRef<((position: XYPosition) => XYPosition) | null>(null);

  // Stable refs so stable callbacks can read current values without deps.
  const docRef = useRef(doc);
  docRef.current = doc;
  const schemaRef = useRef(schema);
  schemaRef.current = schema;

  // Ref to the canvas wrapper — used to focus it on mount so keyboard shortcuts work.
  const canvasWrapperRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    canvasWrapperRef.current?.focus({ preventScroll: true });
  }, []);

  // Track the last selection array we committed to the store.
  // Used as the gate so onSelectionChange is idempotent even when
  // the store's setSelected already is — belt-and-suspenders.
  const lastSelRef = useRef<string[]>([]);
  const syncSetSelected = useCallback(
    (ids: string[]) => {
      lastSelRef.current = ids;
      setSelected(ids);
    },
    [setSelected],
  );

  // --- View projection (see "The controlled-mode contract" above) ---
  // Only the per-node subset of diagnostics reaches a node, so a diagnostic on
  // one node doesn't invalidate the rest. Memoised on `diagnostics` so the
  // per-node arrays are reference-stable between validation runs.
  const diagnosticsByNode = useMemo(() => {
    const byNode = new Map<string, L1Diagnostic[]>();
    for (const d of diagnostics) {
      if (d.node_id) {
        const arr = byNode.get(d.node_id);
        if (arr) arr.push(d);
        else byNode.set(d.node_id, [d]);
      }
    }
    return byNode;
  }, [diagnostics]);

  const selectedIds = useMemo(() => new Set(selected), [selected]);

  // Derivation inputs from the last reconcile, so the next one can tell an
  // unchanged node from one that needs rebuilding. Refs, not state: they are
  // bookkeeping for the projection and must never trigger a render themselves.
  const nodeInputsRef = useRef(new Map<string, NodeInputs>());
  const edgeInputsRef = useRef(new Map<string, EdgeInputs>());

  const [rfNodes, setRfNodes] = useState<PipelineFlowNode[]>(() =>
    reconcileNodes(
      [],
      doc.nodes,
      schema,
      diagnosticsByNode,
      selectedIds,
      nodeInputsRef.current,
      simHealthByNode,
    ),
  );
  const [rfEdges, setRfEdges] = useState<Edge[]>(() =>
    reconcileEdges(
      [],
      doc.edges,
      doc.nodes,
      schema,
      flowCheckActive,
      selectedIds,
      edgeInputsRef.current,
      theme,
    ),
  );

  // Document -> view. React Flow's own fields are carried across by the
  // reconciler, so this never clobbers a measurement or an in-flight drag.
  useEffect(() => {
    setRfNodes((cur) =>
      reconcileNodes(
        cur,
        doc.nodes,
        schema,
        diagnosticsByNode,
        selectedIds,
        nodeInputsRef.current,
        simHealthByNode,
      ),
    );
  }, [doc.nodes, schema, diagnosticsByNode, selectedIds, simHealthByNode]);

  useEffect(() => {
    setRfEdges((cur) =>
      reconcileEdges(
        cur,
        doc.edges,
        doc.nodes,
        schema,
        flowCheckActive,
        selectedIds,
        edgeInputsRef.current,
        theme,
      ),
    );
  }, [doc.edges, doc.nodes, schema, flowCheckActive, selectedIds, theme]);

  // A2: the in-flight connection line takes the source port's wire color
  // instead of React Flow's default gray bezier.
  const connectionLineStyle = useMemo<CSSProperties>(
    () => ({
      strokeWidth: 2,
      stroke: connectingFrom?.wireType
        ? getThemedWireColor(schema, connectingFrom.wireType, theme)
        : undefined,
    }),
    [connectingFrom, schema, theme],
  );

  // --- Controlled mode: React Flow's change stream ---
  //
  // applyNodeChanges is the ONLY writer of React Flow's own fields. It handles
  // all six change types it can emit — `select`, `dimensions` (-> `measured`),
  // `position` (-> `dragging`), `remove`, `add`, `replace` — and it preserves
  // object identity for every node a change did not touch. Hand-rolling this
  // (the old code covered `position` and `remove` and dropped the other four)
  // is what left selection permanently empty and `measured` never set.
  //
  // After applying, we mirror the subset that is DOCUMENT state back into the
  // store. Anything not mirrored here is view state and stays view state.
  const onNodesChange = useCallback(
    (changes: NodeChange<PipelineFlowNode>[]) => {
      setRfNodes((cur) => applyNodeChanges<PipelineFlowNode>(changes, cur));
      for (const c of changes) {
        // Position is committed once per gesture, at drag end. React Flow emits
        // a change per pointer move with `dragging: true` and a final one with
        // `dragging: false`; writing every one of them put hundreds of entries
        // in the undo history for a single drag, and churned the document — and
        // hence this projection — on every frame of it.
        if (c.type === 'position' && c.position && c.dragging !== true) {
          updateNode(c.id, { position: c.position });
        }
        if (c.type === 'remove') {
          removeNode(c.id);
        }
        // 'select' is mirrored to the store by onSelectionChange; 'dimensions'
        // is React Flow's alone and deliberately does not reach the document.
      }
    },
    [updateNode, removeNode],
  );

  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      setRfEdges((cur) => applyEdgeChanges<Edge>(changes, cur));
      for (const c of changes) {
        if (c.type === 'remove') {
          removeEdge(c.id);
        }
      }
    },
    [removeEdge],
  );

  // --- Single selection handler (gated, no onNodeClick dual-write) ---
  // Selection is one flat id list spanning both nodes and edges — RF reports
  // them separately, we merge them so Delete/Backspace (removeSelected) and
  // multi-select (drag-box, shift-click) work identically for either kind.
  const onSelectionChange = useCallback(
    ({ nodes: ns, edges: es }: OnSelectionChangeParams) => {
      const next = [...ns.map((n) => n.id), ...es.map((e) => e.id)];
      if (deepEqual(lastSelRef.current, next)) return;
      lastSelRef.current = next;
      syncSetSelected(next);
    },
    [syncSetSelected],
  );

  // --- Type-check wires ---
  // D1: a connection is valid exactly when wireOrient can find a role-valid
  // produces/accepts orientation for it — see wireOrient.ts for why RF's own
  // fixed source/target handle typing doesn't already guarantee that.
  const isValidConnection = useCallback(
    (c: Connection | Edge) => {
      if (!c.source || !c.target) return false;
      return (
        orientConnection(schemaRef.current, docRef.current, {
          source: c.source,
          sourceHandle: c.sourceHandle ?? '',
          target: c.target,
          targetHandle: c.targetHandle ?? '',
        }) !== null
      );
    },
    [], // reads only stable refs
  );

  // --- Direct click-to-select fallback ---
  // React Flow's onSelectionChange is reliable for multi-select via drag-box and
  // shift-click, but Playwright `click({ force: true })` on a node's inner div
  // doesn't always trigger RF's internal selection handler. We add a click handler
  // on the canvas wrapper that walks up to the nearest .react-flow__node or
  // .react-flow__edge and syncs selection directly.
  const onCanvasClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      // Skip double-clicks — let PipelineNode's onDoubleClick handle inline editing.
      if (e.detail >= 2) return;
      // A modified click (shift for multi-select, meta/ctrl for RF's native
      // additive select) is owned entirely by React Flow's own handling — this
      // fallback only replaces the plain-click case below it, so it must not
      // collapse a just-computed multi-selection back down to one id.
      if (e.shiftKey || e.metaKey || e.ctrlKey) return;

      const target = e.target as HTMLElement;
      const edgeWrapper = target.closest<HTMLElement>('.react-flow__edge');
      if (edgeWrapper) {
        const edgeId = edgeWrapper.dataset.id;
        if (!edgeId) return;
        const next = [edgeId];
        if (deepEqual(lastSelRef.current, next)) return;
        lastSelRef.current = next;
        syncSetSelected(next);
        canvasWrapperRef.current?.focus({ preventScroll: true });
        return;
      }

      const nodeWrapper = target.closest<HTMLElement>('.react-flow__node');
      if (!nodeWrapper) {
        // Clicked the canvas background — deselect
        if (lastSelRef.current.length > 0) {
          lastSelRef.current = [];
          syncSetSelected([]);
        }
        return;
      }
      // Find our node id from the inner data-node-id attribute
      const inner = nodeWrapper.querySelector<HTMLElement>('[data-node-id]');
      const nodeId = inner?.dataset.nodeId ?? nodeWrapper.dataset.id;
      if (!nodeId) return;
      const next = [nodeId];
      if (deepEqual(lastSelRef.current, next)) return;
      lastSelRef.current = next;
      syncSetSelected(next);
      // Re-focus the canvas so subsequent keyboard shortcuts (copy/paste/undo) work.
      canvasWrapperRef.current?.focus({ preventScroll: true });
    },
    [syncSetSelected],
  );
  // B1 (docs/reviews, D1): the stored edge is oriented by wireOrient
  // (produces->accepts), not by RF's source/target verbatim — see
  // wireOrient.ts. Every downstream consumer (store, validator, renderer) can
  // then assume `from`=produces/`to`=accepts always.
  const onConnect = useCallback(
    (c: Connection) => {
      if (!c.source || !c.target) return;
      const d = docRef.current;
      const oriented = orientConnection(schemaRef.current, d, {
        source: c.source,
        sourceHandle: c.sourceHandle ?? '',
        target: c.target,
        targetHandle: c.targetHandle ?? '',
      });
      if (!oriented) {
        // isValidConnection already screens this out before RF fires onConnect;
        // this is defense in depth, not a reachable path in normal use.
        toast.error('These ports cannot be connected — incompatible types or roles');
        return;
      }
      const { from, to } = oriented;

      const adj = new Map(d.nodes.map((n) => [n.id, [] as string[]]));
      for (const e of d.edges) adj.get(e.from.node)?.push(e.to.node);
      adj.get(from.node)?.push(to.node);
      const seen = new Set<string>();
      const reachesSource = (u: string): boolean => {
        if (u === from.node) return true;
        if (seen.has(u)) return false;
        seen.add(u);
        return (adj.get(u) ?? []).some(reachesSource);
      };
      if (reachesSource(to.node)) {
        toast.error('Alloy graphs are acyclic — this connection would create a cycle');
        return;
      }
      // W5-03: a `cardinality: scalar` port accepts one wire, so addEdge just
      // replaced whatever was already there instead of fanning in — as ONE
      // undo step (the add and the removal happened in the same store `set`
      // call), so a single Undo restores the wire this replaced.
      const { replaced } = addEdge(from, to);
      if (replaced.length > 0) {
        toast('Replaced the existing wire on this port', {
          action: { label: 'Undo', onClick: () => useVisualStore.getState().undo() },
        });
      }
    },
    [addEdge],
  );

  const onConnectStart = useCallback(
    (
      _event: unknown,
      params: {
        nodeId: string | null;
        handleId: string | null;
        handleType: 'source' | 'target' | null;
      },
    ) => {
      if (!params.nodeId || !params.handleId || !params.handleType) return;
      const sc = schemaRef.current;
      const d = docRef.current;
      const node = d.nodes.find((n) => n.id === params.nodeId);
      const def = node && sc?.components[node.component];
      let wireType: string | null = null;
      if (params.handleType === 'source') {
        wireType =
          def?.outputs.find((p, i) => portHandleId(p, i) === params.handleId)?.type ?? null;
      } else {
        wireType = def?.inputs.find((p, i) => portHandleId(p, i) === params.handleId)?.type ?? null;
      }
      setConnectingFrom({
        nodeId: params.nodeId,
        handleId: params.handleId,
        handleType: params.handleType,
        wireType,
      } satisfies ConnectingFrom);
    },
    [setConnectingFrom],
  );

  // A dropped connection that resolved onto an incompatible or
  // self-referencing handle previously ended silently — no edge, no toast, no
  // explanation (task item 6). `isValid === false` means the drop landed on a
  // handle React Flow's isValidConnection rejected; `isValid === null` means
  // it did not land on a handle at all (a plain cancelled drag), which is not
  // a rejection and must stay silent.
  const onConnectEnd = useCallback(
    (_event: MouseEvent | TouchEvent, state: FinalConnectionState) => {
      setConnectingFrom(null);
      if (state.isValid === false) {
        toast.error('That connection is not allowed — incompatible ports or a self-connection');
      }
    },
    [setConnectingFrom],
  );

  // --- Drag-drop from palette ---
  const onDrop = useCallback(
    (e: React.DragEvent<HTMLDivElement>) => {
      e.preventDefault();
      const name = e.dataTransfer.getData('application/vb-component');
      if (!name) return;
      const position = screenToFlowRef.current?.({ x: e.clientX, y: e.clientY });
      if (position) addNode(name, position);
    },
    [addNode],
  );

  // --- Keyboard shortcuts (delete/copy/paste/undo/redo/select-all) ---
  const onKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLDivElement>) => {
      // The inline node-label rename input (PipelineNode) lives inside this
      // canvas subtree, so its keydowns bubble here too — Backspace erasing a
      // character while renaming must not also delete the selected node.
      const targetEl = e.target as HTMLElement;
      if (
        targetEl.tagName === 'INPUT' ||
        targetEl.tagName === 'TEXTAREA' ||
        targetEl.isContentEditable
      ) {
        return;
      }

      if (e.key === 'Backspace' || e.key === 'Delete') {
        e.preventDefault();
        removeSelected();
        return;
      }

      const isMeta = e.metaKey || e.ctrlKey;
      if (!isMeta) return;
      const key = e.key.toLowerCase();

      if (key === 'c') {
        e.preventDefault();
        const d = docRef.current;
        const selIds = new Set(lastSelRef.current);
        if (selIds.size === 0) return;
        const nodes = d.nodes.filter((n) => selIds.has(n.id));
        clipboardRef.current = {
          nodes,
          edges: d.edges.filter((edge) => selIds.has(edge.from.node) && selIds.has(edge.to.node)),
        };
        return;
      }

      if (key === 'v' && clipboardRef.current) {
        e.preventDefault();
        pasteOffsetRef.current += 24;
        const off = pasteOffsetRef.current;
        const idMap = new Map<string, string>();
        for (const n of clipboardRef.current.nodes) idMap.set(n.id, `n_${nanoid(8)}`);
        const pastedNodes = clipboardRef.current.nodes.map((n) => ({
          ...n,
          id: idMap.get(n.id)!,
          label: `${n.label}_copy`,
          position: { x: n.position.x + off, y: n.position.y + off },
        }));
        const pastedEdges = clipboardRef.current.edges
          .map((edge) => {
            const fromId = idMap.get(edge.from.node);
            const toId = idMap.get(edge.to.node);
            return fromId && toId
              ? {
                  ...edge,
                  id: `e_${nanoid(8)}`,
                  from: { ...edge.from, node: fromId },
                  to: { ...edge.to, node: toId },
                }
              : null;
          })
          .filter((e): e is GraphEdge => e !== null);
        pasteNodesAndEdges(pastedNodes, pastedEdges);
        syncSetSelected(Array.from(idMap.values()));
        return;
      }

      if (key === 'z') {
        e.preventDefault();
        if (e.shiftKey) {
          useVisualStore.getState().redo();
        } else {
          useVisualStore.getState().undo();
        }
        return;
      }

      if (key === 'a') {
        e.preventDefault();
        syncSetSelected(docRef.current.nodes.map((n) => n.id));
      }
    },
    [pasteNodesAndEdges, syncSetSelected, removeSelected],
  );

  return (
    <div
      // overflow-hidden: React Flow's own base.css never clips `.react-flow`
      // (only its edge/handle layers get `overflow: visible`), so without
      // this a node dragged to a negative flow-x renders outside this pane's
      // box and bleeds into the Palette/app sidebar to its left — task item
      // 7 measured such a node unclickable there, with drags aimed at it
      // instead landing on whatever palette item painted on top.
      className='flex-1 relative outline-none overflow-hidden'
      data-testid='canvas'
      ref={canvasWrapperRef}
      tabIndex={0}
      onKeyDown={onKeyDown}
      onDragOver={(e) => e.preventDefault()}
      onDrop={onDrop}
      onClick={onCanvasClick}
    >
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        nodeTypes={nodeTypes}
        defaultViewport={doc.viewport}
        fitView
        // task item 7: cap how far in either fit (this declarative one at
        // mount, or FitOnFirstNodes' imperative one on import) is allowed to
        // zoom — see FitOnFirstNodes' comment above for why.
        fitViewOptions={{ padding: 0.15, maxZoom: 1 }}
        isValidConnection={isValidConnection}
        onConnect={onConnect}
        onConnectStart={onConnectStart}
        onConnectEnd={onConnectEnd}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onSelectionChange={onSelectionChange}
        onMoveEnd={(_, vp) => updateViewport(vp)}
        // A2: snap the wire to a compatible handle within 30px, not just on exact hover.
        connectionRadius={30}
        connectionLineStyle={connectionLineStyle}
        snapToGrid
        snapGrid={[8, 8]}
        minZoom={0.25}
        maxZoom={2}
        // We delete via our own onKeyDown (removeSelected) so a node's cascaded
        // edges are removed in the same atomic, undoable step — RF's own
        // deleteKeyCode path would fire onNodesChange/onEdgesChange as two
        // separate history entries. Selection stays native (see `selected`
        // round-tripped onto rfNodes/rfEdges above); only the delete key is opted out.
        deleteKeyCode={null}
        // Shift drives both box-select-by-drag (RF's default) and click-to-add,
        // so the same modifier does both halves of "multi-select via drag-box
        // and shift-click".
        multiSelectionKeyCode='Shift'
      >
        <FlowApiBridge screenToFlowRef={screenToFlowRef} />
        <FitOnFirstNodes />
        <Background />
        {/* top-left: the minimap owns bottom-left, and stacking both hid the zoom buttons. */}
        <Controls
          position='top-left'
          className='[&>button]:bg-card [&>button]:border-border [&>button]:fill-zinc-300 [&>button:hover]:bg-accent/20'
        />
        {/* bottom-left avoids overlap with default node placement area (center/right) */}
        {/* React Flow's minimap defaults to a light palette; pin it to the
            current token layer explicitly (F2, 2026-09-14 walkthrough fixes:
            these were dark-only literals, unreadable once light mode
            existed) — bgColor/nodeColor/nodeStrokeColor mirror --color-panel/
            --color-border-strong/--color-accent for each theme. */}
        <MiniMap
          position='bottom-left'
          pannable
          zoomable
          bgColor={theme === 'light' ? '#f4f4f5' : '#0e0e11'}
          maskColor='rgba(9,9,11,0.75)'
          nodeColor={theme === 'light' ? '#d4d4d8' : '#3f3f46'}
          nodeStrokeColor={theme === 'light' ? '#4f46e5' : '#6366f1'}
          className='!border !border-border !rounded-md'
        />
      </ReactFlow>
    </div>
  );
}
