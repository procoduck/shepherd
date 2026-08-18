import { del as idbDel, get as idbGet, set as idbSet } from 'idb-keyval';
import type { GraphDocument } from './types';

const draftKey = (pipelineId: string) => `vb:draft:${pipelineId}`;
export async function saveDraft(pipelineId: string, doc: GraphDocument): Promise<void> {
  await idbSet(draftKey(pipelineId), doc);
}
export async function loadDraft(pipelineId: string): Promise<GraphDocument | null> {
  return (await idbGet<GraphDocument>(draftKey(pipelineId))) ?? null;
}
export async function clearDraft(pipelineId: string): Promise<void> {
  await idbDel(draftKey(pipelineId));
}
