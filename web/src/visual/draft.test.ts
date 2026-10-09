import 'fake-indexeddb/auto';
import { set } from 'idb-keyval';
import { beforeEach, describe, expect, it } from 'vitest';
import {
  clearDraft,
  loadDraft,
  loadDraftWithBase,
  saveDraft,
  shouldOfferRestore,
  subscribeDraftAutosave,
} from './draft';
import type { GraphDocument, GraphNode } from './types';

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

const document: GraphDocument = {
  kind: 'alloy-graph/v1',
  schema_version: 'x',
  nodes: [],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'test' },
};
describe('draft persistence', () => {
  beforeEach(async () => {
    await clearDraft('test');
  });
  it('saves and loads drafts', async () => {
    await saveDraft('test', document);
    expect(await loadDraft('test')).toEqual(document);
  });
  it('clears drafts', async () => {
    await saveDraft('test', document);
    await clearDraft('test');
    expect(await loadDraft('test')).toBeNull();
    expect(await loadDraftWithBase('test')).toBeNull();
  });
  it('keeps the server revision the draft was edited from (F1)', async () => {
    await saveDraft('test', document, 5);
    expect(await loadDraftWithBase('test')).toEqual({ doc: document, baseRevision: 5 });
  });
  it('reports no base for a draft saved before base revisions existed', async () => {
    await set('vb:draft:legacy', document);
    expect(await loadDraftWithBase('legacy')).toEqual({ doc: document, baseRevision: null });
    await clearDraft('legacy');
  });
});

function node(id: string): GraphNode {
  return {
    id,
    component: 'test.component',
    label: id,
    position: { x: 0, y: 0 },
    props: {},
    disabled: false,
    notes: '',
  };
}

describe('shouldOfferRestore', () => {
  it('is false when there is no draft', () => {
    expect(shouldOfferRestore(null, document)).toBe(false);
  });

  it('is false when the draft has no nodes or edges', () => {
    expect(shouldOfferRestore(document, { ...document, schema_version: 'y' })).toBe(false);
  });

  it('is false when the draft matches current except for viewport', () => {
    const draft = { ...document, nodes: [node('n1')], viewport: { x: 9, y: 9, zoom: 2 } };
    const current = { ...document, nodes: [node('n1')], viewport: { x: 0, y: 0, zoom: 1 } };
    expect(shouldOfferRestore(draft, current)).toBe(false);
  });

  it('is true when the draft has content the current doc lacks', () => {
    const draft = { ...document, nodes: [node('n1')] };
    expect(shouldOfferRestore(draft, document)).toBe(true);
  });
});

interface MockState {
  doc: GraphDocument;
  unrelated: number;
}
function makeMockStore(initial: MockState) {
  let state = initial;
  const listeners = new Set<(state: MockState, prevState: MockState) => void>();
  return {
    subscribe(listener: (state: MockState, prevState: MockState) => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    setState(patch: Partial<MockState>) {
      const prev = state;
      state = { ...state, ...patch };
      for (const l of listeners) l(state, prev);
    },
  };
}

describe('subscribeDraftAutosave', () => {
  beforeEach(async () => {
    await clearDraft('auto-test');
  });

  it('debounces: only the trailing doc, after the delay elapses with no further change, is saved', async () => {
    const docA = { ...document, nodes: [node('a')] };
    const docB = { ...document, nodes: [node('a'), node('b')] };
    const store = makeMockStore({ doc: document, unrelated: 0 });
    const unsubscribe = subscribeDraftAutosave(store, 'auto-test', 80);

    store.setState({ doc: docA });
    await sleep(40);
    expect(await loadDraft('auto-test')).toBeNull(); // not yet — still within the debounce window

    store.setState({ doc: docB }); // resets the debounce clock
    await sleep(40);
    expect(await loadDraft('auto-test')).toBeNull(); // docA never got its own save in

    await sleep(80);
    expect(await loadDraft('auto-test')).toEqual(docB);

    unsubscribe();
  });

  it('ignores a store notification whose doc reference did not change', async () => {
    const store = makeMockStore({ doc: document, unrelated: 0 });
    const unsubscribe = subscribeDraftAutosave(store, 'auto-test', 80);

    store.setState({ unrelated: 1 }); // doc reference unchanged
    await sleep(150);
    expect(await loadDraft('auto-test')).toBeNull();

    unsubscribe();
  });

  it('stops saving once unsubscribed, even mid-debounce', async () => {
    const docA = { ...document, nodes: [node('a')] };
    const store = makeMockStore({ doc: document, unrelated: 0 });
    const unsubscribe = subscribeDraftAutosave(store, 'auto-test', 80);

    store.setState({ doc: docA });
    unsubscribe();
    await sleep(150);
    expect(await loadDraft('auto-test')).toBeNull();
  });
});
