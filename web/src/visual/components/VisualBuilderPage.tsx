import { useParams } from '@tanstack/react-router';
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { toast } from 'sonner';
import { graphView } from '../../api/client';
import { clients, toApiError } from '../../api/transport';
import { useCanWrite, useOrg } from '../../hooks/useOrg';
import {
  clearDraft,
  loadDraft,
  saveDraft,
  shouldOfferRestore,
  subscribeDraftAutosave,
} from '../draft';
import { fetchSchema } from '../schemaAdapter';
import { useVisualStore } from '../store';
import type { GraphDocument } from '../types';
import { BottomDrawer } from './BottomDrawer';
import { CanvasPane } from './CanvasPane';
import { InspectorPanel } from './InspectorPanel';
import { Palette } from './Palette';
import { Toolbar } from './Toolbar';

/**
 * Type-guards an arbitrary decoded value as a well-formed alloy-graph/v1
 * GraphDocument (D3 in docs/reviews: wizard_state is the source of truth
 * when loading a source='visual' pipeline). Deliberately checks only the
 * shape needed to load safely — node/edge/binding identity and the
 * array-ness of the collections — not every field, so a document that
 * gained fields in a newer schema_version still loads.
 */
function isWellFormedGraphDocument(x: unknown): x is GraphDocument {
  if (!x || typeof x !== 'object') return false;
  const g = x as Record<string, unknown>;
  if (g.kind !== 'alloy-graph/v1') return false;
  if (!Array.isArray(g.nodes) || !Array.isArray(g.edges) || !Array.isArray(g.bindings))
    return false;
  return g.nodes.every(
    (n) =>
      n &&
      typeof n === 'object' &&
      typeof (n as Record<string, unknown>).id === 'string' &&
      typeof (n as Record<string, unknown>).component === 'string',
  );
}

if ((import.meta as ImportMeta & { env: { MODE: string } }).env.MODE !== 'production') {
  // @ts-expect-error test/dev helper
  window.__visualStore = useVisualStore;
}

