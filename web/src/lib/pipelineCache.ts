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
  const key = ['pipeline', orgId, p.id];
  // A GetPipeline sent before the write (the breadcrumb's focus refetch, say)
  // may still be in flight; answering after the seed below, it would put the
  // pre-write copy back. Cancel it first.
  await qc.cancelQueries({ queryKey: key });
  qc.setQueryData(key, p);
  await Promise.all([
    qc.invalidateQueries({ queryKey: ['revisions', orgId, p.id] }),
    qc.invalidateQueries({ queryKey: ['pipelines', orgId] }),
  ]);
}
