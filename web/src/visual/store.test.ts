import { beforeEach, describe, expect, it } from 'vitest';
import { useVisualStore } from './store';

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
    });
    useVisualStore.temporal.getState().clear();
  });
  it('mutates documents and tracks history', () => {
    const before = useVisualStore.temporal.getState().pastStates.length;
    useVisualStore.getState().addNode('x', { x: 1, y: 1 });
    expect(useVisualStore.getState().doc.nodes).toHaveLength(1);
    expect(useVisualStore.temporal.getState().pastStates.length).toBeGreaterThan(before);
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
});
