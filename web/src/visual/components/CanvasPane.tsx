import {
  applyEdgeChanges,
  applyNodeChanges,
  Background,
  type Connection,
  Controls,
  type Edge,
  type EdgeChange,
  type FinalConnectionState,
  MiniMap,
  type Node,
  type NodeChange,
  type NodeTypes,
  type OnSelectionChangeParams,
  ReactFlow,
  useNodesInitialized,
  useReactFlow,
  type XYPosition,
} from '@xyflow/react';
import { type CSSProperties, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import '@xyflow/react/dist/base.css';
import deepEqual from 'fast-deep-equal';
import { nanoid } from 'nanoid';
import { toast } from 'sonner';
import { resolvePorts } from '../l1';
import { getWireColor, portHandleId } from '../schemaAdapter';
import { type ConnectingFrom, useVisualStore } from '../store';
import type { ComponentDef, GraphEdge, GraphNode, L1Diagnostic, SchemaPayload } from '../types';
import { orientConnection, rfEndpointsForEdge } from '../wireOrient';
import type { PipelineNodeData } from './PipelineNode';
import { PipelineNode } from './PipelineNode';

const nodeTypes: NodeTypes = { pipeline: PipelineNode as NodeTypes[string] };

/** The concrete node type this canvas renders — pins React Flow's generics so
 * applyNodeChanges returns our node shape rather than the bare NodeBase. */
type PipelineFlowNode = Node<PipelineNodeData, 'pipeline'>;

// Clipboard data is scoped to this canvas instance, preventing cross-pipeline pastes.
type Clipboard = { nodes: GraphNode[]; edges: GraphEdge[] };

// FitOnFirstNodes fits the graph once per loaded document. It re-fits when
// importSeq changes, because a pipeline's graph arrives asynchronously after mount:
// without that the stored viewport wins and the nodes render clipped under the
// toolbar until the user presses the fit control.
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
function FitOnFirstNodes() {
  const { fitView } = useReactFlow();
  const initialized = useNodesInitialized();
  const nodeCount = useVisualStore((s) => s.doc.nodes.length);
  const importSeq = useVisualStore((s) => s.importSeq);
  const fittedForRef = useRef<number | null>(null);

  useEffect(() => {
    if (nodeCount === 0) {
      fittedForRef.current = null;
      return;
    }
    if (initialized && fittedForRef.current !== importSeq) {
      // Defer a frame: on a freshly imported document React Flow reports the nodes
      // as initialized before their measured sizes land, and fitting against
      // unmeasured nodes leaves the stored viewport in place.
      const seq = importSeq;
      const raf = requestAnimationFrame(() => {
        fitView({ padding: 0.15, duration: 200, maxZoom: 1 });
        // Marked done only once the fit has actually run. Marking it before the
        // frame meant a re-render (nodes finishing measurement, say) cancelled the
        // pending rAF via this effect's cleanup while the guard already read as
        // "fitted" — so the view silently never fitted at all.
        fittedForRef.current = seq;
      });
      return () => cancelAnimationFrame(raf);
    }
    return undefined;
  }, [initialized, nodeCount, importSeq, fitView]);

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
  const setPlacementProvider = useVisualStore((s) => s.setPlacementProvider);
  useEffect(() => {
    setPlacementProvider((index: number) => {
      const pane = document.querySelector('.react-flow__pane');
      const r = pane?.getBoundingClientRect();
      if (!r || r.width === 0) return { x: 80 + index * 320, y: 80 };
      // Stagger down-right from just inside the top-left of the visible area, so
      // successive nodes stay reachable without overlapping.
      const step = 48;
      const inset = 32;
      const p = screenToFlowPosition({
        x: r.x + inset + (index % 3) * step,
        y: r.y + inset + (index % 5) * step,
      });
      return { x: Math.round(p.x), y: Math.round(p.y) };
    });
    return () => setPlacementProvider(null);
  }, [screenToFlowPosition, setPlacementProvider]);

  return null;
}

function getSourceReachableEdges(
  nodes: GraphNode[],
  edges: GraphEdge[],
  schema: SchemaPayload,
): Set<string> {
  const nodeMap = new Map(nodes.map((n) => [n.id, n]));
  const sourceNodeIds = new Set(
    nodes
      .filter((n) => !n.disabled && schema.components[n.component]?.category === 'sources')
      .map((n) => n.id),
  );
  const reachable = new Set<string>();
  const queue = [...sourceNodeIds];
  const visited = new Set<string>();
  while (queue.length) {
    const nodeId = queue.shift()!;
    if (visited.has(nodeId)) continue;
    visited.add(nodeId);
    const node = nodeMap.get(nodeId);
    if (!node || node.disabled) continue; // halt at disabled nodes
    for (const edge of edges) {
      if (edge.from.node === nodeId) {
        const target = nodeMap.get(edge.to.node);
        if (target && !target.disabled) {
          reachable.add(edge.id);
          queue.push(edge.to.node);
        }
      }
    }
  }
  return reachable;
}

/*
 * --- The controlled-mode contract -------------------------------------------
 *
 * React Flow runs CONTROLLED here: we own the arrays it renders. That makes the
 * ownership split something we have to honour explicitly, because every way of
 * getting it wrong is silent — no error, no warning, just a feature that does
 * nothing (see docs/reviews/canvas-framework-evaluation.md).
 *
 *   React Flow owns  `selected`, `measured`, `dragging`, and the handle bounds
 *                    it derives from them. These are written ONLY by
 *                    applyNodeChanges/applyEdgeChanges, from its change stream.
 *   The document owns node and edge existence, component, props, label,
 *                    position, disabled, notes. These are written ONLY by the
 *                    store.
 *
 * The arrays below are the projection of the two, and they are RECONCILED,
 * never rebuilt. A node whose inputs are unchanged is returned by the SAME
 * REFERENCE. That is load-bearing rather than an optimisation: `adoptUserNodes`
 * runs with `checkEquality: true` and its fast path is strict identity
 * (`userNode === internals.userNode`), so handing it a fresh object makes it
 * re-adopt the node and throw away its cached handle bounds. The previous code
 * rebuilt every node object on every render, so that path never hit — which is
 * why handles had to be re-measured by hand, and why simply supplying `measured`
 * broke connection dragging.
 */
const EMPTY_DIAGNOSTICS: L1Diagnostic[] = [];

/** The values a projected node is derived from, kept so the reconciler can tell
 * "nothing changed" from "rebuild me" without deep-comparing. `src` is compared
 * by reference, which is exact: the store replaces a node object only when that
 * node actually changes, so this stays correct as `GraphNode` grows fields. */
type NodeInputs = {
  src: GraphNode;
  def: ComponentDef | undefined;
  diags: L1Diagnostic[];
};

function reconcileNodes(
  current: PipelineFlowNode[],
  docNodes: GraphNode[],
  schema: SchemaPayload | null,
  diagnosticsByNode: Map<string, L1Diagnostic[]>,
  selectedIds: Set<string>,
  inputs: Map<string, NodeInputs>,
): PipelineFlowNode[] {
  const byId = new Map(current.map((n) => [n.id, n]));
  const nextInputs = new Map<string, NodeInputs>();
  let changed = current.length !== docNodes.length;

  const next = docNodes.map((src, i) => {
    const def = schema?.components[src.component];
    const diags = diagnosticsByNode.get(src.id) ?? EMPTY_DIAGNOSTICS;
    const selected = selectedIds.has(src.id);
    nextInputs.set(src.id, { src, def, diags });

    const prev = byId.get(src.id);
    const prevIn = inputs.get(src.id);
    // `selected` is compared against the node React Flow is holding, not against
    // the last reconcile's input. When RF drives the selection its change stream
    // has already written the field, so this sees them agree and reuses the node
    // instead of re-adopting it; a selection set programmatically (paste,
    // select-all) still differs here and correctly rebuilds.
    if (
      prev &&
      prevIn &&
      prevIn.src === src &&
      prevIn.def === def &&
      prevIn.diags === diags &&
      prev.selected === selected
    ) {
      // Reused verbatim — including React Flow's own fields, and including the
      // position it is maintaining mid-drag (the document is only written at
      // drag end, so `src` is unchanged for the whole gesture).
      if (current[i] !== prev) changed = true; // same nodes, new order
      return prev;
    }

    changed = true;
    return {
      // `prev` first so React Flow's fields survive a document-driven rebuild.
      // Spreading `undefined` for a brand-new node is a no-op.
      ...prev,
      id: src.id,
      type: 'pipeline' as const,
      position: src.position,
      selected,
      data: { ...src, schema: def, diagnostics: diags } as PipelineNodeData,
    };
  });

  inputs.clear();
  for (const [id, v] of nextInputs) inputs.set(id, v);
  // Returning `current` unchanged lets React bail out of the state update.
  return changed ? next : current;
}

type EdgeInputs = {
  src: GraphEdge;
  wireType: string | undefined;
  animated: boolean;
  fromLabel: string;
  rf: ReturnType<typeof rfEndpointsForEdge>;
};

function reconcileEdges(
  current: Edge[],
  docEdges: GraphEdge[],
  docNodes: GraphNode[],
  schema: SchemaPayload | null,
  flowCheckActive: boolean,
  selectedIds: Set<string>,
  inputs: Map<string, EdgeInputs>,
): Edge[] {
  const reachable =
    flowCheckActive && schema
      ? getSourceReachableEdges(docNodes, docEdges, schema)
      : new Set<string>();
  const byId = new Map(current.map((e) => [e.id, e]));
  const nextInputs = new Map<string, EdgeInputs>();
  let changed = current.length !== docEdges.length;

  const next = docEdges.map((src, i) => {
    const fromNode = docNodes.find((n) => n.id === src.from.node);
    // The wire's type lives on whichever port `from` resolves to — for a
    // receiver-kind wire (D1) that is an ARGUMENT (`forward_to`), not an
    // export, so searching only `.outputs` silently found nothing and rendered
    // an uncolored, unlabeled edge for every such wire. resolvePorts covers both.
    const wireType = resolvePorts(fromNode && schema?.components[fromNode.component]).find(
      (p) => p.id === src.from.port,
    )?.type;
    const animated = flowCheckActive && reachable.has(src.id);
    const fromLabel = fromNode?.label ?? src.from.node;
    const selected = selectedIds.has(src.id);
    // React Flow's source/target are fixed by schema kind (export/argument),
    // independent of the stored edge's produces/accepts orientation — see
    // wireOrient.ts's rfEndpointsForEdge for why handing it `from`/`to`
    // verbatim silently fails to render a receiver-kind wire.
    const rf = rfEndpointsForEdge(schema, { nodes: docNodes }, src);
    nextInputs.set(src.id, { src, wireType, animated, fromLabel, rf });

    const prev = byId.get(src.id);
    const prevIn = inputs.get(src.id);
    if (
      prev &&
      prevIn &&
      prevIn.src === src &&
      prevIn.wireType === wireType &&
      prevIn.animated === animated &&
      prevIn.fromLabel === fromLabel &&
      prev.selected === selected &&
      deepEqual(prevIn.rf, rf)
    ) {
      if (current[i] !== prev) changed = true;
      return prev;
    }

    changed = true;
    return {
      ...prev,
      id: src.id,
      ...rf,
      selected,
      animated,
      style: wireType ? { stroke: getWireColor(schema, wireType) } : undefined,
      // `data` carries the wire's semantics rather than just its looks, so a
      // custom edge component (animated dataflow, live throughput from Alloy)
      // can read them without re-deriving anything from the schema.
      data: { wireType, fromLabel },
      label: animated ? (
        <div data-testid='edge-tooltip'>
          {wireType ?? 'unknown'} · from {fromLabel}
        </div>
      ) : undefined,
      labelShowBg: animated,
    };
  });

  inputs.clear();
  for (const [id, v] of nextInputs) inputs.set(id, v);
  return changed ? next : current;
}

export function CanvasPane() {
  const doc = useVisualStore((s) => s.doc);
  const schema = useVisualStore((s) => s.schema);
  const selected = useVisualStore((s) => s.selected);
  const diagnostics = useVisualStore((s) => s.diagnostics);
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
    reconcileNodes([], doc.nodes, schema, diagnosticsByNode, selectedIds, nodeInputsRef.current),
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
    ),
  );

  // Document -> view. React Flow's own fields are carried across by the
  // reconciler, so this never clobbers a measurement or an in-flight drag.
  useEffect(() => {
    setRfNodes((cur) =>
      reconcileNodes(cur, doc.nodes, schema, diagnosticsByNode, selectedIds, nodeInputsRef.current),
    );
  }, [doc.nodes, schema, diagnosticsByNode, selectedIds]);

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
      ),
    );
  }, [doc.edges, doc.nodes, schema, flowCheckActive, selectedIds]);

  // A2: the in-flight connection line takes the source port's wire color
  // instead of React Flow's default gray bezier.
  const connectionLineStyle = useMemo<CSSProperties>(
    () => ({
      strokeWidth: 2,
      stroke: connectingFrom?.wireType ? getWireColor(schema, connectingFrom.wireType) : undefined,
    }),
    [connectingFrom, schema],
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
      addEdge(from, to);
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
          useVisualStore.temporal.getState().redo();
        } else {
          useVisualStore.temporal.getState().undo();
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
        {/* React Flow's minimap defaults to a light palette; pin it to the dark token layer. */}
        <MiniMap
          position='bottom-left'
          pannable
          zoomable
          bgColor='#0e0e11'
          maskColor='rgba(9,9,11,0.75)'
          nodeColor='#3f3f46'
          nodeStrokeColor='#6366f1'
          className='!border !border-border !rounded-md'
        />
      </ReactFlow>
    </div>
  );
}
