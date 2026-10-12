/**
 * reconcile.ts — W5-06: the pure projection CanvasPane's controlled React
 * Flow mode is built on, extracted so the reference-identity invariant it
 * depends on can be unit-tested without a DOM.
 *
 * --- The controlled-mode contract -------------------------------------------
 *
 * CanvasPane runs React Flow CONTROLLED: it owns the arrays React Flow
 * renders. That makes the ownership split something to honour explicitly,
 * because every way of getting it wrong is silent — no error, no warning,
 * just a feature that does nothing (see docs/reviews/canvas-framework-evaluation.md).
 *
 *   React Flow owns  `selected`, `measured`, `dragging`, and the handle bounds
 *                    it derives from them. These are written ONLY by
 *                    applyNodeChanges/applyEdgeChanges, from its change stream.
 *   The document owns node and edge existence, component, props, label,
 *                    position, disabled, notes. These are written ONLY by the
 *                    store.
 *
 * The functions below are the projection of the two, and they are
 * RECONCILED, never rebuilt. A node whose inputs are unchanged is returned by
 * the SAME REFERENCE. That is load-bearing rather than an optimisation:
 * `adoptUserNodes` runs with `checkEquality: true` and its fast path is
 * strict identity (`userNode === internals.userNode`), so handing it a fresh
 * object makes it re-adopt the node and throw away its cached handle bounds.
 * A version that rebuilt every node object on every render never hit that
 * path — which is why handles had to be re-measured by hand, and why simply
 * supplying `measured` broke connection dragging.
 *
 * Every "unchanged" check below therefore compares its inputs by REFERENCE
 * (`===`), never by deep equality: the store only ever hands out a new
 * `GraphNode`/`GraphEdge` object when that node/edge actually changed, so a
 * changed reference is authoritative even if a deep-equal check would call
 * the content the same.
 */
import type { Edge, Node } from '@xyflow/react';
import deepEqual from 'fast-deep-equal';
import { createElement } from 'react';
import type { Theme } from '../theme';
import type { PipelineNodeData } from './components/PipelineNode';
import { resolvePorts } from './l1';
import { getThemedWireColor } from './schemaAdapter';
import type { SimHealthEntry } from './store';
import type { ComponentDef, GraphEdge, GraphNode, L1Diagnostic, SchemaPayload } from './types';
import { rfEndpointsForEdge } from './wireOrient';

/** The concrete node type CanvasPane renders — pins React Flow's generics so
 * applyNodeChanges returns our node shape rather than the bare NodeBase. */
export type PipelineFlowNode = Node<PipelineNodeData, 'pipeline'>;

// Reused across every node with no diagnostics of its own, so a diagnostic on
// one node doesn't force every OTHER node's projection to see a "new" array.
export const EMPTY_DIAGNOSTICS: L1Diagnostic[] = [];

export function getSourceReachableEdges(
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

/** The values a projected node is derived from, kept so the reconciler can tell
 * "nothing changed" from "rebuild me" without deep-comparing. `src` is compared
 * by reference, which is exact: the store replaces a node object only when that
 * node actually changes, so this stays correct as `GraphNode` grows fields. */
export type NodeInputs = {
  src: GraphNode;
  def: ComponentDef | undefined;
  diags: L1Diagnostic[];
  health: SimHealthEntry | undefined;
};

export function reconcileNodes(
  current: PipelineFlowNode[],
  docNodes: GraphNode[],
  schema: SchemaPayload | null,
  diagnosticsByNode: Map<string, L1Diagnostic[]>,
  selectedIds: Set<string>,
  inputs: Map<string, NodeInputs>,
  simHealthByNode: Record<string, SimHealthEntry> | null,
): PipelineFlowNode[] {
  const byId = new Map(current.map((n) => [n.id, n]));
  const nextInputs = new Map<string, NodeInputs>();
  let changed = current.length !== docNodes.length;

  const next = docNodes.map((src, i) => {
    const def = schema?.components[src.component];
    const diags = diagnosticsByNode.get(src.id) ?? EMPTY_DIAGNOSTICS;
    const health = simHealthByNode?.[src.id];
    const selected = selectedIds.has(src.id);
    nextInputs.set(src.id, { src, def, diags, health });

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
      prevIn.health === health &&
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
      data: { ...src, schema: def, diagnostics: diags, health } as PipelineNodeData,
    };
  });

  inputs.clear();
  for (const [id, v] of nextInputs) inputs.set(id, v);
  // Returning `current` unchanged lets React bail out of the state update.
  return changed ? next : current;
}

export type EdgeInputs = {
  src: GraphEdge;
  wireType: string | undefined;
  animated: boolean;
  fromLabel: string;
  /** The accepting node's label: with fromLabel, the wire's accessible name. */
  toLabel: string;
  rf: ReturnType<typeof rfEndpointsForEdge>;
  /** The stroke colour is theme-dependent (overlay color_light), so a theme
   *  switch must invalidate the cached edge the same way a wire-type change does. */
  theme: Theme;
};

export function reconcileEdges(
  current: Edge[],
  docEdges: GraphEdge[],
  docNodes: GraphNode[],
  schema: SchemaPayload | null,
  flowCheckActive: boolean,
  selectedIds: Set<string>,
  inputs: Map<string, EdgeInputs>,
  theme: Theme,
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
    const toLabel = docNodes.find((n) => n.id === src.to.node)?.label ?? src.to.node;
    const selected = selectedIds.has(src.id);
    // React Flow's source/target are fixed by schema kind (export/argument),
    // independent of the stored edge's produces/accepts orientation — see
    // wireOrient.ts's rfEndpointsForEdge for why handing it `from`/`to`
    // verbatim silently fails to render a receiver-kind wire.
    const rf = rfEndpointsForEdge(schema, { nodes: docNodes }, src);
    nextInputs.set(src.id, { src, wireType, animated, fromLabel, toLabel, rf, theme });

    const prev = byId.get(src.id);
    const prevIn = inputs.get(src.id);
    if (
      prev &&
      prevIn &&
      prevIn.src === src &&
      prevIn.wireType === wireType &&
      prevIn.animated === animated &&
      prevIn.fromLabel === fromLabel &&
      prevIn.toLabel === toLabel &&
      prevIn.theme === theme &&
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
      // #310: React Flow's default name is "Edge from <rf source id> to <rf
      // target id>" — internal ids, and reversed for a receiver-kind wire,
      // whose React Flow endpoints run export→argument. The stored edge runs
      // produces→accepts, which is the dataflow; name it by the node labels.
      ariaLabel: `Edge from ${fromLabel} to ${toLabel}`,
      style: wireType ? { stroke: getThemedWireColor(schema, wireType, theme) } : undefined,
      // `data` carries the wire's semantics rather than just its looks, so a
      // custom edge component (animated dataflow, live throughput from Alloy)
      // can read them without re-deriving anything from the schema.
      data: { wireType, fromLabel },
      label: animated
        ? createElement(
            'div',
            { 'data-testid': 'edge-tooltip' },
            `${wireType ?? 'unknown'} · from ${fromLabel}`,
          )
        : undefined,
      labelShowBg: animated,
    };
  });

  inputs.clear();
  for (const [id, v] of nextInputs) inputs.set(id, v);
  return changed ? next : current;
}
