import { useParams } from '@tanstack/react-router';
import { useEffect, useRef, useState } from 'react';
import { graphView } from '../../api/client';
import { clients, toApiError } from '../../api/transport';
import { useOrgId } from '../../hooks/useOrg';
import { fetchSchema } from '../schemaAdapter';
import { useVisualStore } from '../store';
import { BottomDrawer } from './BottomDrawer';
import { CanvasPane } from './CanvasPane';
import { InspectorPanel } from './InspectorPanel';
import { Palette } from './Palette';
import { Toolbar } from './Toolbar';

if ((import.meta as ImportMeta & { env: { MODE: string } }).env.MODE !== 'production') {
  // @ts-expect-error test/dev helper
  window.__visualStore = useVisualStore;
}

export function VisualBuilderPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const pipelineId = id ?? 'new';
  const orgId = useOrgId();
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
  // from the Pipeline record and the graph from VisualService.GraphView.
  //
  // NOTE: this intentionally does not read `wizard_state` — the generated
  // Pipeline message (shepherd.mgmt.v1, see pipeline.proto) only carries
  // wizard_state on Create/Update requests, never on GetPipeline's
  // response, so there is no read path for it today. GraphView's
  // best-effort re-parse of the pipeline's rendered `contents` is the only
  // graph-shaped read available and is used as the load source instead.
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
        const pipeline = await clients.pipeline.getPipeline({ orgId, id: pipelineId });
        if (cancelled) return;
        if (pipeline.source !== 'visual') {
          setLoadError(
            `"${pipeline.name}" wasn't created with the visual builder and can't be edited here. Open its graph view and use "Recreate as visual pipeline" instead.`,
          );
          setLoadState('error');
          return;
        }
        const gv = await graphView(orgId, pipelineId);
        if (cancelled) return;
        if (gv.opaque) {
          setLoadError(
            gv.warning || 'This pipeline could not be fully loaded into the visual builder.',
          );
          setLoadState('error');
          return;
        }
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
  }, [pipelineId, orgId]);

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
