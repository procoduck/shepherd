import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
  edgeNames,
  moduleOfInstance,
  moduleResolver,
  nodeKindLabel,
  summariseWarning,
} from './graphViewText';
import type { GraphNode } from './types';

// The server's real parse of the k8s-monitoring metrics config (four modules).
const metrics = JSON.parse(
  readFileSync(
    new URL('../../tests/fixtures/k8s-monitoring-metrics-graphview.json', import.meta.url),
    'utf8',
  ),
) as { graph: { nodes: GraphNode[]; edges: unknown[] }; warning: string };

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

describe('summariseWarning instance wording', () => {
  const line = (m: string) =>
    `module "${m}" is shown as a group of its components (the builder cannot edit modules)`;
  const inst = (m: string) => `"${m}" is not a component and cannot be edited as a node`;
  const decl = (m: string) => node({ id: `d_${m}`, component: 'declare', label: m });
  const warn = (...items: string[]) =>
    `this pipeline could not be fully mapped to a graph: ${items.join('; ')}`;

  it('counts the instances when they do not pair one-to-one with the modules', () => {
    expect(
      summariseWarning(warn(line('a'), line('b'), inst('a')), [decl('a'), decl('b')]).lines,
    ).toEqual([
      '2 modules are shown as groups of their components, 1 instance as a read-only node — the builder cannot edit modules.',
    ]);
    expect(summariseWarning(warn(line('a'), inst('a'), inst('a')), [decl('a')]).lines).toEqual([
      '1 module is shown as a group of its components, 2 instances as read-only nodes — the builder cannot edit modules.',
    ]);
    expect(summariseWarning(warn(line('a')), [decl('a')]).lines).toEqual([
      '1 module is shown as a group of its components — the builder cannot edit modules.',
    ]);
  });
});

describe('moduleOfInstance scoping', () => {
  // Two sibling modules each declare an inner module "m"; the top level
  // declares one too.
  const nodes = [
    node({ id: 'top_m', component: 'declare', label: 'm' }),
    node({ id: 'a', component: 'declare', label: 'a' }),
    node({ id: 'a_m', component: 'declare', label: 'm', parent: 'a' }),
    node({ id: 'b', component: 'declare', label: 'b' }),
    node({ id: 'b_m', component: 'declare', label: 'm', parent: 'b' }),
    node({ id: 'c', component: 'declare', label: 'c' }),
    node({ id: 'c_inner', component: 'declare', label: 'inner', parent: 'c' }),
  ];
  const instance = (parent?: string) =>
    node({ id: `i_${parent}`, component: 'opaque.m', label: 'x', parent });

  it("resolves an instance to its own scope's module, never a sibling's", () => {
    expect(moduleOfInstance(instance('b'), nodes)?.id).toBe('b_m');
    expect(moduleOfInstance(instance('a'), nodes)?.id).toBe('a_m');
    expect(moduleOfInstance(instance(), nodes)?.id).toBe('top_m');
  });

  it('walks up to an enclosing scope', () => {
    expect(moduleOfInstance(instance('c_inner'), nodes)?.id).toBe('top_m');
    expect(moduleOfInstance(instance('c_inner'), nodes.slice(1))).toBeUndefined();
  });
});

