import { beforeEach, describe, expect, it } from 'vitest';
import { shallow } from 'zustand/shallow';
import {
  type ConnectingFrom,
  selectConnectionState,
  selectSelectedNode,
  useVisualStore,
} from './store';
import type { ComponentDef, SchemaPayload } from './types';

describe('visual store', () => {
  beforeEach(() => {
    useVisualStore.setState({
      doc: {
        kind: 'alloy-graph/v1',
        schema_version: 'alloy-v1.18.1',
        nodes: [],
        edges: [],
        bindings: [],
        viewport: { x: 0, y: 0, zoom: 1 },
        meta: { created_with: 'test' },
      },
      selected: [],
      diagnostics: [],
      schema: null,
      allowExperimental: false,
      connectingFrom: null,
    });
    useVisualStore.temporal.getState().clear();
  });
  it('mutates documents and tracks history', () => {
    const before = useVisualStore.temporal.getState().pastStates.length;
    useVisualStore.getState().addNode('x', { x: 1, y: 1 });
    expect(useVisualStore.getState().doc.nodes).toHaveLength(1);
    expect(useVisualStore.temporal.getState().pastStates.length).toBeGreaterThan(before);
  });

  it('setAllowExperimental re-gates an experimental graph (#114)', () => {
    // Minimal schema with one experimental component and a graph that uses it.
    const schema = {
      _meta: { schema_version: 'alloy-v1.18.1' },
      components: {
        'loki.secretfilter': {
          stability: 'experimental',
          doc: '',
          attributes: [],
          blocks: [],
          inputs: [],
          outputs: [],
          default_snippet: '',
        },
      },
    } as unknown as SchemaPayload;
    useVisualStore.setState({
      schema,
      doc: {
        kind: 'alloy-graph/v1',
        schema_version: 'alloy-v1.18.1',
        nodes: [
          {
            id: 'n',
            component: 'loki.secretfilter',
            label: 'sf',
            position: { x: 0, y: 0 },
            props: {},
            disabled: false,
            notes: '',
          },
        ],
        edges: [],
        bindings: [],
        viewport: { x: 0, y: 0, zoom: 1 },
        meta: { created_with: 'test' },
      },
    });
    const codes = () => useVisualStore.getState().diagnostics.map((d) => d.code);

    // Opting in clears the experimental gate; opting back out restores it.
    useVisualStore.getState().setAllowExperimental(true);
    expect(useVisualStore.getState().allowExperimental).toBe(true);
    expect(codes()).not.toContain('experimental_gated');

    useVisualStore.getState().setAllowExperimental(false);
    expect(codes()).toContain('experimental_gated');
  });

  it('setAllowExperimental is a no-op when the value is unchanged', () => {
    useVisualStore.setState({ allowExperimental: false, diagnostics: [] });
    useVisualStore.getState().setAllowExperimental(false);
    // Unchanged flag must not trigger a revalidate (which needs a schema).
    expect(useVisualStore.getState().diagnostics).toEqual([]);
  });
  it('updateNode drops explicitly-undefined patch values instead of planting them on the node', () => {
    // Regression: InspectorPanel.setProp passes `block_order: undefined` for
    // plain attributes; spreading that onto the node made the save path's
    // protobuf Struct conversion throw "google.protobuf.Value must have a
    // value", so any pipeline whose attribute was edited could never save.
    const store = useVisualStore;
    store.getState().addNode('discovery.kubernetes', { x: 0, y: 0 });
    const id = store.getState().doc.nodes[0].id;
    store.getState().updateNode(id, { props: { role: 'pod' }, block_order: undefined });
    const node = store.getState().doc.nodes[0];
    expect(node.props).toEqual({ role: 'pod' });
    expect(Object.hasOwn(node, 'block_order')).toBe(false);
    // The whole doc must survive a Struct-equivalent walk: no own key anywhere
    // may hold undefined (JSON.stringify would silently drop it; the protobuf
    // conversion throws on it).
    const scan = (v: unknown): void => {
      if (Array.isArray(v)) for (const x of v) scan(x);
      else if (v && typeof v === 'object')
        for (const val of Object.values(v)) {
          expect(val).not.toBeUndefined();
          scan(val);
        }
    };
    scan(store.getState().doc);
  });
  it('viewport changes do not add history', () => {
    const n = useVisualStore.temporal.getState().pastStates.length;
    useVisualStore.getState().updateViewport({ x: 3, y: 4, zoom: 2 });
    expect(useVisualStore.temporal.getState().pastStates.length).toBe(n);
  });
  it('paste is one history entry — undo removes all pasted nodes+edges atomically', () => {
    const store = useVisualStore;
    store.getState().addNode('prometheus.scrape', { x: 0, y: 0 });
    const baseNodeId = store.getState().doc.nodes[0].id;
    store.getState().pasteNodesAndEdges(
      [
        {
          id: 'n_pasted',
          component: 'prometheus.scrape',
          label: 'scrape_copy',
          position: { x: 60, y: 60 },
          props: {},
          disabled: false,
          notes: '',
        },
      ],
      [],
    );
    expect(store.getState().doc.nodes).toHaveLength(2);
    store.temporal.getState().undo();
    expect(store.getState().doc.nodes).toHaveLength(1);
    expect(store.getState().doc.nodes[0].id).toBe(baseNodeId);
  });
  it('100-entry history cap evicts the oldest entry', () => {
    for (let i = 0; i < 101; i++)
      useVisualStore.getState().addNode('prometheus.scrape', { x: i, y: 0 });
    expect(useVisualStore.temporal.getState().pastStates.length).toBeLessThanOrEqual(100);
  });
  it('undo/redo restores exact node ids', () => {
    useVisualStore.getState().addNode('prometheus.scrape', { x: 0, y: 0 });
    const id = useVisualStore.getState().doc.nodes[0].id;
    useVisualStore.getState().setLabel(id, 'renamed');
    useVisualStore.temporal.getState().undo();
    expect(useVisualStore.getState().doc.nodes[0].id).toBe(id);
    useVisualStore.temporal.getState().redo();
    expect(useVisualStore.getState().doc.nodes[0].label).toBe('renamed');
  });
  it('setConnectingFrom does not add undo history (mirrors updateViewport)', () => {
    const n = useVisualStore.temporal.getState().pastStates.length;
    useVisualStore
      .getState()
      .setConnectingFrom({ nodeId: 'a', handleId: 'x', handleType: 'source', wireType: 'targets' });
    expect(useVisualStore.temporal.getState().pastStates.length).toBe(n);
  });

  describe('removeSelected', () => {
    it('is a no-op (no history entry) when nothing is selected', () => {
      useVisualStore.getState().addNode('prometheus.scrape', { x: 0, y: 0 });
      const n = useVisualStore.temporal.getState().pastStates.length;
      useVisualStore.getState().removeSelected();
      expect(useVisualStore.getState().doc.nodes).toHaveLength(1);
      expect(useVisualStore.temporal.getState().pastStates.length).toBe(n);
    });

    it('deletes a selected node and cascades its edges in one undoable step', () => {
      const store = useVisualStore;
      store.getState().addNode('prometheus.scrape', { x: 0, y: 0 });
      store.getState().addNode('prometheus.remote_write', { x: 200, y: 0 });
      const [a, b] = store.getState().doc.nodes.map((n) => n.id);
      store.getState().addEdge({ node: a, port: 'out' }, { node: b, port: 'in' });
      expect(store.getState().doc.edges).toHaveLength(1);

      store.getState().setSelected([a]);
      store.getState().removeSelected();

      expect(store.getState().doc.nodes.map((n) => n.id)).toEqual([b]);
      expect(store.getState().doc.edges).toHaveLength(0);
      expect(store.getState().selected).toEqual([]);

      store.temporal.getState().undo();
      expect(store.getState().doc.nodes.map((n) => n.id)).toEqual([a, b]);
      expect(store.getState().doc.edges).toHaveLength(1);
    });

    it('deletes a selected edge and leaves both endpoint nodes intact', () => {
      const store = useVisualStore;
      store.getState().addNode('prometheus.scrape', { x: 0, y: 0 });
      store.getState().addNode('prometheus.remote_write', { x: 200, y: 0 });
      const [a, b] = store.getState().doc.nodes.map((n) => n.id);
      store.getState().addEdge({ node: a, port: 'out' }, { node: b, port: 'in' });
      const edgeId = store.getState().doc.edges[0].id;

      store.getState().setSelected([edgeId]);
      store.getState().removeSelected();

      expect(store.getState().doc.edges).toHaveLength(0);
      expect(store.getState().doc.nodes).toHaveLength(2);

      store.temporal.getState().undo();
      expect(store.getState().doc.edges).toHaveLength(1);
      expect(store.getState().doc.edges[0].id).toBe(edgeId);
    });

    it('deletes a multi-node selection in one undoable step', () => {
      const store = useVisualStore;
      store.getState().addNode('prometheus.scrape', { x: 0, y: 0 });
      store.getState().addNode('prometheus.remote_write', { x: 200, y: 0 });
      const [a, b] = store.getState().doc.nodes.map((n) => n.id);

      store.getState().setSelected([a, b]);
      const before = store.temporal.getState().pastStates.length;
      store.getState().removeSelected();

      expect(store.getState().doc.nodes).toHaveLength(0);
      // One history entry for the whole batch, not one per node.
      expect(store.temporal.getState().pastStates.length).toBe(before + 1);

      store.temporal.getState().undo();
      expect(
        store
          .getState()
          .doc.nodes.map((n) => n.id)
          .sort(),
      ).toEqual([a, b].sort());
    });
  });
});