export function VisualBuilderPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const pipelineId = id ?? 'new';
  const { orgId, orgs, setOrgId } = useOrg();
  const prevPipelineIdRef = useRef<string | null>(null);

  // #226: a viewer may pan, zoom and select, but not edit. The canvas and
  // inspector read this flag to stop offering edits; the store also refuses
  // every graph mutation while it holds. A layout effect, so the first paint
  // a viewer sees is already read-only.
  const canWrite = useCanWrite();
  useLayoutEffect(() => {
    useVisualStore.getState().setReadOnly(!canWrite);
  }, [canWrite]);

  // Use a ref to the store action to avoid including it in the effect deps
  // (Zustand action references are stable but the selector creates new refs each render)
  const setSchemaRef = useRef(useVisualStore.getState().setSchema);

  // Warn before the browser discards an unsaved graph. draft.ts's
  // subscribeDraftAutosave (wired below) means a refresh or crash loses at
  // most a few hundred ms of edits, not the whole session — this dialog is
  // still worth keeping as a second line of defense against an accidental
  // close.
  useEffect(() => {
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      // Only a graph that differs from what was loaded or last saved is worth
      // a prompt; an untouched pipeline on screen is not.
      if (!useVisualStore.getState().isDirty()) return;
      e.preventDefault();
      // Chrome requires returnValue to be set; the string is never displayed.
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, []);

  const [loadState, setLoadState] = useState<'idle' | 'loading' | 'error'>('idle');
  const [loadError, setLoadError] = useState<string | null>(null);
  const [draftToRestore, setDraftToRestore] = useState<GraphDocument | null>(null);
  // Whether the restore check below has run for this pipelineId. Autosave
  // waits for it (see the autosave effect).
  const [draftChecked, setDraftChecked] = useState(false);
  const [discardingDraft, setDiscardingDraft] = useState(false);

  useEffect(() => {
    fetchSchema()
      .then((schema) => setSchemaRef.current(schema))
      .catch(console.error);
  }, []); // run once on mount

  // #114: mirror the org's experimental-components setting into the store so the
  // palette offers experimental components only for a permitted org. The server
  // render gate is authoritative regardless; this is UX. Re-runs when the
  // resolved org changes (a multi-org pipeline load can switch orgId below).
  useEffect(() => {
    const org = orgs.find((o) => o.id === orgId);
    useVisualStore.getState().setAllowExperimental(org?.allowExperimentalComponents ?? false);
  }, [orgs, orgId]);

  useEffect(() => {
    if (pipelineId !== 'new') return;
    const stored = sessionStorage.getItem('vb:import-graph');
    if (!stored) return;
    try {
      const doc = JSON.parse(stored) as import('../types').GraphDocument;
      if (doc.kind === 'alloy-graph/v1' && Array.isArray(doc.nodes)) {
        useVisualStore.getState().importGraph(doc);
      }
    } catch {
      // Ignore invalid stored data.
    }
    sessionStorage.removeItem('vb:import-graph');
  }, [pipelineId]);

  useEffect(() => {
    if (prevPipelineIdRef.current !== null && prevPipelineIdRef.current !== pipelineId) {
      useVisualStore.getState().resetDoc();
      // A new pipelineId has its own draft to check before autosave resumes.
      setDraftChecked(false);
      setDraftToRestore(null);
    }
    prevPipelineIdRef.current = pipelineId;
  }, [pipelineId]);

  // Persist the in-memory graph to IndexedDB as it's edited (design §4.4) —
  // the builder otherwise holds the whole graph in memory with no
  // persistence, so a refresh, a closed tab or a crash loses however long
  // was spent wiring it up. Re-subscribed whenever pipelineId changes so a
  // save always lands under the right draft key.
  //
  // Not until the restore check has run, and not while a restore is pending:
  // until then the draft on disk is the only copy of an earlier session's
  // work. Mount-time doc changes (setSchema stamping the served version onto
  // a fresh doc, an existing pipeline's load) used to trigger a save of the
  // empty or freshly loaded doc over it 500ms later — losing the draft
  // whenever the user left without choosing, and hiding the banner entirely
  // whenever that save beat the check's read.
  useEffect(() => {
    if (!draftChecked || draftToRestore) return;
    return subscribeDraftAutosave(useVisualStore, pipelineId);
  }, [pipelineId, draftChecked, draftToRestore]);

  // #202: editing the graph while the restore banner is still unanswered
  // counts as choosing to continue from what's on screen. Autosave is paused
  // while the banner shows (above), so without this an edit made then was
  // never saved: the old draft stayed on disk, and "Restore draft" on the
  // next visit silently brought back the graph from before the edit. Rather
  // than block editing until the banner is answered, the first real edit
  // supersedes the old draft: it is saved at once (the change that triggered
  // this happened before autosave re-subscribes, so it would otherwise wait
  // for the next edit) and the banner is dismissed, which resumes autosave.
  //
  // Only a change to the graph's content counts — nodes, edges, bindings.
  // setSchema stamping the served version onto a fresh doc (which can land
  // after the check) and a pan/zoom are not the user continuing from here.
  // Restore's own importGraph also passes through here; that saves the
  // restored draft over itself, which is harmless.
  useEffect(() => {
    if (!draftToRestore) return;
    const unsubscribe = useVisualStore.subscribe((state, prev) => {
      const { doc } = state;
      if (
        doc.nodes === prev.doc.nodes &&
        doc.edges === prev.doc.edges &&
        doc.bindings === prev.doc.bindings
      ) {
        return;
      }
      unsubscribe();
      saveDraft(pipelineId, doc).catch(console.error);
      setDraftToRestore(null);
    });
    return unsubscribe;
  }, [pipelineId, draftToRestore]);

  // Offer to restore a draft left behind by an earlier session. Checked once
  // the doc this pipelineId should actually start from is in place: for
  // 'new' that's immediately (a fresh mount starts from the empty default
  // doc, and the sessionStorage import effect above — which takes priority
  // — has already run by the time this runs, since effects commit in
  // declaration order); for an existing pipeline it's deferred until the
  // fetch above finishes, so the draft is compared against what was
  // actually saved, not a transient empty doc.
  useEffect(() => {
    if (pipelineId !== 'new' && loadState !== 'idle') return;
    let cancelled = false;
    loadDraft(pipelineId)
      .then((draft) => {
        if (cancelled) return;
        const current = useVisualStore.getState().doc;
        if (shouldOfferRestore(draft, current)) setDraftToRestore(draft);
      })
      .catch(console.error)
      .finally(() => {
        if (!cancelled) setDraftChecked(true);
      });
    return () => {
      cancelled = true;
    };
  }, [pipelineId, loadState]);

  // Load an existing pipeline into the canvas (B4.4): seed name/matchers
  // from the Pipeline record, and the graph itself per D3 (docs/reviews:
  // graph-model-and-validation.md, F7) — `wizard_state` is the source of
  // truth for a source='visual' pipeline; re-parsing the generated text via
  // VisualService.GraphView is a fallback for when wizard_state is
  // missing/invalid, used only to avoid an empty canvas, and surfaced to
  // the user as a best-effort reconstruction rather than passed off as the
  // saved graph. GraphView's text re-parse remains the sole source for the
  // read-only "view as graph" of a pipeline that wasn't authored visually.
  //
  // NOTE: as of this change, GetPipeline's response (shepherd.mgmt.v1
  // Pipeline, pipeline.proto) still has no `wizard_state` field — it exists
  // only on Create/UpdatePipelineRequest. The read below degrades to the
  // GraphView fallback until that's added (Pipeline gains
  // `google.protobuf.Struct wizard_state`, populated in pipelineToProto /
  // GetPipeline, mirroring how CreatePipelineRequest/UpdatePipelineRequest
  // already carry it) — that proto+mgmtapi change is outside this task's
  // owned files (VisualBuilderPage.tsx, Toolbar.tsx) and must land
  // separately for the graph to actually round-trip end to end.
  // A pipeline that wasn't authored visually (source !== 'visual') or
  // whose contents couldn't be fully mapped back to a graph (opaque)
  // surfaces as a clear error rather than importing a broken graph.
  useEffect(() => {
    if (pipelineId === 'new' || !orgId) return;
    let cancelled = false;
    setLoadState('loading');
    setLoadError(null);
    (async () => {
      try {
        // A pipeline URL is shareable, so it may name a pipeline that lives in an org
        // other than the one currently selected. Try the active org first, then the
        // user's other orgs, and move the selection to whichever owns it — otherwise
        // the fetch 404s and the canvas just sits empty with no explanation.
        let effectiveOrgId = orgId;
        let pipeline = await clients.pipeline
          .getPipeline({ orgId, id: pipelineId })
          .catch(() => null);
        if (!pipeline) {
          for (const candidate of orgs) {
            if (candidate.id === orgId) continue;
            const found = await clients.pipeline
              .getPipeline({ orgId: candidate.id, id: pipelineId })
              .catch(() => null);
            if (found) {
              pipeline = found;
              effectiveOrgId = candidate.id;
              break;
            }
          }
        }
        if (cancelled) return;
        if (!pipeline) {
          setLoadError('Pipeline not found in any organisation you can access.');
          setLoadState('error');
          return;
        }
        if (effectiveOrgId !== orgId) {
          setOrgId(effectiveOrgId);
        }
        if (pipeline.source !== 'visual') {
          setLoadError(
            `"${pipeline.name}" wasn't created with the visual builder and can't be edited here. Open its graph view and use "Recreate as visual pipeline" instead.`,
          );
          setLoadState('error');
          return;
        }
        // The generated Pipeline message has no `wizard_state` field yet
        // (see the note above) — this cast is forward-compatible: it reads
        // the field the moment the proto/backend gain it, and safely finds
        // nothing (undefined) until then.
        const rawWizardState = (pipeline as unknown as { wizardState?: unknown }).wizardState;
        if (isWellFormedGraphDocument(rawWizardState)) {
          useVisualStore.getState().importGraph(rawWizardState);
          useVisualStore.getState().setPipelineMeta(pipeline.name, pipeline.matchers);
          setLoadState('idle');
          return;
        }

        // Fall back to the read-only text re-parse — say so, since it can
        // silently drop node ids, positions, notes, disabled flags,
        // bindings and non-scalar props relative to what was actually saved.
        const gv = await graphView(effectiveOrgId, pipelineId);
        if (cancelled) return;
        if (gv.opaque) {
          setLoadError(
            gv.warning || 'This pipeline could not be fully loaded into the visual builder.',
          );
          setLoadState('error');
          return;
        }
        toast.warning(
          "This pipeline's saved graph was missing or unreadable, so it was reconstructed from its generated config — positions, notes, disabled flags and some bindings may not match what was last saved.",
        );
        useVisualStore.getState().importGraph(gv.graph);
        useVisualStore.getState().setPipelineMeta(pipeline.name, pipeline.matchers);
        setLoadState('idle');
      } catch (e) {
        if (cancelled) return;
        setLoadError(toApiError(e).message || 'Failed to load pipeline');
        setLoadState('error');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [pipelineId, orgId, orgs, setOrgId]);

  if (pipelineId !== 'new' && loadState === 'loading') {
    return (
      <div
        className='flex items-center justify-center h-full text-sm text-muted'
        data-testid='visual-builder-loading'
      >
        Loading pipeline…
      </div>
    );
  }

  if (loadState === 'error') {
    return (
      <div
        className='flex items-center justify-center h-full p-8 text-sm text-red-500 text-center'
        data-testid='visual-builder-load-error'
      >
        {loadError}
      </div>
    );
  }

  return (
    <div className='flex flex-col h-full' data-testid='visual-builder'>
      {draftToRestore && (
        <div
          data-testid='draft-restore-banner'
          className='shrink-0 px-4 py-2 bg-yellow-50 border-b border-yellow-300 text-xs flex items-center gap-3'
        >
          <span>An unsaved draft was found for this pipeline — restore it or discard it?</span>
          <button
            data-testid='draft-restore'
            className='underline font-medium'
            onClick={() => {
              useVisualStore.getState().importGraph(draftToRestore);
              setDraftToRestore(null);
            }}
          >
            Restore draft
          </button>
          <button
            data-testid='draft-discard'
            className='underline font-medium disabled:opacity-60'
            disabled={discardingDraft}
            onClick={() => {
              // The banner comes down only once the delete has committed.
              // Dismissing first and deleting in the background looked
              // instantaneous but lied: a reload within the next few tens of
              // milliseconds (a user's reflex, or the drafts spec) aborts the
              // still-open IndexedDB transaction and the "discarded" draft
              // offers itself again on the next visit.
              setDiscardingDraft(true);
              clearDraft(pipelineId)
                .catch(console.error)
                .finally(() => {
                  setDiscardingDraft(false);
                  setDraftToRestore(null);
                });
            }}
          >
            Discard draft
          </button>
        </div>
      )}
      <Toolbar pipelineId={pipelineId} />
      <div className='flex flex-1 min-h-0 overflow-hidden'>
        <Palette />
        <CanvasPane />
        <InspectorPanel />
      </div>
      <BottomDrawer />
    </div>
  );
}
