import { useParams } from '@tanstack/react-router';
import { useEffect, useRef, useState } from 'react';
import { toast } from 'sonner';
import { graphView } from '../../api/client';
import { clients, toApiError } from '../../api/transport';
import { useOrg } from '../../hooks/useOrg';
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

  // Use a ref to the store action to avoid including it in the effect deps
  // (Zustand action references are stable but the selector creates new refs each render)
  const setSchemaRef = useRef(useVisualStore.getState().setSchema);

  const [loadState, setLoadState] = useState<'idle' | 'loading' | 'error'>('idle');
  const [loadError, setLoadError] = useState<string | null>(null);

  useEffect(() => {
    fetchSchema()
      .then((schema) => setSchemaRef.current(schema))
      .catch(console.error);
  }, []); // run once on mount

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
    }
    prevPipelineIdRef.current = pipelineId;
  }, [pipelineId]);

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