describe('selectConnectionState (A3 narrow selector)', () => {
  // Only accepts prom.metrics on its one input — used below as a node that's
  // "unaffected" by drags of an incompatible wire type.
  const scalarSink: ComponentDef = {
    stability: 'ga',
    doc: '',
    attributes: [],
    blocks: [],
    inputs: [{ prop: 'receiver', type: 'prom.metrics' }],
    outputs: [],
    default_snippet: '',
  };

  it('is idle (dragActive: false) when no drag is in progress', () => {
    expect(selectConnectionState(null, 'n1', scalarSink).dragActive).toBe(false);
  });

  it('is idle for the node the drag started from', () => {
    const cf: ConnectingFrom = {
      nodeId: 'n1',
      handleId: 'receiver',
      handleType: 'target',
      wireType: 'prom.metrics',
    };
    expect(selectConnectionState(cf, 'n1', scalarSink).dragActive).toBe(false);
  });

  it('marks a compatible node as a valid target with its port id listed', () => {
    const cf: ConnectingFrom = {
      nodeId: 'other',
      handleId: 'metrics',
      handleType: 'source',
      wireType: 'prom.metrics',
    };
    const state = selectConnectionState(cf, 'n1', scalarSink);
    expect(state.dragActive).toBe(true);
    expect(state.isValidTarget).toBe(true);
    expect(state.isDimmed).toBe(false);
    expect(state.validPortIds).toEqual(['receiver']);
  });

  it('yields a shallow-stable result for an unaffected node across two different connectingFrom values', () => {
    // scalarSink only accepts prom.metrics — neither drag below is compatible,
    // so its computed connection state should come back identical (shallow-equal,
    // in fact reference-equal — see below) both times, regardless of which other
    // node's drag is in progress.
    const cfA: ConnectingFrom = {
      nodeId: 'other-1',
      handleId: 'logs',
      handleType: 'source',
      wireType: 'loki.logs',
    };
    const cfB: ConnectingFrom = {
      nodeId: 'other-2',
      handleId: 'traces',
      handleType: 'source',
      wireType: 'otel.traces',
    };
    const a = selectConnectionState(cfA, 'n1', scalarSink);
    const b = selectConnectionState(cfB, 'n1', scalarSink);
    expect(shallow(a, b)).toBe(true);
    expect(a).toBe(b); // same shared reference, not just shallow-equal — the strongest form of stable
    expect(a.validPortIds).toEqual([]);
    expect(a.isDimmed).toBe(true);
  });
});

