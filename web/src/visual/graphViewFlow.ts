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
        source: e.to.node,
        sourceHandle:
          to?.rfType === 'source' ? to.handleId : generic(e.to.node, e.to.port, 'source', 'left'),
        target: e.from.node,
        targetHandle:
          from?.rfType === 'target'
            ? from.handleId
            : generic(e.from.node, e.from.port, 'target', 'right'),
      };
    }
    return {
      id: e.id,
      source: e.from.node,
      sourceHandle:
        from?.rfType === 'source'
          ? from.handleId
          : generic(e.from.node, e.from.port, 'source', 'right'),
      target: e.to.node,
      targetHandle:
        to?.rfType === 'target' ? to.handleId : generic(e.to.node, e.to.port, 'target', 'left'),
    };
  });
  return { edges, genericPorts };
}

/** Whether a parsed node is one the graph model could not map (ParseAlloy
 *  prefixes its component with `opaque.`). */
export const isOpaqueNode = (component: string) => component.startsWith('opaque.');

/** The key ParseAlloy stores an opaque block's printed source under. */
export const RAW_KEY = '$raw';
