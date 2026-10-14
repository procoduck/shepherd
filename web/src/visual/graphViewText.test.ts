import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { moduleOfInstance, nodeKindLabel, summariseWarning } from './graphViewText';
import type { GraphNode } from './types';

// The server's real parse of the k8s-monitoring metrics config (four modules).
const metrics = JSON.parse(
  readFileSync(
    new URL('../../tests/fixtures/k8s-monitoring-metrics-graphview.json', import.meta.url),
    'utf8',
  ),
) as { graph: { nodes: GraphNode[] }; warning: string };

const node = (o: Partial<GraphNode>): GraphNode =>
  ({
    id: 'n',
    component: 'discovery.relabel',
    label: 'x',
    position: { x: 0, y: 0 },
    props: {},
    ...o,
  }) as GraphNode;

describe('summariseWarning', () => {
  it('folds each module and its instance into one line, keeping every item', () => {
    const s = summariseWarning(metrics.warning, metrics.graph.nodes);
    expect(s.items).toHaveLength(8);
    expect(s.lines).toEqual([
      '4 modules are shown as groups of their components, their instances as read-only nodes — the builder cannot edit modules.',
    ]);
    expect(s.unmapped).toBe(false);
  });

  it('counts a block that is not a module instance as unmapped', () => {
    const s = summariseWarning(
      'this pipeline could not be fully mapped to a graph: module "m" is shown as a group of its components (the builder cannot edit modules); "m" is not a component and cannot be edited as a node; "weird" is not a component and cannot be edited as a node',
      [node({ id: 'd', component: 'declare', label: 'm' })],
    );
    expect(s.lines).toEqual([
      '1 module is shown as a group of its components, its instance as a read-only node — the builder cannot edit modules.',
      '1 item could not be mapped to the graph: "weird" is not a component and cannot be edited as a node',
    ]);
    expect(s.unmapped).toBe(true);
  });

  it('treats a warning in another shape as one unmapped item', () => {
    const s = summariseWarning('some expressions could not be mapped', []);
    expect(s.items).toEqual(['some expressions could not be mapped']);
    expect(s.unmapped).toBe(true);
  });
});

describe('moduleOfInstance', () => {
  it('finds the declare an instance instantiates', () => {
    const inst = metrics.graph.nodes.find((n) => n.component === 'opaque.cluster_metrics');
    expect(moduleOfInstance(inst as GraphNode, metrics.graph.nodes)?.id).toBe(
      'n_declare_cluster_metrics',
    );
  });

  it('is undefined for a component, or an opaque block with no module', () => {
    expect(moduleOfInstance(node({}), metrics.graph.nodes)).toBeUndefined();
    expect(
      moduleOfInstance(node({ component: 'opaque.nope' }), metrics.graph.nodes),
    ).toBeUndefined();
  });
});

describe('nodeKindLabel', () => {
  it('names a node by its kind and label', () => {
    expect(nodeKindLabel(node({ component: 'discovery.relabel', label: 'nodes' }))).toBe(
      'discovery.relabel nodes',
    );
    expect(nodeKindLabel(node({ component: 'declare', label: 'cluster_metrics' }))).toBe(
      'module cluster_metrics',
    );
    expect(nodeKindLabel(node({ component: 'opaque.cluster_metrics', label: 'feature' }))).toBe(
      'cluster_metrics feature',
    );
  });
});