describe('undo/redo keep diagnostics in step with the document', () => {
  // The mutations each end with `diagnostics: revalidate(...)`; zundo restores
  // `doc` alone, straight into the store, bypassing all of them. Measured in the
  // browser before this was fixed: delete a wired node (2 problems appear), undo
  // it, and the graph is whole again while the Problems drawer still says 2 —
  // with the toolbar, which re-renders server-side, saying "Valid" beside it.
  const schema = {
    _meta: { alloy_version: 'alloy-v1.18.1' },
    components: {
      'discovery.kubernetes': {
        category: 'sources',
        attributes: [],
        blocks: [],
        inputs: [],
        outputs: [{ export: 'targets', path: ['targets'], type: 'targets', role: 'produces' }],
      },
    },
  } as unknown as SchemaPayload;

  beforeEach(() => {
    useVisualStore.setState({
      doc: {
        kind: 'alloy-graph/v1',
        schema_version: 'alloy-v1.18.1',
        nodes: [],
        edges: [],
        bindings: [],
        viewport: { x: 0, y: 0, zoom: 1 },
        meta: { created_with: 'test' },
      },
      selected: [],
      diagnostics: [],
      schema,
      allowExperimental: false,
      connectingFrom: null,
    });
    useVisualStore.temporal.getState().clear();
  });

  it('recomputes diagnostics after undo, not just the document', () => {
    const s = useVisualStore.getState();
    s.addNode('discovery.kubernetes', { x: 0, y: 0 });
    const afterAdd = useVisualStore.getState().diagnostics.length;

    // Remove it: an empty graph has nothing to complain about.
    const id = useVisualStore.getState().doc.nodes[0].id;
    useVisualStore.getState().removeNode(id);
    expect(useVisualStore.getState().doc.nodes).toHaveLength(0);
    expect(useVisualStore.getState().diagnostics).toHaveLength(0);

    useVisualStore.getState().undo();

    expect(useVisualStore.getState().doc.nodes).toHaveLength(1);
    expect(
      useVisualStore.getState().diagnostics.length,
      'diagnostics must describe the restored graph, not the one left behind',
    ).toBe(afterAdd);
  });

  it('recomputes diagnostics after redo too', () => {
    useVisualStore.getState().addNode('discovery.kubernetes', { x: 0, y: 0 });
    const id = useVisualStore.getState().doc.nodes[0].id;
    useVisualStore.getState().removeNode(id);
    useVisualStore.getState().undo();
    expect(useVisualStore.getState().doc.nodes).toHaveLength(1);

    useVisualStore.getState().redo();

    expect(useVisualStore.getState().doc.nodes).toHaveLength(0);
    expect(
      useVisualStore.getState().diagnostics,
      'an emptied graph has no problems to report',
    ).toHaveLength(0);
  });
});

