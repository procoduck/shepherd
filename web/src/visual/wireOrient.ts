/**
 * wireOrient.ts — B1 (docs/reviews/README.md): resolves a React Flow
 * connection gesture into the graph document's role-oriented edge.
 *
 * React Flow fixes which handle is "source" and which is "target" by the
 * schema's input/output classification (see PipelineNode.tsx's `layoutPorts`)
 * — an export is always an RF `source` handle and an argument always an RF
 * `target` handle, regardless of D1 ROLE. A receiver-kind export
 * (`prometheus.remote_write.receiver`) is therefore an RF `source` handle
 * even though its role is "accepts": dragging from it to `prometheus.scrape`'s
 * `forward_to` (an RF `target` handle whose role is "produces") is the only
 * gesture RF's strict connectionMode allows for that pair, and it runs
 * backwards relative to dataflow.
 *
 * The graph document, l1.ts and both renderers all key off ROLE instead: an
 * edge's `from` must be a "produces" port and `to` an "accepts" port. This
 * module is the single place that reconciles the two — every caller that
 * turns an RF connection into a stored `GraphEdge` (CanvasPane's
 * isValidConnection and onConnect) goes through it, so they can't drift.
 */
import { canConnectPorts, resolvePorts } from './l1';
import type { GraphDocument, SchemaPayload } from './types';

export interface OrientedEdge {
  from: { node: string; port: string };
  to: { node: string; port: string };
}

export interface RFEndpoints {
  source: string;
  sourceHandle: string;
  target: string;
  targetHandle: string;
}

/**
 * Converts a role-oriented GraphEdge (`from`=produces, `to`=accepts — what
 * orientConnection above stores) into the `source`/`target` pair React Flow
 * needs to actually RENDER the edge.
 *
 * This is a second, independent place the export/argument (RF source/target)
 * split matters, on top of orientConnection's role split: RF hard-requires
 * `source`+`sourceHandle` to name a handle it registered as `source`-type
 * (a schema EXPORT — see PipelineNode.tsx's `rfType`) and `target`+
 * `targetHandle` to name a `target`-type handle (a schema ARGUMENT); handing
 * it the reverse silently fails to resolve the handle and the edge never
 * renders — no DOM node, no error. For a data-kind wire `from` already IS
 * the export, so this is a no-op; for a receiver-kind wire (D1) `from` is
 * the ARGUMENT (`forward_to`) that references the export (`receiver`), so
 * rendering needs the two swapped back. The stored GraphEdge itself (and
 * everything keyed on role — L1, both renderers) is untouched; this
 * function's output is consumed by React Flow's `Edge` props alone.
 */
export function rfEndpointsForEdge(
  schema: SchemaPayload | null | undefined,
  doc: Pick<GraphDocument, 'nodes'>,
  edge: OrientedEdge,
): RFEndpoints {
  const fromNode = doc.nodes.find((n) => n.id === edge.from.node);
  const fromPort = resolvePorts(fromNode && schema?.components[fromNode.component]).find(
    (p) => p.id === edge.from.port,
  );
  if (fromPort?.origin === 'argument') {
    return {
      source: edge.to.node,
      sourceHandle: edge.to.port,
      target: edge.from.node,
      targetHandle: edge.from.port,
    };
  }
  return {
    source: edge.from.node,
    sourceHandle: edge.from.port,
    target: edge.to.node,
    targetHandle: edge.to.port,
  };
}

/** Resolves an RF connection gesture (source/target per RF's fixed
 *  input/output classification) into the role-oriented `{from,to}` the graph
 *  document stores, or `null` when the two ports cannot form a valid
 *  produces/accepts pair in either order (including a self-connection, an
 *  unknown handle, or a schema-less node). */
export function orientConnection(
  schema: SchemaPayload | null | undefined,
  doc: Pick<GraphDocument, 'nodes'>,
  connection: { source: string; sourceHandle: string; target: string; targetHandle: string },
): OrientedEdge | null {
  const { source, sourceHandle, target, targetHandle } = connection;
  if (!schema || !source || !target || source === target) return null;
  const a = doc.nodes.find((n) => n.id === source);
  const b = doc.nodes.find((n) => n.id === target);
  const sourcePort = resolvePorts(a && schema.components[a.component]).find(
    (p) => p.id === sourceHandle,
  );
  const targetPort = resolvePorts(b && schema.components[b.component]).find(
    (p) => p.id === targetHandle,
  );
  if (!sourcePort || !targetPort) return null;
  if (canConnectPorts(sourcePort, targetPort)) {
    return { from: { node: source, port: sourceHandle }, to: { node: target, port: targetHandle } };
  }
  if (canConnectPorts(targetPort, sourcePort)) {
    return { from: { node: target, port: targetHandle }, to: { node: source, port: sourceHandle } };
  }
  return null;
}
