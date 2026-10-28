import { describe, expect, it } from 'vitest';
import { shippedSchema } from '../../tests/fixtures/schema-fixture';
import {
  checkConnection,
  edgeAriaLabel,
  edgesOnHandle,
  nodeAriaLabel,
  portAriaLabel,
  readingOrder,
  spatialNeighbor,
} from './canvasA11y';
import type { GraphDocument, GraphNode } from './types';
import { rfEndpointsForEdge } from './wireOrient';

const node = (id: string, component: string, label: string, x: number, y: number): GraphNode => ({
  id,
  component,
  label,
  position: { x, y },
  props: {},
  disabled: false,
  notes: '',
});

describe('screen-reader labels', () => {
  it('names a node by component, label and port counts', () => {
    expect(
      nodeAriaLabel({ component: 'prometheus.scrape', label: 'x', disabled: false }, 2, 1),
    ).toBe('prometheus.scrape "x", 2 inputs, 1 output');
    expect(
      nodeAriaLabel({ component: 'loki.write', label: 'l', disabled: true }, 1, 0, {
        errors: 2,
        warnings: 1,
      }),
    ).toBe('loki.write "l", 1 input, 0 outputs, disabled, 2 errors, 1 warning');
  });

  it('names a port by name, direction, type and wires', () => {
    expect(
      portAriaLabel({ label: 'forward_to', role: 'produces', wireType: 'prom.metrics' }, 1),
    ).toBe('forward_to, output, prom.metrics, 1 wire');
    expect(portAriaLabel({ label: 'targets', role: 'accepts', wireType: 'targets' }, 0)).toBe(
      'targets, input, targets, 0 wires',
    );
  });

  it('names a wire from its producing end to its accepting end', () => {
    const nodes = [
      node('a', 'prometheus.scrape', 'app', 0, 0),
      node('b', 'prometheus.remote_write', 'sink', 300, 0),
    ];
    expect(
      edgeAriaLabel(
        { id: 'e', from: { node: 'a', port: 'forward_to' }, to: { node: 'b', port: 'receiver' } },
        nodes,
      ),
    ).toBe(
      'Wire from prometheus.scrape "app" forward_to to prometheus.remote_write "sink" receiver',
    );
  });
});

describe('node navigation', () => {
  const placed = [
    { id: 'src', position: { x: 0, y: 100 } },
    { id: 'mid', position: { x: 300, y: 100 } },
    { id: 'high', position: { x: 320, y: -200 } },
    { id: 'low', position: { x: 300, y: 300 } },
    { id: 'sink', position: { x: 600, y: 100 } },
  ];

  it('reads left to right, then top to bottom', () => {
    expect(readingOrder(placed).map((n) => n.id)).toEqual(['src', 'mid', 'low', 'high', 'sink']);
  });

  it('moves to the nearest node on the axis first', () => {
    expect(spatialNeighbor(placed, 'src', 'right')).toBe('mid');
    expect(spatialNeighbor(placed, 'mid', 'right')).toBe('sink');
    expect(spatialNeighbor(placed, 'mid', 'down')).toBe('low');
    expect(spatialNeighbor(placed, 'mid', 'up')).toBe('high');
    expect(spatialNeighbor(placed, 'mid', 'left')).toBe('src');
    expect(spatialNeighbor(placed, 'src', 'left')).toBeNull();
    expect(spatialNeighbor(placed, 'missing', 'left')).toBeNull();
  });
});

describe('keyboard wiring', () => {
  const doc: Pick<GraphDocument, 'nodes' | 'edges'> = {
    nodes: [
      node('k', 'discovery.kubernetes', 'k8s', 0, 0),
      node('s', 'prometheus.scrape', 'app', 300, 0),
      node('w', 'prometheus.remote_write', 'sink', 600, 0),
    ],
    edges: [{ id: 'e1', from: { node: 'k', port: 'targets' }, to: { node: 's', port: 'targets' } }],
  };
  const schema = shippedSchema;

  it('orients a compatible pair as the mouse path does', () => {
    const r = checkConnection(
      schema,
      doc,
      { nodeId: 's', handleId: 'forward_to', rfType: 'target' },
      { nodeId: 'w', handleId: 'receiver', rfType: 'source' },
    );
    expect(r).toEqual({
      ok: true,
      from: { node: 's', port: 'forward_to' },
      to: { node: 'w', port: 'receiver' },
    });
  });

  it('refuses an incompatible pair, a duplicate, a self-wire and a cycle', () => {
    expect(
      checkConnection(
        schema,
        doc,
        { nodeId: 'k', handleId: 'targets', rfType: 'source' },
        { nodeId: 'w', handleId: 'receiver', rfType: 'source' },
      ),
    ).toEqual({ ok: false, reason: 'invalid' });
    expect(
      checkConnection(
        schema,
        doc,
        { nodeId: 'k', handleId: 'targets', rfType: 'source' },
        { nodeId: 's', handleId: 'targets', rfType: 'target' },
      ),
    ).toEqual({ ok: false, reason: 'duplicate' });
    expect(
      checkConnection(
        schema,
        doc,
        { nodeId: 's', handleId: 'forward_to', rfType: 'target' },
        { nodeId: 's', handleId: 'targets', rfType: 'source' },
      ),
    ).toEqual({ ok: false, reason: 'invalid' });
    const relabel = {
      nodes: [...doc.nodes, node('r', 'discovery.relabel', 'r', 0, 300)],
      edges: [
        { id: 'a', from: { node: 'k', port: 'targets' }, to: { node: 'r', port: 'targets' } },
        { id: 'b', from: { node: 'r', port: 'output' }, to: { node: 'k', port: 'x' } },
      ],
    };
    expect(
      checkConnection(
        schema,
        relabel,
        { nodeId: 'r', handleId: 'output', rfType: 'source' },
        { nodeId: 'k', handleId: 'targets', rfType: 'target' },
      ).ok,
    ).toBe(false);
  });

  it("lists a handle's wires by React Flow's endpoints", () => {
    const rf = (e: GraphDocument['edges'][number]) => rfEndpointsForEdge(schema, doc, e);
    expect(
      edgesOnHandle(doc.edges, rf, { nodeId: 's', handleId: 'targets', rfType: 'target' }).map(
        (e) => e.id,
      ),
    ).toEqual(['e1']);
    expect(
      edgesOnHandle(doc.edges, rf, { nodeId: 's', handleId: 'forward_to', rfType: 'target' }),
    ).toEqual([]);
  });
});