describe('addEdge scalar fan-in (W5-03)', () => {
  // Same hand-built-schema convention as scalarConflicts's own tests
  // (wireOrient.test.ts) — the shipped schema does not populate `cardinality`
  // on any real port yet.
  const schema = {
    _meta: { alloy_version: 'x' },
    components: {
      source: {
        stability: 'ga',
        attributes: [],
        blocks: [],
        inputs: [],
        outputs: [{ export: 'out', path: ['out'], type: 'x', role: 'produces' }],
      },
      sink: {
        stability: 'ga',
        attributes: [],
        blocks: [],
        inputs: [{ prop: 'in', path: ['in'], type: 'x', role: 'accepts', cardinality: 'scalar' }],
        outputs: [],
      },
    },
  } as unknown as SchemaPayload;

  beforeEach(() => {
    useVisualStore.setState({
      doc: {
        kind: 'alloy-graph/v1',
        schema_version: 'alloy-v1.18.1',
        nodes: [],
        edges: [],
        bindings: [],
        viewport: { x: 0, y: 0, zoom: 1 },
        meta: { created_with: 'test' },
      },
      selected: [],
      diagnostics: [],
      schema,
      allowExperimental: false,
      connectingFrom: null,
    });
    useVisualStore.temporal.getState().clear();
  });

  it('a second wire onto a scalar port replaces the first, in one undo step', () => {
    const store = useVisualStore;
    store.getState().addNode('source', { x: 0, y: 0 });
    store.getState().addNode('source', { x: 0, y: 100 });
    store.getState().addNode('sink', { x: 200, y: 50 });
    const [a, b, c] = store.getState().doc.nodes.map((n) => n.id);

    const first = store.getState().addEdge({ node: a, port: 'out' }, { node: c, port: 'in' });
    expect(first).toEqual({ added: true, replaced: [] });
    const afterFirst = store.temporal.getState().pastStates.length;

    const second = store.getState().addEdge({ node: b, port: 'out' }, { node: c, port: 'in' });
    expect(second.added).toBe(true);
    expect(second.replaced).toHaveLength(1);
    expect(second.replaced[0].from.node).toBe(a);

    expect(store.getState().doc.edges).toHaveLength(1);
    expect(store.getState().doc.edges[0].from.node).toBe(b);
    // The replace is ONE undo step, not two (the add and the removal happen
    // in the same `set` call).
    expect(store.temporal.getState().pastStates.length).toBe(afterFirst + 1);

    store.temporal.getState().undo();
    expect(store.getState().doc.edges).toHaveLength(1);
    expect(store.getState().doc.edges[0].from.node).toBe(a);
  });

  it('a duplicate or cycling edge is still refused (added: false, replaced: [])', () => {
    const store = useVisualStore;
    store.getState().addNode('source', { x: 0, y: 0 });
    store.getState().addNode('sink', { x: 200, y: 0 });
    const [a, c] = store.getState().doc.nodes.map((n) => n.id);
    store.getState().addEdge({ node: a, port: 'out' }, { node: c, port: 'in' });

    const dup = store.getState().addEdge({ node: a, port: 'out' }, { node: c, port: 'in' });
    expect(dup).toEqual({ added: false, replaced: [] });
    expect(store.getState().doc.edges).toHaveLength(1);
  });
});

