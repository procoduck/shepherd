import { useParams } from '@tanstack/react-router';
import { useEffect, useRef } from 'react';
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
  const prevPipelineIdRef = useRef<string | null>(null);

  // Use a ref to the store action to avoid including it in the effect deps
  // (Zustand action references are stable but the selector creates new refs each render)
  const setSchemaRef = useRef(useVisualStore.getState().setSchema);

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
