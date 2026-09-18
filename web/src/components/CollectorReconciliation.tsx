import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';

/**
 * The CollectorDetailPage "Reconciliation" tab (#110): declared-vs-served-vs-
 * observed drift for one collector, from FleetService.GetReconciliation. Split
 * out of CollectorDetailPage to keep that page under the file-size guard and to
 * own its own query (mounted only while the tab is active).
 */
export function CollectorReconciliation({ orgId, id }: { orgId: string; id: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ['reconciliation', orgId, id],
    queryFn: () => clients.fleet.getReconciliation({ orgId, id }),
    enabled: !!orgId,
    refetchInterval: 30000,
  });
  const findings = data?.findings ?? [];

  return (
    <div className='space-y-3' data-testid='reconciliation-tab'>
      <p className='text-xs text-muted-2'>
        Drift between what this collector's role declares, what Shepherd serves it, and what it is
        observed running. A managed pipeline running that is no longer served is drift the collector
        will clear on its next config reload.
      </p>
      {isLoading ? (
        <p className='text-sm text-muted'>Checking…</p>
      ) : findings.length === 0 ? (
        <div
          className='rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-4 py-3 text-sm text-emerald-400'
          data-testid='reconciliation-in-sync'
        >
          In sync — declared, served and observed all agree.
        </div>
      ) : (
        <ul className='space-y-2' data-testid='reconciliation-findings'>
          {findings.map((f, i) => (
            <li
              key={`${f.kind}-${f.controllerPath || f.pipelineName}-${i}`}
              className='rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3'
            >
              <div className='flex items-center gap-2'>
                <span className='text-xs font-medium uppercase tracking-wide text-amber-400'>
                  {f.kind === 'unserved_component_observed'
                    ? 'Running, not served'
                    : 'Role / signal mismatch'}
                </span>
                {f.stale && (
                  <span className='rounded border border-border-strong px-1.5 py-0.5 text-[10px] text-muted-2'>
                    stale
                  </span>
                )}
                <span className='ml-auto text-[10px] text-muted-3'>{f.sources.join(' ↔ ')}</span>
              </div>
              <p className='mt-1 text-sm text-zinc-200'>{f.summary}</p>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