describe('edge order (W5-08)', () => {
  // A plain list-cardinality accepts port — no scalar-replace involved, so
  // every addEdge call here actually adds.
  const schema = {
    _meta: { alloy_version: 'x' },
    components: {
      source: {
        stability: 'ga',
        attributes: [],
        blocks: [],
        inputs: [],
        outputs: [{ export: 'out', path: ['out'], type: 'x', role: 'produces' }],
      },
      list_sink: {
        stability: 'ga',
        attributes: [],
        blocks: [],
        inputs: [{ prop: 'in', path: ['in'], type: 'x', role: 'accepts', cardinality: 'list' }],
        outputs: [],
      },
    },
  } as unknown as SchemaPayload;

  beforeEach(() => {
    useVisualStore.setState({
      doc: {
        kind: 'alloy-graph/v1',
        schema_version: 'alloy-v1.18.1',
        nodes: [],
        edges: [],
        bindings: [],
        viewport: { x: 0, y: 0, zoom: 1 },
        meta: { created_with: 'test' },
      },
      selected: [],
      diagnostics: [],
      schema,
      allowExperimental: false,
      connectingFrom: null,
    });
    useVisualStore.temporal.getState().clear();
  });

  it('addEdge stamps order per accepts port, starting at 0 and counting up', () => {
    const store = useVisualStore;
    store.getState().addNode('source', { x: 0, y: 0 });
    store.getState().addNode('source', { x: 0, y: 100 });
    store.getState().addNode('list_sink', { x: 200, y: 50 });
    const [a, b, c] = store.getState().doc.nodes.map((n) => n.id);

    store.getState().addEdge({ node: a, port: 'out' }, { node: c, port: 'in' });
    store.getState().addEdge({ node: b, port: 'out' }, { node: c, port: 'in' });

    const edges = store.getState().doc.edges;
    expect(edges.map((e) => e.order)).toEqual([0, 1]);
  });

  it('order is scoped per (node, port) — a second port starts at 0 again', () => {
    const store = useVisualStore;
    store.getState().addNode('source', { x: 0, y: 0 });
    store.getState().addNode('list_sink', { x: 200, y: 0 });
    store.getState().addNode('list_sink', { x: 200, y: 100 });
    const [a, c1, c2] = store.getState().doc.nodes.map((n) => n.id);

    store.getState().addEdge({ node: a, port: 'out' }, { node: c1, port: 'in' });
    store.getState().addEdge({ node: a, port: 'out' }, { node: c2, port: 'in' });

    expect(store.getState().doc.edges.map((e) => e.order)).toEqual([0, 0]);
  });

  it('moveEdge swaps order with the previous/next sibling in one undo step', () => {
    const store = useVisualStore;
    store.getState().addNode('source', { x: 0, y: 0 });
    store.getState().addNode('source', { x: 0, y: 100 });
    store.getState().addNode('list_sink', { x: 200, y: 50 });
    const [a, b, c] = store.getState().doc.nodes.map((n) => n.id);
    store.getState().addEdge({ node: a, port: 'out' }, { node: c, port: 'in' });
    store.getState().addEdge({ node: b, port: 'out' }, { node: c, port: 'in' });
    const [e0, e1] = store.getState().doc.edges;
    expect([e0.order, e1.order]).toEqual([0, 1]);

    const before = store.temporal.getState().pastStates.length;
    store.getState().moveEdge(e1.id, 'up');

    const after = store.getState().doc.edges;
    expect(after.find((e) => e.id === e0.id)?.order).toBe(1);
    expect(after.find((e) => e.id === e1.id)?.order).toBe(0);
    expect(store.temporal.getState().pastStates.length).toBe(before + 1);

    store.temporal.getState().undo();
    const restored = store.getState().doc.edges;
    expect(restored.find((e) => e.id === e0.id)?.order).toBe(0);
    expect(restored.find((e) => e.id === e1.id)?.order).toBe(1);
  });

  it('moveEdge is a no-op at either boundary', () => {
    const store = useVisualStore;
    store.getState().addNode('source', { x: 0, y: 0 });
    store.getState().addNode('list_sink', { x: 200, y: 0 });
    const [a, c] = store.getState().doc.nodes.map((n) => n.id);
    store.getState().addEdge({ node: a, port: 'out' }, { node: c, port: 'in' });
    const [only] = store.getState().doc.edges;
    const before = store.temporal.getState().pastStates.length;

    store.getState().moveEdge(only.id, 'up');
    store.getState().moveEdge(only.id, 'down');

    expect(store.getState().doc.edges[0].order).toBe(0);
    expect(store.temporal.getState().pastStates.length).toBe(before);
  });
});

