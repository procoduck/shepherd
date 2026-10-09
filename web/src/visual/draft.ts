import deepEqual from 'fast-deep-equal';
import {
  delMany as idbDelMany,
  get as idbGet,
  getMany as idbGetMany,
  setMany as idbSetMany,
} from 'idb-keyval';
import type { GraphDocument } from './types';

const draftKey = (pipelineId: string) => `vb:draft:${pipelineId}`;
// The server revision the draft's edits started from (F1), kept beside the
// draft rather than inside it so a draft written before this existed still
// reads as one — with no base, which loadDraftWithBase reports as null.
const draftBaseKey = (pipelineId: string) => `vb:draft-base:${pipelineId}`;

/**
 * Saves `doc` as the pipeline's draft. `baseRevision` is the server revision
 * the graph was loaded at (the builder store's pipelineRevision): restoring
 * the draft later sends it as expected_revision, so a draft older than the
 * server copy cannot be saved over it unseen. Both keys go in one IndexedDB
 * transaction.
 */
export async function saveDraft(
  pipelineId: string,
  doc: GraphDocument,
  baseRevision = 0,
): Promise<void> {
  await idbSetMany([
    [draftKey(pipelineId), doc],
    [draftBaseKey(pipelineId), baseRevision],
  ]);
}
export async function loadDraft(pipelineId: string): Promise<GraphDocument | null> {
  return (await idbGet<GraphDocument>(draftKey(pipelineId))) ?? null;
}
/** The draft and its base revision; `baseRevision` is null for a draft saved before F1. */
export async function loadDraftWithBase(
  pipelineId: string,
): Promise<{ doc: GraphDocument; baseRevision: number | null } | null> {
  const [doc, base] = await idbGetMany<unknown>([draftKey(pipelineId), draftBaseKey(pipelineId)]);
  if (!doc) return null;
  return {
    doc: doc as GraphDocument,
    baseRevision: typeof base === 'number' ? base : null,
  };
}
export async function clearDraft(pipelineId: string): Promise<void> {
  await idbDelMany([draftKey(pipelineId), draftBaseKey(pipelineId)]);
}

/**
 * True when `draft` is worth offering as a restore over `current` (design
 * §4.4): the draft actually has content, and it differs from what's already
 * loaded. `viewport` is excluded from the comparison — panning/zooming alone
 * must never make an otherwise-identical draft look "different" and pop the
 * banner.
 */
export function shouldOfferRestore(draft: GraphDocument | null, current: GraphDocument): boolean {
  if (!draft) return false;
  if (draft.nodes.length === 0 && draft.edges.length === 0) return false;
  const { viewport: _draftViewport, ...draftRest } = draft;
  const { viewport: _currentViewport, ...currentRest } = current;
  return !deepEqual(draftRest, currentRest);
}

/**
 * Debounced IndexedDB autosave (design §4.4): the builder holds the entire
 * graph in memory with no persistence, so a crash, a closed tab or an
 * accidental reload silently loses however long was spent wiring it up to
 * that point. Subscribing this to the visual store means the last `delayMs`
 * of edits are the most that's ever at risk.
 *
 * Generic over the store's state type (rather than importing VisualStore
 * from ./store) so a minimal `{ subscribe }` test double can drive it
 * without constructing a full store.
 */
export function subscribeDraftAutosave<S extends { doc: GraphDocument; pipelineRevision?: number }>(
  store: { subscribe: (listener: (state: S, prevState: S) => void) => () => void },
  pipelineId: string,
  delayMs = 500,
): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const unsubscribe = store.subscribe((state, prevState) => {
    if (state.doc === prevState.doc) return; // every other store field is irrelevant here
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      void saveDraft(pipelineId, state.doc, state.pipelineRevision ?? 0);
    }, delayMs);
  });
  return () => {
    if (timer !== null) clearTimeout(timer);
    unsubscribe();
  };
}
