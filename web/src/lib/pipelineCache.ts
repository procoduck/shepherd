import type { QueryClient } from '@tanstack/react-query';
import type { Pipeline } from '@/gen/shepherd/mgmt/v1/pipeline_pb';

// Brings every query the pipeline page (PipelineEditorPage) reads up to date
// with a pipeline another surface — the visual builder, a wizard — has just
// written, before that surface navigates there.
//
// The pipeline entry is seeded with the write's own response (it IS the server
// copy) so the page's first render already shows it; without this it rendered
// whatever the cache held from before the write and showed the old matchers
// (H1). Revisions and the list are only marked stale — awaited, so an active
// observer has refetched before the navigation — and inactive ones refetch
// when the page mounts them.
export async function primePipelineCache(
  qc: QueryClient,
  orgId: string,
  p: Pipeline,
): Promise<void> {
  qc.setQueryData(['pipeline', orgId, p.id], p);
  await Promise.all([
    qc.invalidateQueries({ queryKey: ['revisions', orgId, p.id] }),
    qc.invalidateQueries({ queryKey: ['pipelines', orgId] }),
  ]);
}
