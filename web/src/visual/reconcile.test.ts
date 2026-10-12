import { describe, expect, it } from 'vitest';
import { schemaFixture } from '../../tests/fixtures/schema-fixture';
import {
  type EdgeInputs,
  EMPTY_DIAGNOSTICS,
  getSourceReachableEdges,
  type NodeInputs,
  reconcileEdges,
  reconcileNodes,
} from './reconcile';
import type { GraphEdge, GraphNode, SchemaPayload } from './types';

const schema: SchemaPayload = {
  _meta: { alloy_version: 'v', components_total: 2 },
  components: {
    'test.source': {
      stability: 'ga',
      doc: '',
      attributes: [],
      blocks: [],
      inputs: [],
      outputs: [{ type: 'targets', export: 'output' }],
      default_snippet: '',
      category: 'sources',
    },
    'test.sink': {
      stability: 'ga',
      doc: '',
      attributes: [],
      blocks: [],
      inputs: [{ type: 'targets', prop: 'input' }],
      outputs: [],
      default_snippet: '',
    },
  },
  wire_types: { targets: { color: '#123456', label: 'Targets' } },
};

function node(id: string, component = 'test.sink', overrides: Partial<GraphNode> = {}): GraphNode {
  return {
    id,
    component,
    label: id,
    position: { x: 0, y: 0 },
    props: {},
    disabled: false,
    notes: '',
    ...overrides,
  };
}

function edge(id: string, from: string, to: string): GraphEdge {
  return { id, from: { node: from, port: 'output' }, to: { node: to, port: 'input' } };
}

describe('reconcileNodes', () => {
  it('reuses the exact same object reference across a reconcile that changed nothing', () => {
    const n1 = node('n1');
    const inputs = new Map<string, NodeInputs>();
    const first = reconcileNodes([], [n1], schema, new Map(), new Set(), inputs, null);
    const second = reconcileNodes(first, [n1], schema, new Map(), new Set(), inputs, null);
    expect(second[0]).toBe(first[0]);
    expect(second).toBe(first); // whole array bails out too
  });

  it('rebuilds the projected node when the underlying GraphNode reference changes, even with identical content', () => {
    // This is the reference-identity invariant CanvasPane's controlled-mode
    // contract depends on: the store only ever hands us a new node object when
    // something about that node actually changed, so an equal-but-different
    // reference must be treated as "rebuild", never silently reused via a
    // content (deep-equal) comparison — reusing here would let RF keep stale
    // cached handle bounds for a node that DID change.
    const n1 = node('n1');
    const n1Clone: GraphNode = JSON.parse(JSON.stringify(n1));
    const inputs = new Map<string, NodeInputs>();
    const first = reconcileNodes([], [n1], schema, new Map(), new Set(), inputs, null);
    const second = reconcileNodes(first, [n1Clone], schema, new Map(), new Set(), inputs, null);
    expect(second[0]).not.toBe(first[0]);
  });

  it('preserves node identity across a pure reorder, but the array itself is new', () => {
    const n1 = node('n1');
    const n2 = node('n2');
    const inputs = new Map<string, NodeInputs>();
    const first = reconcileNodes([], [n1, n2], schema, new Map(), new Set(), inputs, null);
    const second = reconcileNodes(first, [n2, n1], schema, new Map(), new Set(), inputs, null);
    expect(second).not.toBe(first);
    expect(second[0]).toBe(first[1]);
    expect(second[1]).toBe(first[0]);
  });

  it('carries EMPTY_DIAGNOSTICS as the shared default so unrelated nodes never invalidate each other', () => {
    const n1 = node('n1');
    const inputs = new Map<string, NodeInputs>();
    const first = reconcileNodes([], [n1], schema, new Map(), new Set(), inputs, null);
    expect(first[0].data.diagnostics).toBe(EMPTY_DIAGNOSTICS);
  });
});

