import { beforeEach, describe, expect, it } from 'vitest';
import { shallow } from 'zustand/shallow';
import { type ConnectingFrom, selectConnectionState, useVisualStore } from './store';
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
