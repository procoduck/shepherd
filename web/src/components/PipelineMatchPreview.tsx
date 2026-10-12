import { useQueries, useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import { REJECTED_KIND, reconciliationQuery } from '@/components/CollectorReconciliation';
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

/** At most this many FAILED collectors are asked which pipeline they rejected. */
const MAX_REJECTION_LOOKUPS = 20;

/**
 * The matched collectors that rejected THIS pipeline (#297). A collector's
 * load error points at a line of its merged config; its reconciliation's
 * served_config_rejected finding names the pipeline that line belongs to.
 * Only collectors reading FAILED are asked, so a healthy fleet costs one
 * ListCollectors call.
 */
function PipelineRejections({
  pipelineName,
  orgId,
  matchedIds,
}: {
  pipelineName: string;
  orgId: string;
  matchedIds: string[];
}) {
  const { data: fleet } = useQuery({
    queryKey: ['collectors', orgId],
    queryFn: () => clients.fleet.listCollectors({ orgId }),
    enabled: !!orgId && matchedIds.length > 0,
  });
  const matched = new Set(matchedIds);
  const allFailed = (fleet?.items ?? []).filter(
    (c) => matched.has(c.id) && c.remoteConfigStatus.toUpperCase() === 'FAILED',
  );
  const failed = allFailed.slice(0, MAX_REJECTION_LOOKUPS);
  const unchecked = allFailed.length - failed.length;
  // One request per failing collector: fetched on mount and window focus
  // only, never polled (the collector page's own tab keeps its poll).
  const results = useQueries({
    queries: failed.map((c) => ({
      ...reconciliationQuery(orgId, c.id),
      refetchInterval: false as const,
    })),
  });
  const rejections = failed.flatMap((c, i) => {
    const f = results[i]?.data?.findings.find(
      (x) => x.kind === REJECTED_KIND && x.pipelineName === pipelineName,
    );
    return f ? [{ collector: c, summary: f.summary }] : [];
  });
  if (rejections.length === 0) return null;
  return (
    <Banner variant='error' testId='pipeline-rejected-by'>
      <ul className='space-y-1'>
        {rejections.map(({ collector: c, summary }) => (
          <li key={c.id}>
            <span className='font-medium'>
              {c.cluster}/{c.role}:
            </span>{' '}
            {summary} Error: <code className='font-mono'>{c.remoteConfigError}</code>
          </li>
        ))}
      </ul>
      {unchecked > 0 && (
        <p className='mt-1'>
          {unchecked} more failing collector{unchecked === 1 ? ' was' : 's were'} not checked; open
          each collector's Reconciliation tab to see which pipeline it rejected.
        </p>
      )}
    </Banner>
  );
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
      <PipelineRejections
        pipelineName={pipeline.name}
        orgId={orgId}
        matchedIds={collectors.filter((c) => !c.excludedReason).map((c) => c.id)}
      />
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