describe('setBinding / removeBinding (W5-01)', () => {
  // A minimal hand-built schema (store.test.ts's established pattern —
  // selectConnectionState's `scalarSink` above, and the undo/redo describe
  // block's own `schema` const) with a secret attribute nested inside a
  // repeatable block, so the nested-instance-path case is exercised the same
  // way a real remote_write endpoint's basic_auth.password is.
  const schema = {
    _meta: { alloy_version: 'alloy-v1.18.1' },
    components: {
      'test.sink': {
        category: 'destinations',
        attributes: [],
        blocks: [
          {
            name: 'endpoint',
            repeatable: true,
            attributes: [{ name: 'password', type: 'secret', required: false }],
          },
        ],
        inputs: [],
        outputs: [],
      },
    },
  } as unknown as SchemaPayload;

  beforeEach(() => {
    useVisualStore.setState({
      doc: {
        kind: 'alloy-graph/v1',
        schema_version: 'alloy-v1.18.1',
        nodes: [],
        edges: [],
        bindings: [],
        viewport: { x: 0, y: 0, zoom: 1 },
        meta: { created_with: 'test' },
      },
      selected: [],
      diagnostics: [],
      schema,
      allowExperimental: false,
      connectingFrom: null,
    });
    useVisualStore.temporal.getState().clear();
  });

  it('writes a $expr at a nested instance path and clears secret_by_value', () => {
    const store = useVisualStore;
    store.getState().addNode('test.sink', { x: 0, y: 0 });
    const id = store.getState().doc.nodes[0].id;
    store.getState().updateNode(id, { props: { endpoint: [{ password: 'a literal secret' }] } });
    expect(store.getState().diagnostics.map((d) => d.code)).toContain('secret_by_value');

    const before = store.temporal.getState().pastStates.length;
    store.getState().setBinding(id, ['endpoint', '0', 'password'], 'local.file.creds.content');

    const node = store.getState().doc.nodes[0];
    expect(node.props).toEqual({ endpoint: [{ password: { $expr: 'local.file.creds.content' } }] });
    expect(store.getState().diagnostics.map((d) => d.code)).not.toContain('secret_by_value');
    // One undo step for the whole write, like every other mutation.
    expect(store.temporal.getState().pastStates.length).toBe(before + 1);

    store.temporal.getState().undo();
    expect(store.getState().doc.nodes[0].props).toEqual({
      endpoint: [{ password: 'a literal secret' }],
    });
  });

  it('removeBinding clears the bound prop back to unset, leaving sibling attributes alone', () => {
    const store = useVisualStore;
    store.getState().addNode('test.sink', { x: 0, y: 0 });
    const id = store.getState().doc.nodes[0].id;
    store.getState().updateNode(id, {
      props: { endpoint: [{ url: 'http://x', password: { $expr: 'local.file.creds.content' } }] },
    });

    store.getState().removeBinding(id, ['endpoint', '0', 'password']);

    expect(store.getState().doc.nodes[0].props).toEqual({ endpoint: [{ url: 'http://x' }] });
  });
});

