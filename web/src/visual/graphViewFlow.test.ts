import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { shippedSchema } from '../../tests/fixtures/schema-fixture';
import { layoutPorts } from './components/PipelineNode';
import { genericHandleId, graphViewFlow } from './graphViewFlow';
import type { GraphDocument } from './types';

const def = (component: string) => shippedSchema.components[component];

// The server's real parse of a trimmed grafana/k8s-monitoring config (kept in
// sync by internal/visual/parse_graphview_test.go).
const k8s = JSON.parse(
  readFileSync(
    new URL('../../tests/fixtures/k8s-monitoring-graphview.json', import.meta.url),
    'utf8',
  ),
) as { graph: GraphDocument };

/** Whether React Flow (strict mode) can draw an edge: its source handle is a
 *  `source` handle on the source node, its target handle a `target` one. */
function drawable(
  doc: GraphDocument,
  flow: ReturnType<typeof graphViewFlow>,
  edge: ReturnType<typeof graphViewFlow>['edges'][number],
) {
  const handles = (id: string) => {
    const n = doc.nodes.find((x) => x.id === id);
    const { left, right } = layoutPorts(n && def(n.component), flow.genericPorts[id]);
    return [...left, ...right];
  };
  return (
    handles(edge.source).some((h) => h.handleId === edge.sourceHandle && h.rfType === 'source') &&
    handles(edge.target).some((h) => h.handleId === edge.targetHandle && h.rfType === 'target')
  );
}

describe('graphViewFlow', () => {
  it('gives every edge of the k8s-monitoring graph a handle at both ends', () => {
    const flow = graphViewFlow(k8s.graph, def);
    expect(flow.edges).toHaveLength(k8s.graph.edges.length);
    for (const e of flow.edges) expect(drawable(k8s.graph, flow, e), e.id).toBe(true);
  });

  it('adds generic handles only where the schema has none, by dataflow side', () => {
    const flow = graphViewFlow(k8s.graph, def);
    // The opaque instance feeds loki.write's receiver: its handle sits on its
    // right, and loki.write keeps its own schema handle.
    expect(flow.genericPorts.n_opaque_cluster_events_feature).toEqual([
      {
        handleId: genericHandleId('right', 'logs_destinations'),
        label: 'logs_destinations',
        rfType: 'target',
        side: 'right',
      },
    ]);
    expect(Object.keys(flow.genericPorts)).toEqual(['n_opaque_cluster_events_feature']);
  });

  it('turns a receiver-kind wire around to the RF handle types it uses', () => {
    const doc = {
      nodes: [
        { id: 's', component: 'prometheus.scrape' },
        { id: 'w', component: 'prometheus.remote_write' },
      ],
      edges: [
        { id: 'e', from: { node: 's', port: 'forward_to' }, to: { node: 'w', port: 'receiver' } },
      ],
    } as unknown as GraphDocument;
    const flow = graphViewFlow(doc, def);
    expect(flow.edges[0]).toMatchObject({
      source: 'w',
      sourceHandle: 'receiver',
      target: 's',
      targetHandle: 'forward_to',
    });
    expect(flow.genericPorts).toEqual({});
  });

  it('draws an edge between two unmapped nodes on generic handles', () => {
    const doc = {
      nodes: [
        { id: 'a', component: 'opaque.a' },
        { id: 'b', component: 'opaque.b' },
      ],
      edges: [{ id: 'e', from: { node: 'a', port: 'out' }, to: { node: 'b', port: 'in' } }],
    } as unknown as GraphDocument;
    const flow = graphViewFlow(doc, def);
    expect(drawable(doc, flow, flow.edges[0])).toBe(true);
    expect(flow.genericPorts.a[0].side).toBe('right');
    expect(flow.genericPorts.b[0].side).toBe('left');
  });
});
