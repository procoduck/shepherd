import 'fake-indexeddb/auto';
import { get, set } from 'idb-keyval';
import { beforeEach, describe, expect, it } from 'vitest';
import {
  clearAllDrafts,
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
    expect(await loadDraftWithBase('test')).toEqual({ doc: document, baseRevision: 5, meta: null });
  });
  // #319: the toolbar's name and matchers travel with the draft.
  it('keeps the name and matchers, and clears them with the draft', async () => {
    const meta = { name: 'checkout', matchers: ['env="prod"'] };
    await saveDraft('test', document, 3, meta);
    expect(await loadDraftWithBase('test')).toEqual({ doc: document, baseRevision: 3, meta });
    await clearDraft('test');
    await saveDraft('test', document, 3);
    expect((await loadDraftWithBase('test'))?.meta).toBeNull();
  });
  it('reports no base for a draft saved before base revisions existed', async () => {
    await set('vb:draft:legacy', document);
    expect(await loadDraftWithBase('legacy')).toEqual({
      doc: document,
      baseRevision: null,
      meta: null,
    });
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
  pipelineName?: string;
  matchers?: string[];
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

describe('shouldOfferRestore with name and matchers (#319)', () => {
  const graph = { ...document, nodes: [node('a')] };
  const loaded = { name: 'saved', matchers: ['env="prod"'] };
  it('offers a draft whose graph matches but whose name differs', () => {
    expect(shouldOfferRestore(graph, graph, { ...loaded, name: 'renamed' }, loaded)).toBe(true);
  });
  it('offers a draft whose graph matches but whose matchers differ', () => {
    const draftMeta = { ...loaded, matchers: ['env="prod"', 'team="a"'] };
    expect(shouldOfferRestore(graph, graph, draftMeta, loaded)).toBe(true);
  });
  it('does not offer a draft identical in graph, name and matchers', () => {
    expect(shouldOfferRestore(graph, graph, { ...loaded }, loaded)).toBe(false);
  });
  it('offers an empty-graph draft that carries a name the current one lacks', () => {
    expect(shouldOfferRestore(document, document, { name: 'n', matchers: [] })).toBe(true);
  });
  it('does not offer an empty draft with an empty name and no matchers', () => {
    expect(shouldOfferRestore(document, document, { name: '', matchers: [] })).toBe(false);
  });
  it('judges a draft saved before #319 (no meta) on its graph alone', () => {
    expect(shouldOfferRestore(graph, graph, null, { name: 'other', matchers: [] })).toBe(false);
  });
});

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

  it('saves on a name or matcher change alone, with the name and matchers (#319)', async () => {
    const store = makeMockStore({
      doc: document,
      unrelated: 0,
      pipelineName: 'a',
      matchers: [] as string[],
    });
    const unsubscribe = subscribeDraftAutosave(store, 'auto-test', 40);

    store.setState({ pipelineName: 'b' });
    await sleep(100);
    expect((await loadDraftWithBase('auto-test'))?.meta).toEqual({ name: 'b', matchers: [] });

    store.setState({ matchers: ['env="prod"'] });
    await sleep(100);
    expect((await loadDraftWithBase('auto-test'))?.meta).toEqual({
      name: 'b',
      matchers: ['env="prod"'],
    });

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

// #324 review: drafts live in the browser's IndexedDB, not per account, so the
// next person to sign in on the same browser was offered the last one's
// unsaved work. Sign-out clears every builder draft and nothing else.
describe('clearAllDrafts', () => {
  it('removes every draft key, for every pipeline, and leaves other keys alone', async () => {
    await saveDraft('a', document, 2, { name: 'a', matchers: [] });
    await saveDraft('new', document);
    await set('unrelated', 1);
    await clearAllDrafts();
    expect(await loadDraftWithBase('a')).toBeNull();
    expect(await loadDraftWithBase('new')).toBeNull();
    expect(await get('vb:draft-meta:a')).toBeUndefined();
    expect(await get('vb:draft-base:new')).toBeUndefined();
    expect(await get('unrelated')).toBe(1);
  });
});
