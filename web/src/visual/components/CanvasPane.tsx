import {
  Background,
  type Connection,
  Controls,
  type Edge,
  type EdgeChange,
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
import { portsCompatible } from '../l1';
import { getWireColor, portHandleId } from '../schemaAdapter';
import { type ConnectingFrom, useVisualStore } from '../store';
import type { GraphEdge, GraphNode, SchemaPayload } from '../types';
import type { PipelineNodeData } from './PipelineNode';
import { PipelineNode } from './PipelineNode';

const nodeTypes: NodeTypes = { pipeline: PipelineNode as NodeTypes[string] };

// Clipboard data is scoped to this canvas instance, preventing cross-pipeline pastes.
type Clipboard = { nodes: GraphNode[]; edges: GraphEdge[] };

function FitOnFirstNodes() {
  const { fitView } = useReactFlow();
  const initialized = useNodesInitialized();
  const nodeCount = useVisualStore((s) => s.doc.nodes.length);
  const [hasFit, setHasFit] = useState(false);

  useEffect(() => {
    if (initialized && nodeCount > 0 && !hasFit) {
      fitView({ padding: 0.15, duration: 200 });
      setHasFit(true);
    }
    if (nodeCount === 0) setHasFit(false);
  }, [initialized, nodeCount, hasFit, fitView]);

  return null;
}

function FlowApiBridge({
  screenToFlowRef,
}: {
  screenToFlowRef: React.MutableRefObject<((position: XYPosition) => XYPosition) | null>;
}) {
  const { screenToFlowPosition } = useReactFlow();
  screenToFlowRef.current = screenToFlowPosition;
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

export function CanvasPane() {
  const doc = useVisualStore((s) => s.doc);
  const schema = useVisualStore((s) => s.schema);
  const selected = useVisualStore((s) => s.selected);
  const diagnostics = useVisualStore((s) => s.diagnostics);
  const flowCheckActive = useVisualStore((s) => s.flowCheckActive);
  const addEdge = useVisualStore((s) => s.addEdge);
  const removeEdge = useVisualStore((s) => s.removeEdge);
  const removeNode = useVisualStore((s) => s.removeNode);
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

  // --- Node array for React Flow ---
  // Pass only the per-node subset of diagnostics so memo only invalidates
  // the affected node rows rather than the entire list.
  const rfNodes = useMemo<Node<PipelineNodeData>[]>(() => {
    const byNode = new Map<string, typeof diagnostics>();
    for (const d of diagnostics) {
      if (d.node_id) {
        const arr = byNode.get(d.node_id) ?? [];
        arr.push(d);
        byNode.set(d.node_id, arr);
      }
    }
    return doc.nodes.map((n) => ({
      id: n.id,
      type: 'pipeline',
      position: n.position,
      // Do NOT pass `selected` here — let React Flow own its selection state.
      // We read the selection back via onSelectionChange to sync our store.
      // Passing controlled `selected` fights RF's internal state and breaks click-selection.
      data: {
        ...n,
        // Pass isSelected separately so PipelineNode can style it without RF's controlled prop.
        isSelected: selected.includes(n.id),
        schema: schema?.components[n.component],
        diagnostics: byNode.get(n.id) ?? [],
        // connectingFrom deliberately NOT passed here (A3) — it would force this
        // memo (and every node's data, hence every PipelineNode) to recompute on
        // every drag start/end. PipelineNode reads it straight from the store
        // via a narrow per-node selector instead.
      } as PipelineNodeData,
    }));
  }, [doc.nodes, selected, schema, diagnostics]);

  const rfEdges = useMemo<Edge[]>(() => {
    const reachable =
      flowCheckActive && schema
        ? getSourceReachableEdges(doc.nodes, doc.edges, schema)
        : new Set<string>();
    return doc.edges.map((e) => {
      const fromNode = doc.nodes.find((n) => n.id === e.from.node);
      const wireType =
        fromNode &&
        schema?.components[fromNode.component]?.outputs.find(
          (o, i) => portHandleId(o, i) === e.from.port,
        )?.type;
      const animated = flowCheckActive && reachable.has(e.id);
      return {
        id: e.id,
        source: e.from.node,
        sourceHandle: e.from.port,
        target: e.to.node,
        targetHandle: e.to.port,
        animated,
        style: wireType ? { stroke: getWireColor(schema, wireType) } : undefined,
        label: animated ? (
          <div data-testid='edge-tooltip'>
            {wireType ?? 'unknown'} · from {fromNode?.label ?? e.from.node}
          </div>
        ) : undefined,
        labelShowBg: animated,
      };
    });
  }, [doc.edges, doc.nodes, flowCheckActive, schema]);

  // A2: the in-flight connection line takes the source port's wire color
  // instead of React Flow's default gray bezier.
  const connectionLineStyle = useMemo<CSSProperties>(
    () => ({
      strokeWidth: 2,
      stroke: connectingFrom?.wireType ? getWireColor(schema, connectingFrom.wireType) : undefined,
    }),
    [connectingFrom, schema],
  );

  // --- Controlled mode: handle React Flow's internally-generated changes ---
  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      for (const c of changes) {
        if (c.type === 'position' && c.position) {
          updateNode(c.id, { position: c.position });
        }
        if (c.type === 'remove') {
          removeNode(c.id);
        }
        // 'select' changes are handled exclusively by onSelectionChange.
      }
    },
    [updateNode, removeNode],
  );

  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      for (const c of changes) {
        if (c.type === 'remove') {
          removeEdge(c.id);
        }
      }
    },
    [removeEdge],
  );

  // --- Single selection handler (gated, no onNodeClick dual-write) ---
  const onSelectionChange = useCallback(
    ({ nodes: ns }: OnSelectionChangeParams) => {
      const next = ns.map((n) => n.id);
      if (deepEqual(lastSelRef.current, next)) return;
      lastSelRef.current = next;
      syncSetSelected(next);
    },
    [setSelected],
  );

  // --- Type-check wires ---
  const isValidConnection = useCallback(
    (c: Connection | Edge) => {
      const sc = schemaRef.current;
      const d = docRef.current;
      if (!sc || !c.source || !c.target || c.source === c.target) return false;
      const a = d.nodes.find((n) => n.id === c.source);
      const b = d.nodes.find((n) => n.id === c.target);
      const ad = a && sc.components[a.component];
      const bd = b && sc.components[b.component];
      const out = ad?.outputs.find((p, i) => portHandleId(p, i) === (c.sourceHandle ?? ''));
      const inp = bd?.inputs.find((p, i) => portHandleId(p, i) === (c.targetHandle ?? ''));
      return !!out && !!inp && portsCompatible(out.type, inp.type);
    },
    [], // reads only stable refs
  );

  // --- Direct click-to-select fallback ---
  // React Flow's onSelectionChange is reliable for multi-select via drag-box and
  // keyboard, but Playwright `click({ force: true })` on a node's inner div doesn't
  // always trigger RF's internal selection handler. We add a click handler on the
  // canvas wrapper that walks up to the nearest .react-flow__node and syncs selection.
  const onCanvasClick = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      // Skip double-clicks — let PipelineNode's onDoubleClick handle inline editing.
      if (e.detail >= 2) return;

      const target = e.target as HTMLElement;
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
  const onConnect = useCallback(
    (c: Connection) => {
      if (!c.source || !c.target) return;
      const d = docRef.current;
      const adj = new Map(d.nodes.map((n) => [n.id, [] as string[]]));
      for (const e of d.edges) adj.get(e.from.node)?.push(e.to.node);
      adj.get(c.source)?.push(c.target);
      const seen = new Set<string>();
      const reachesSource = (u: string): boolean => {
        if (u === c.source) return true;
        if (seen.has(u)) return false;
        seen.add(u);
        return (adj.get(u) ?? []).some(reachesSource);
      };
      if (reachesSource(c.target)) {
        toast.error('Alloy graphs are acyclic — this connection would create a cycle');
        return;
      }
      addEdge(
        { node: c.source, port: c.sourceHandle ?? '' },
        { node: c.target, port: c.targetHandle ?? '' },
      );
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

  const onConnectEnd = useCallback(() => {
    setConnectingFrom(null);
  }, [setConnectingFrom]);

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

  // --- Keyboard shortcuts (copy/paste/undo/redo/select-all) ---
  const onKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLDivElement>) => {
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
    [pasteNodesAndEdges, syncSetSelected],
  );

  return (
    <div
      className='flex-1 relative outline-none'
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
      >
        <FlowApiBridge screenToFlowRef={screenToFlowRef} />
        <FitOnFirstNodes />
        <Background />
        <Controls />
        {/* bottom-left avoids overlap with default node placement area (center/right) */}
        <MiniMap position='bottom-left' />
      </ReactFlow>
    </div>
  );
}