describe('reconcileEdges', () => {
  it('reuses the exact same edge object when nothing changed', () => {
    const n1 = node('n1', 'test.source');
    const n2 = node('n2', 'test.sink');
    const e1 = edge('e1', 'n1', 'n2');
    const inputs = new Map<string, EdgeInputs>();
    const first = reconcileEdges([], [e1], [n1, n2], schema, false, new Set(), inputs, 'dark');
    const second = reconcileEdges(first, [e1], [n1, n2], schema, false, new Set(), inputs, 'dark');
    expect(second[0]).toBe(first[0]);
    expect(second).toBe(first);
  });

  it('rebuilds when the edge reference changes with identical content', () => {
    const n1 = node('n1', 'test.source');
    const n2 = node('n2', 'test.sink');
    const e1 = edge('e1', 'n1', 'n2');
    const e1Clone: GraphEdge = JSON.parse(JSON.stringify(e1));
    const inputs = new Map<string, EdgeInputs>();
    const first = reconcileEdges([], [e1], [n1, n2], schema, false, new Set(), inputs, 'dark');
    const second = reconcileEdges(
      first,
      [e1Clone],
      [n1, n2],
      schema,
      false,
      new Set(),
      inputs,
      'dark',
    );
    expect(second[0]).not.toBe(first[0]);
  });

  it('colors and labels a reachable edge only while flow check is active', () => {
    const n1 = node('n1', 'test.source');
    const n2 = node('n2', 'test.sink');
    const e1 = edge('e1', 'n1', 'n2');
    const inputs = new Map<string, EdgeInputs>();
    const off = reconcileEdges([], [e1], [n1, n2], schema, false, new Set(), inputs, 'dark');
    expect(off[0].animated).toBe(false);
    expect(off[0].style).toEqual({ stroke: '#123456' });

    const inputs2 = new Map<string, EdgeInputs>();
    const on = reconcileEdges([], [e1], [n1, n2], schema, true, new Set(), inputs2, 'dark');
    expect(on[0].animated).toBe(true);
    expect(on[0].label).toBeTruthy();
  });
});

describe('getSourceReachableEdges', () => {
  it('walks forward from an enabled sources-category node and halts at a disabled node', () => {
    const n1 = node('n1', 'test.source');
    const n2 = node('n2', 'test.sink', { disabled: true });
    const n3 = node('n3', 'test.sink');
    const e1 = edge('e1', 'n1', 'n2');
    const e2 = edge('e2', 'n2', 'n3');
    const reachable = getSourceReachableEdges([n1, n2, n3], [e1, e2], schema);
    expect(reachable.has('e1')).toBe(false); // target n2 is disabled
    expect(reachable.has('e2')).toBe(false); // never reached, n2 halts traversal
  });

  it('marks an edge reachable when both ends are enabled and it descends from a source', () => {
    const n1 = node('n1', 'test.source');
    const n2 = node('n2', 'test.sink');
    const e1 = edge('e1', 'n1', 'n2');
    const reachable = getSourceReachableEdges([n1, n2], [e1], schema);
    expect(reachable.has('e1')).toBe(true);
  });
});

// #310: React Flow's default edge name is "Edge from <rf source id> to <rf
// target id>" — internal node ids, and for a receiver-kind wire (whose React
// Flow endpoints are swapped back to export→argument, wireOrient.ts) the
// reverse of the dataflow. The name is the nodes' labels, in dataflow order.
describe('reconcileEdges accessible names', () => {
  it('names a data-kind wire by its node labels, source first', () => {
    const n1 = node('n1', 'test.source', { label: 'pods' });
    const n2 = node('n2', 'test.sink', { label: 'relabel' });
    const out = reconcileEdges(
      [],
      [edge('e1', 'n1', 'n2')],
      [n1, n2],
      schema,
      false,
      new Set(),
      new Map(),
      'dark',
    );
    expect(out[0].ariaLabel).toBe('Edge from pods to relabel');
  });

  it('names a receiver-kind wire in dataflow order although React Flow draws it reversed', () => {
    const scrape = node('n3', 'prometheus.scrape', { label: 'app' });
    const write = node('n2', 'prometheus.remote_write', { label: 'sink' });
    const wire: GraphEdge = {
      id: 'e1',
      from: { node: 'n3', port: 'forward_to' },
      to: { node: 'n2', port: 'receiver' },
    };
    const out = reconcileEdges(
      [],
      [wire],
      [scrape, write],
      schemaFixture,
      false,
      new Set(),
      new Map(),
      'dark',
    );
    // React Flow's own endpoints run export→argument (remote_write → scrape)…
    expect(out[0].source).toBe('n2');
    expect(out[0].target).toBe('n3');
    // …but the name follows the data.
    expect(out[0].ariaLabel).toBe('Edge from app to sink');
  });

  it('renames the wire when a node is relabelled', () => {
    const n1 = node('n1', 'test.source', { label: 'pods' });
    const n2 = node('n2', 'test.sink', { label: 'relabel' });
    const e1 = edge('e1', 'n1', 'n2');
    const inputs = new Map<string, EdgeInputs>();
    const first = reconcileEdges([], [e1], [n1, n2], schema, false, new Set(), inputs, 'dark');
    const renamed = { ...n2, label: 'keep-prod' };
    const second = reconcileEdges(
      first,
      [e1],
      [n1, renamed],
      schema,
      false,
      new Set(),
      inputs,
      'dark',
    );
    expect(second[0].ariaLabel).toBe('Edge from pods to keep-prod');
  });
});