describe('edgeNames', () => {
  it('adds the ports to parallel edges between the same nodes (OTel batch to exporter)', () => {
    const nodes = [
      node({ id: 'batch', component: 'otelcol.processor.batch', label: 'default' }),
      node({ id: 'otlp', component: 'otelcol.exporter.otlp', label: 'default' }),
      node({ id: 'scrape', component: 'prometheus.scrape', label: 'x' }),
      node({ id: 'rw', component: 'prometheus.remote_write', label: 'x' }),
    ];
    const e = (id: string, from: string, fp: string, to: string, tp: string) => ({
      id,
      from: { node: from, port: fp },
      to: { node: to, port: tp },
    });
    const names = edgeNames(
      [
        e('m', 'batch', 'metrics', 'otlp', 'input'),
        e('l', 'batch', 'logs', 'otlp', 'input'),
        e('t', 'batch', 'traces', 'otlp', 'input'),
        e('s', 'scrape', 'metrics', 'rw', 'receiver'),
      ],
      nodes,
    );
    expect([...names.values()]).toEqual([
      'Edge from otelcol.processor.batch default to otelcol.exporter.otlp default (metrics to input)',
      'Edge from otelcol.processor.batch default to otelcol.exporter.otlp default (logs to input)',
      'Edge from otelcol.processor.batch default to otelcol.exporter.otlp default (traces to input)',
      // Unambiguous names stay short.
      'Edge from prometheus.scrape x to prometheus.remote_write x',
    ]);
  });

  it('names the module for the same kind and label in two module scopes, then numbers', () => {
    const nodes = [
      node({ id: 'a', component: 'declare', label: 'a' }),
      node({ id: 'b', component: 'declare', label: 'b' }),
      node({ id: 'a_r', component: 'discovery.relabel', label: 'nodes', parent: 'a' }),
      node({ id: 'a_s', component: 'prometheus.scrape', label: 'nodes', parent: 'a' }),
      node({ id: 'b_r', component: 'discovery.relabel', label: 'nodes', parent: 'b' }),
      node({ id: 'b_s', component: 'prometheus.scrape', label: 'nodes', parent: 'b' }),
    ];
    const e = (id: string, from: string, to: string) => ({
      id,
      from: { node: from, port: 'output' },
      to: { node: to, port: 'targets' },
    });
    const names = edgeNames(
      [e('1', 'a_r', 'a_s'), e('2', 'b_r', 'b_s'), e('3', 'b_r', 'b_s')],
      nodes,
    );
    const base =
      'Edge from discovery.relabel nodes to prometheus.scrape nodes (output to targets) in module';
    expect([...names.values()]).toEqual([`${base} a`, `${base} b`, `${base} b (2)`]);
    expect(new Set(names.values()).size).toBe(3);
  });

  it('gives every edge of the metrics fixture a distinct name', () => {
    const graph = metrics.graph as unknown as {
      nodes: GraphNode[];
      edges: Parameters<typeof edgeNames>[0];
    };
    const names = edgeNames(graph.edges, graph.nodes);
    expect(new Set(names.values()).size).toBe(graph.edges.length);
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

describe('moduleResolver', () => {
  it('resolves every node of a graph as moduleOfInstance does', () => {
    const resolve = moduleResolver(metrics.graph.nodes);
    for (const n of metrics.graph.nodes) {
      expect(resolve(n)?.id).toBe(moduleOfInstance(n, metrics.graph.nodes)?.id);
    }
  });

  it('keeps scoping across a chart-sized graph', () => {
    // 300 sibling modules, each declaring an inner "m" and instantiating it,
    // plus a top-level "m": each instance resolves within its own module.
    const nodes = [node({ id: 'top_m', component: 'declare', label: 'm' })];
    for (let i = 0; i < 300; i++) {
      nodes.push(
        node({ id: `mod${i}`, component: 'declare', label: `mod${i}` }),
        node({ id: `mod${i}_m`, component: 'declare', label: 'm', parent: `mod${i}` }),
        node({ id: `mod${i}_i`, component: 'opaque.m', label: 'x', parent: `mod${i}` }),
      );
    }
    nodes.push(node({ id: 'top_i', component: 'opaque.m', label: 'x' }));
    const resolve = moduleResolver(nodes);
    for (let i = 0; i < 300; i++) {
      expect(resolve(nodes[3 * i + 3] as GraphNode)?.id).toBe(`mod${i}_m`);
    }
    expect(resolve(nodes[nodes.length - 1] as GraphNode)?.id).toBe('top_m');
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
