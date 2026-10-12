import { QueryError } from '@/components/QueryError';

/**
 * The pipeline page before its pipeline arrives. It used to render the whole
 * form empty and editable — a blank name, an empty editor — until GetPipeline
 * answered (2026-10-12 walkthrough), and a failed load looked the same.
 */
export function PipelineLoadState({ error }: { error: unknown }) {
  return error ? (
    <QueryError error={error} noun='this pipeline' testId='pipeline-load-error' />
  ) : (
    <p className='text-sm text-muted' data-testid='pipeline-loading'>
      Loading pipeline…
    </p>
  );
}
