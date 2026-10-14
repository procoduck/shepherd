/**
 * graphViewFlow.ts — React Flow edges for the read-only graph view of a
 * parsed (not builder-made) pipeline.
 *
 * React Flow draws an edge only when `sourceHandle` names a `source`-type
 * handle on the source node and `targetHandle` a `target`-type handle on the
 * target node (strict connection mode); anything else is dropped without a
 * DOM node or an error. A parsed graph breaks that in two ways the builder
 * never does:
 *   - an opaque node (`opaque.*`: a `declare`, a custom component's instance)
 *     has no schema, so no handles at all — every edge touching it vanished;
 *   - an edge whose ports the schema does give the same RF type at both ends
 *     (or a receiver-kind wire, where the referencing ARGUMENT is the `from`)
 *     names the wrong handle type for one end.
 * So each end uses the node's own schema handle when it has one for that port,
 * the edge is turned around when that is what lines both ends up, and an end
 * left without a fitting handle gets a generic one, derived from the edge —
 * placed by dataflow (the `from` end on the node's right, the `to` end on its
 * left) so the canvas still reads left to right.
 */
import { type GenericPort, layoutPorts, type PortLayout } from './components/PipelineNode';
import type { ComponentDef, GraphDocument } from './types';

export type { GenericPort };

export interface GraphViewEdge {
  id: string;
  /** React Flow holds this edge against dataflow (target = the dataflow
   *  source); the layout orders nodes by dataflow, so it turns it back. */
  reversed?: boolean;
  source: string;
  sourceHandle: string;
  target: string;
  targetHandle: string;
}

export interface GraphViewFlow {
  edges: GraphViewEdge[];
  /** Per node id, the generic handles to draw after its schema ports. */
  genericPorts: Record<string, GenericPort[]>;
}

/** Handle id of a generic port: namespaced so it can never collide with a
 *  schema port's id (a node may already use `receiver` for the other type). */
export const genericHandleId = (side: 'left' | 'right', port: string) => `generic:${side}:${port}`;

export function graphViewFlow(
  doc: Pick<GraphDocument, 'nodes' | 'edges'>,
  componentDef: (component: string) => ComponentDef | undefined,
): GraphViewFlow {
  const ports = new Map<string, PortLayout[]>();
  for (const n of doc.nodes) {
    const { left, right } = layoutPorts(componentDef(n.component));
    ports.set(n.id, [...left, ...right]);
  }
  const handle = (node: string, port: string) => ports.get(node)?.find((p) => p.handleId === port);

  const genericPorts: Record<string, GenericPort[]> = {};
  const generic = (
    node: string,
    port: string,
    rfType: 'source' | 'target',
    side: 'left' | 'right',
  ) => {
    const handleId = genericHandleId(side, port);
    const list = genericPorts[node] ?? [];
    if (!list.some((p) => p.handleId === handleId && p.rfType === rfType))
      list.push({ handleId, label: port, rfType, side });
    genericPorts[node] = list;
    return handleId;
  };

  // A module group's port sits on the side data crosses it on (graphView
  // Layout.ts): an edge between the group and a node inside it reaches the
  // port from within, so the inside end's side is the group's opposite one —
  // data leaving the module goes out through a port on its right edge, data
  // entering it comes in on the left.
  const parentOf = new Map(doc.nodes.map((n) => [n.id, n.parent ?? '']));
  // An edge from a group to itself (an export passing an argument straight
  // through) runs inside it: both its ends are reached from within.
  const inside = (group: string, node: string) => {
    if (node === group) return true;
    const seen = new Set<string>();
    for (let p = parentOf.get(node) ?? ''; p && !seen.has(p); p = parentOf.get(p) ?? '') {
      if (p === group) return true;
      seen.add(p);
    }
    return false;
  };
  /** The side of the generic handle at one end of an edge: the from end on
   *  the node's right, the to end on its left — turned around for a group
   *  reached from inside. */
  const side = (node: string, isFrom: boolean, other: string): 'left' | 'right' =>
    isFrom !== inside(node, other) ? 'right' : 'left';

  const edges = doc.edges.map((e) => {
    const from = handle(e.from.node, e.from.port);
    const to = handle(e.to.node, e.to.port);
    // Turned around when the schema says so: the `from` end is an RF target
    // (a receiver-kind argument such as forward_to) and the `to` end is an RF
    // source (the receiver export it references), or one of the two is.
    const reversed =
      (from?.rfType === 'target' && to?.rfType !== 'target') ||
      (from === undefined && to?.rfType === 'source');
    if (reversed) {
      return {
        id: e.id,
        reversed: true,
        source: e.to.node,
        sourceHandle:
          to?.rfType === 'source'
            ? to.handleId
            : generic(e.to.node, e.to.port, 'source', side(e.to.node, false, e.from.node)),
        target: e.from.node,
        targetHandle:
          from?.rfType === 'target'
            ? from.handleId
            : generic(e.from.node, e.from.port, 'target', side(e.from.node, true, e.to.node)),
      };
    }
    return {
      id: e.id,
      source: e.from.node,
      sourceHandle:
        from?.rfType === 'source'
          ? from.handleId
          : generic(e.from.node, e.from.port, 'source', side(e.from.node, true, e.to.node)),
      target: e.to.node,
      targetHandle:
        to?.rfType === 'target'
          ? to.handleId
          : generic(e.to.node, e.to.port, 'target', side(e.to.node, false, e.from.node)),
    };
  });
  return { edges, genericPorts };
}

/**
 * The editable draft for a GraphView response — what the builder imports on
 * its fallback load (a pipeline without a usable wizard_state) and what
 * "Recreate as visual pipeline" hands it. GraphView's `bindings` are a
 * display-only projection of references the parser kept inside expressions
 * (internal/visual/parse.go, ParseResult.Bindings): each expression is
 * already in its node's props, so a binding carried into the builder renders
 * the attribute a second time — at the node's top level even when the prop is
 * nested. Every consumer that edits or renders a GraphView graph goes through
 * here.
 */
export function builderDraftFromGraphView<T extends Pick<GraphDocument, 'bindings' | 'nodes'>>(
  graph: T,
): T {
  // `parent` too (GraphView's module grouping): the builder has no modules,
  // and the server's Render refuses a node that carries one. A graph with a
  // module is opaque and never gets here; this keeps it that way regardless.
  return {
    ...graph,
    nodes: graph.nodes.map(({ parent: _, ...n }) => n),
    bindings: [],
  };
}

/** Whether a parsed node is one the graph model could not map (ParseAlloy
 *  prefixes its component with `opaque.`). */
export const isOpaqueNode = (component: string) => component.startsWith('opaque.');

/** The key ParseAlloy stores an opaque block's printed source under. */
export const RAW_KEY = '$raw';
