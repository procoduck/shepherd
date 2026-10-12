import deepEqual from 'fast-deep-equal';
import {
  delMany as idbDelMany,
  get as idbGet,
  getMany as idbGetMany,
  keys as idbKeys,
  setMany as idbSetMany,
} from 'idb-keyval';
import type { GraphDocument } from './types';

const draftKey = (pipelineId: string) => `vb:draft:${pipelineId}`;
// The server revision the draft's edits started from (F1), kept beside the
// draft rather than inside it so a draft written before this existed still
// reads as one — with no base, which loadDraftWithBase reports as null.
const draftBaseKey = (pipelineId: string) => `vb:draft-base:${pipelineId}`;
// The toolbar's name and matchers (#319), kept beside the draft like its base
// so a draft written before this existed still reads — with no meta, null.
const draftMetaKey = (pipelineId: string) => `vb:draft-meta:${pipelineId}`;

/** The builder toolbar's pipeline name and matcher chips, saved with a draft. */
export interface DraftMeta {
  name: string;
  matchers: string[];
}

function isDraftMeta(x: unknown): x is DraftMeta {
  if (!x || typeof x !== 'object') return false;
  const m = x as Record<string, unknown>;
  return (
    typeof m.name === 'string' &&
    Array.isArray(m.matchers) &&
    m.matchers.every((v) => typeof v === 'string')
  );
}

/**
 * Saves `doc` as the pipeline's draft. `baseRevision` is the server revision
 * the graph was loaded at (the builder store's pipelineRevision): restoring
 * the draft later sends it as expected_revision, so a draft older than the
 * server copy cannot be saved over it unseen. `meta` is the toolbar's name and
 * matchers (#319), restored with the graph. Every key goes in one IndexedDB
 * transaction.
 */
export async function saveDraft(
  pipelineId: string,
  doc: GraphDocument,
  baseRevision = 0,
  meta?: DraftMeta,
): Promise<void> {
  const entries: [IDBValidKey, unknown][] = [
    [draftKey(pipelineId), doc],
    [draftBaseKey(pipelineId), baseRevision],
  ];
  if (meta) entries.push([draftMetaKey(pipelineId), { name: meta.name, matchers: meta.matchers }]);
  await idbSetMany(entries);
}
export async function loadDraft(pipelineId: string): Promise<GraphDocument | null> {
  return (await idbGet<GraphDocument>(draftKey(pipelineId))) ?? null;
}
/**
 * The draft, its base revision and its name/matchers. `baseRevision` is null
 * for a draft saved before F1; `meta` is null for one saved before #319.
 */
export async function loadDraftWithBase(pipelineId: string): Promise<{
  doc: GraphDocument;
  baseRevision: number | null;
  meta: DraftMeta | null;
} | null> {
  const [doc, base, meta] = await idbGetMany<unknown>([
    draftKey(pipelineId),
    draftBaseKey(pipelineId),
    draftMetaKey(pipelineId),
  ]);
  if (!doc) return null;
  return {
    doc: doc as GraphDocument,
    baseRevision: typeof base === 'number' ? base : null,
    meta: isDraftMeta(meta) ? meta : null,
  };
}
/**
 * Removes every builder draft this browser holds, for every pipeline (#324
 * review). Drafts are keyed by pipeline, not by account, so without this the
 * next person to sign in on the same browser was offered the last one's
 * unsaved work. Called on sign-out; other IndexedDB keys are left alone.
 */
export async function clearAllDrafts(): Promise<void> {
  const all = await idbKeys();
  const drafts = all.filter((k) => typeof k === 'string' && k.startsWith('vb:draft'));
  if (drafts.length > 0) await idbDelMany(drafts);
}
export async function clearDraft(pipelineId: string): Promise<void> {
  await idbDelMany([draftKey(pipelineId), draftBaseKey(pipelineId), draftMetaKey(pipelineId)]);
}

/**
 * True when `draft` is worth offering as a restore over `current` (design
 * §4.4): the draft actually has content, and it differs from what's already
 * loaded. `viewport` is excluded from the comparison — panning/zooming alone
 * must never make an otherwise-identical draft look "different" and pop the
 * banner.
 *
 * #319: with `draftMeta` (a draft saved with its name and matchers), a name or
 * matcher that differs from `currentMeta` is a difference too, and a name or
 * matcher counts as content — an unsaved rename alone is offered back. A
 * draft with no meta (saved before #319) is judged on its graph alone.
 */
export function shouldOfferRestore(
  draft: GraphDocument | null,
  current: GraphDocument,
  draftMeta: DraftMeta | null = null,
  currentMeta: DraftMeta = { name: '', matchers: [] },
): boolean {
  if (!draft) return false;
  const graphContent = draft.nodes.length > 0 || draft.edges.length > 0;
  const metaContent = !!draftMeta && (draftMeta.name !== '' || draftMeta.matchers.length > 0);
  if (!graphContent && !metaContent) return false;
  const { viewport: _draftViewport, ...draftRest } = draft;
  const { viewport: _currentViewport, ...currentRest } = current;
  if (!deepEqual(draftRest, currentRest)) return true;
  return (
    !!draftMeta &&
    (draftMeta.name !== currentMeta.name || !deepEqual(draftMeta.matchers, currentMeta.matchers))
  );
}

/** The draft meta of a store state, when the state carries a name (the
 *  builder store always does; a minimal test double may not). */
export function draftMetaOf(state: {
  pipelineName?: string;
  matchers?: string[];
}): DraftMeta | undefined {
  if (state.pipelineName === undefined) return undefined;
  return { name: state.pipelineName, matchers: state.matchers ?? [] };
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
export function subscribeDraftAutosave<
  S extends {
    doc: GraphDocument;
    pipelineRevision?: number;
    pipelineName?: string;
    matchers?: string[];
  },
>(
  store: { subscribe: (listener: (state: S, prevState: S) => void) => () => void },
  pipelineId: string,
  delayMs = 500,
): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const unsubscribe = store.subscribe((state, prevState) => {
    // The graph, the name and the matchers (#319) are what a draft keeps;
    // every other store field is irrelevant here.
    if (
      state.doc === prevState.doc &&
      state.pipelineName === prevState.pipelineName &&
      state.matchers === prevState.matchers
    ) {
      return;
    }
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      void saveDraft(pipelineId, state.doc, state.pipelineRevision ?? 0, draftMetaOf(state));
    }, delayMs);
  });
  return () => {
    if (timer !== null) clearTimeout(timer);
    unsubscribe();
  };
}
