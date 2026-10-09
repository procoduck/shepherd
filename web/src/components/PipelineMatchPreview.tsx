import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import { QueryError } from '@/components/QueryError';
import { Banner } from '@/components/ui/Banner';
import type { MatchedCollector, Pipeline } from '@/gen/shepherd/mgmt/v1/pipeline_pb';

/**
 * Groups matched collectors that role enforcement (gate G6) leaves out of
 * their served config by the server's reason, in first-seen order — one line
 * per reason, collectors sharing a cluster/role counted rather than repeated
 * (the server's wizard warnings read the same): "Excluded from 4
 * collector(s): prod/metrics ×3, dev/metrics — its signals (logs) are not
 * allowed on role metrics."
 */
export function exclusionLines(collectors: MatchedCollector[]): string[] {
  // reason -> cluster/role name -> how many matched collectors share it
  const byReason = new Map<string, Map<string, number>>();
  for (const c of collectors) {
    if (!c.excludedReason) continue;
    const names = byReason.get(c.excludedReason) ?? new Map<string, number>();
    const name = `${c.cluster}/${c.role}`;
    names.set(name, (names.get(name) ?? 0) + 1);
    byReason.set(c.excludedReason, names);
  }
  return [...byReason].map(([reason, names]) => {
    const total = [...names.values()].reduce((a, b) => a + b, 0);
    const labels = [...names].map(([name, n]) => (n > 1 ? `${name} ×${n}` : name));
    return `Excluded from ${total} collector(s): ${labels.join(', ')} — ${reason}.`;
  });
}

/**
 * The pipeline page's match preview (walkthrough finding M2): how many
 * collectors the SAVED pipeline's matchers select, and a warning for every
 * one whose role refuses its signals — the server fills excluded_reason from
 * the same check the served config is assembled with. Without it, such a
 * pipeline saved and enabled silently and only showed up as a comment in
 * each collector's served config.
 *
 * The query key extends the page's ['pipeline', queryOrgId, id] key, so every
 * mutation that invalidates the pipeline (save, enable/disable, restore,
 * detach) refetches this too.
 */
export function PipelineMatchPreview({
  pipeline,
  orgId,
  queryOrgId,
}: {
  pipeline: Pipeline;
  orgId: string;
  queryOrgId: string;
}) {
  const { data, error } = useQuery({
    queryKey: ['pipeline', queryOrgId, pipeline.id, 'matches'],
    queryFn: () => clients.pipeline.previewMatches({ orgId, id: pipeline.id }),
    enabled: !!orgId,
  });
  // A failed preview is not "matches nothing" and not "nothing to warn
  // about": say it failed, so a missing exclusion warning is never silent.
  if (error && !data) {
    return (
      <QueryError
        error={error}
        noun='the collectors this pipeline matches'
        testId='pipeline-match-error'
      />
    );
  }
  if (!data) return null;
  const collectors = data.collectors ?? [];
  const lines = exclusionLines(collectors);

  return (
    <div className='space-y-2'>
      {/* The SAVED pipeline: unsaved edits to the matchers or contents are
          not previewed until they are saved. */}
      <p className='text-xs text-muted-2' data-testid='pipeline-match-count'>
        Saved pipeline matches {collectors.length} collector{collectors.length === 1 ? '' : 's'}
        {pipeline.enabled ? '' : ' (when enabled)'}.
      </p>
      {lines.length > 0 && (
        <Banner variant='warning' testId='pipeline-role-exclusion'>
          <ul className='space-y-1'>
            {lines.map((l) => (
              <li key={l}>{l}</li>
            ))}
          </ul>
        </Banner>
      )}
    </div>
  );
}