describe('selectSelectedNode (W5-10 narrow selector)', () => {
  it('returns the exactly-one selected node', () => {
    const store = useVisualStore;
    store.getState().addNode('discovery.kubernetes', { x: 0, y: 0 });
    const id = store.getState().doc.nodes[0].id;
    store.getState().setSelected([id]);
    expect(selectSelectedNode(store.getState())?.id).toBe(id);
  });

  it('returns undefined for zero or multiple selected nodes', () => {
    const store = useVisualStore;
    store.getState().addNode('discovery.kubernetes', { x: 0, y: 0 });
    store.getState().addNode('prometheus.scrape', { x: 100, y: 0 });
    const [a, b] = store.getState().doc.nodes.map((n) => n.id);

    store.getState().setSelected([]);
    expect(selectSelectedNode(store.getState())).toBeUndefined();

    store.getState().setSelected([a, b]);
    expect(selectSelectedNode(store.getState())).toBeUndefined();
  });

  it('returns the identical object reference after an unrelated node changes', () => {
    const store = useVisualStore;
    store.getState().addNode('discovery.kubernetes', { x: 0, y: 0 });
    store.getState().addNode('prometheus.scrape', { x: 100, y: 0 });
    const [a, b] = store.getState().doc.nodes.map((n) => n.id);
    store.getState().setSelected([a]);

    const before = selectSelectedNode(store.getState());
    store.getState().updateNode(b, { label: 'renamed' });
    const after = selectSelectedNode(store.getState());

    expect(after).toBe(before);
  });

  it('returns a NEW reference once the selected node itself changes', () => {
    const store = useVisualStore;
    store.getState().addNode('discovery.kubernetes', { x: 0, y: 0 });
    const id = store.getState().doc.nodes[0].id;
    store.getState().setSelected([id]);

    const before = selectSelectedNode(store.getState());
    store.getState().setLabel(id, 'renamed');
    const after = selectSelectedNode(store.getState());

    expect(after).not.toBe(before);
    expect(after?.label).toBe('renamed');
  });
});

describe('schema_version follows the served schema, never a literal', () => {
  // A fleet bump used to leave every NEW pipeline stamped with the previous
  // version (makeDefaultDoc hardcoded it), so each one immediately read as
  // "needs upgrading" against the schema it was authored on. Red run: put a
  // literal back into makeDefaultDoc's default and drop the stamping from
  // setSchema — the first two cases fail on the literal.
  const schemaAt = (alloyVersion: string): SchemaPayload =>
    ({
      _meta: { alloy_version: alloyVersion, components_total: 0 },
      components: {},
    }) as SchemaPayload;
  const emptyGraphAt = (schemaVersion: string) => ({
    kind: 'alloy-graph/v1' as const,
    schema_version: schemaVersion,
    nodes: [],
    edges: [],
    bindings: [],
    viewport: { x: 0, y: 0, zoom: 1 },
    meta: { created_with: 'test' },
  });

  it('a document with no version yet is stamped with the served version once the schema loads', () => {
    useVisualStore.getState().importGraph(emptyGraphAt(''));
    useVisualStore.getState().setSchema(schemaAt('1.19.2'));
    expect(useVisualStore.getState().doc.schema_version).toBe('alloy-v1.19.2');
  });

  it('a reset after the schema loaded starts on the served version, normalised', () => {
    for (const spelling of ['1.19.2', 'v1.19.2', 'alloy-v1.19.2']) {
      useVisualStore.getState().setSchema(schemaAt(spelling));
      useVisualStore.getState().resetDoc();
      expect(useVisualStore.getState().doc.schema_version).toBe('alloy-v1.19.2');
    }
  });

  it('a loaded graph keeps the version it was authored against, even when empty', () => {
    useVisualStore.getState().importGraph(emptyGraphAt('alloy-v1.12.0'));
    useVisualStore.getState().setSchema(schemaAt('1.19.2'));
    expect(useVisualStore.getState().doc.schema_version).toBe('alloy-v1.12.0');
  });
});
