import { readFileSync } from 'node:fs';
import ELK from 'elkjs/lib/elk.bundled.js';
import { describe, expect, it } from 'vitest';
import { shippedSchema } from '../../tests/fixtures/schema-fixture';
import { graphViewFlow } from './graphViewFlow';
import {
  COLLAPSE_CHILDREN_OVER,
  defaultCollapsed,
  groupPorts,
  groupTree,
  isModuleGroup,
  layoutGraph,
} from './graphViewLayout';
import type { GraphDocument } from './types';

// The server's real parse of grafana/k8s-monitoring 4.4.0's alloy-metrics
// config with modules expanded (internal/visual/parse_modules_test.go keeps it
// in sync): four `declare` groups holding 30 of its 33 components.
const metrics = JSON.parse(
  readFileSync(
    new URL('../../tests/fixtures/k8s-monitoring-metrics-graphview.json', import.meta.url),
    'utf8',
  ),
) as { graph: GraphDocument };

const def = (component: string) => shippedSchema.components[component];

type Box = { x: number; y: number; width: number; height: number };
const overlaps = (a: Box, b: Box) =>
  a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;

async function layout(collapsed = new Set<string>()) {
  const flow = graphViewFlow(metrics.graph, def);
  const result = await layoutGraph(new ELK(), {
    nodes: metrics.graph.nodes,
    edges: flow.edges,
    collapsed,
    sizes: new Map(),
  });
  // Absolute boxes: a child's position is relative to its group.
  const tree = groupTree(metrics.graph.nodes);
  const boxes = new Map<string, Box>();
  const abs = (id: string): { x: number; y: number } => {
    const p = result.positions.get(id) ?? { x: 0, y: 0 };
    const parent = tree.parentOf.get(id);
    if (!parent) return p;
    const o = abs(parent);
    return { x: o.x + p.x, y: o.y + p.y };
  };
  for (const id of result.positions.keys()) {
    const size = result.groupSizes.get(id) ?? { width: 220, height: 80 };
    boxes.set(id, { ...abs(id), ...size });
  }
  return { result, boxes, tree, flow };
}

describe('graphViewLayout on the k8s-monitoring metrics config', () => {
  it('finds the four module groups and their children', () => {
    const tree = groupTree(metrics.graph.nodes);
    expect([...tree.groups]).toHaveLength(4);
    expect(metrics.graph.nodes.filter(isModuleGroup)).toHaveLength(4);
    const sizes = [...tree.groups].map((g) => tree.children.get(g)?.length).sort();
    expect(sizes).toEqual([15, 3, 4, 8]);
    // None crosses the collapse threshold, so the chart's config opens expanded.
    expect(Math.max(...sizes.map(Number))).toBeLessThanOrEqual(COLLAPSE_CHILDREN_OVER);
    expect(defaultCollapsed(tree).size).toBe(0);
  });

  it('puts each group port on the side data crosses it on', () => {
    const flow = graphViewFlow(metrics.graph, def);
    const tree = groupTree(metrics.graph.nodes);
    const ports = groupPorts(flow.edges, tree.groups);
    for (const g of tree.groups) {
      // metrics_destinations carries receivers out of the module: right edge.
      expect(ports.get(g)?.map((p) => p.key)).toEqual(['right:argument.metrics_destinations']);
    }
  });

  it('lays out every node with no two siblings overlapping and children inside their group', async () => {
    const { result, boxes, tree } = await layout();
    expect(result.positions.size).toBe(metrics.graph.nodes.length);
    const ids = [...boxes.keys()];
    for (let i = 0; i < ids.length; i++) {
      for (let j = i + 1; j < ids.length; j++) {
        const [a, b] = [ids[i], ids[j]];
        if (tree.parentOf.get(a) !== tree.parentOf.get(b)) continue;
        expect(overlaps(boxes.get(a) as Box, boxes.get(b) as Box), `${a} overlaps ${b}`).toBe(
          false,
        );
      }
    }
    for (const [id, parent] of tree.parentOf) {
      if (!parent) continue;
      const c = boxes.get(id) as Box;
      const g = boxes.get(parent) as Box;
      expect(c.x >= g.x && c.y >= g.y, `${id} starts outside its group`).toBe(true);
      expect(c.x + c.width <= g.x + g.width && c.y + c.height <= g.y + g.height).toBe(true);
    }
    // Measured on the 886-line config: logged for the PR, bounded loosely so
    // a slow CI runner does not flake.
    console.info(`ELK layout of ${metrics.graph.nodes.length} nodes: ${result.ms.toFixed(0)} ms`);
    expect(result.ms).toBeLessThan(5000);
  });

  it("hides a collapsed group's contents and the edges inside it", async () => {
    const tree = groupTree(metrics.graph.nodes);
    const cluster = metrics.graph.nodes.find(
      (n) => isModuleGroup(n) && n.label === 'cluster_metrics',
    );
    if (!cluster) throw new Error('no cluster_metrics group');
    const { result, flow } = await layout(new Set([cluster.id]));
    const hidden = tree.children.get(cluster.id) ?? [];
    expect(hidden).toHaveLength(15);
    for (const id of hidden) expect(result.positions.has(id)).toBe(false);
    expect(result.positions.has(cluster.id)).toBe(true);
    expect(result.edges.length).toBeLessThan(flow.edges.length);
    // The group still connects to its instance.
    expect(result.edges.some((e) => e.source === cluster.id)).toBe(true);
    const size = result.groupSizes.get(cluster.id);
    expect(size?.width).toBe(240);
  });

  it('lays out an edge through a group, from its input port to its output port', async () => {
    const doc = {
      nodes: [
        { id: 'g', component: 'declare' },
        { id: 'r', component: 'discovery.relabel', parent: 'g' },
        { id: 'i', component: 'opaque.m' },
      ],
      edges: [
        { id: 'in', from: { node: 'g', port: 'argument.in' }, to: { node: 'r', port: 'targets' } },
        {
          id: 'through',
          from: { node: 'g', port: 'argument.in' },
          to: { node: 'g', port: 'export.out' },
        },
        { id: 'link', from: { node: 'g', port: 'export.out' }, to: { node: 'i', port: 'out' } },
      ],
    } as unknown as GraphDocument;
    const flow = graphViewFlow(doc, def);
    const result = await layoutGraph(new ELK(), {
      nodes: doc.nodes,
      edges: flow.edges,
      collapsed: new Set(),
      sizes: new Map(),
    });
    expect(result.edges).toHaveLength(3);
    expect(result.portTops.get('g')?.size).toBe(2);
  });
});
