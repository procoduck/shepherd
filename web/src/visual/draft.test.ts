import 'fake-indexeddb/auto';
import { beforeEach, describe, expect, it } from 'vitest';
import { clearDraft, loadDraft, saveDraft } from './draft';
import type { GraphDocument } from './types';

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
  });
});
