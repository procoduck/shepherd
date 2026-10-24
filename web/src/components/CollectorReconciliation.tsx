import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import { toneClass } from '@/components/ui/statusTone';

/**
 * The heading for one finding kind (internal/reconcile's Kind values).
 * role_signal_excluded (M2) is a pipeline whose matchers select this collector
 * but whose signals its role refuses, so the served config leaves it out —
 * enforcement working, but a pipeline the operator aimed here is not running.
 */
function findingLabel(kind: string): string {
  switch (kind) {
    case 'unserved_component_observed':
      return 'Running, not served';
    case 'role_signal_excluded':
      return 'Matched, excluded by role';
    case REJECTED_KIND:
      return 'Rejected by collector';
    default:
      return 'Role / signal mismatch';
  }
}

/**
 * served_config_rejected (#297): the collector refused the config it is
 * served. When the load error's line falls inside a pipeline's declare
 * block, pipelineName names that pipeline and the summary reads "rejected in
 * pipeline "X" (line N of its text, …)".
 */
export const REJECTED_KIND = 'served_config_rejected';

/** The reconciliation query, shared by the tab, the banner and the pipeline page. */
export function reconciliationQuery(orgId: string, id: string) {
  return {
    queryKey: ['reconciliation', orgId, id],
    queryFn: () => clients.fleet.getReconciliation({ orgId, id }),
    enabled: !!orgId && !!id,
    refetchInterval: 30000,
  };
}

/**
 * The collector banner's line naming the pipeline a FAILED collector
 * rejected (#297). Renders nothing until the finding names one.
 */
export function CollectorRejectionNote({ orgId, id }: { orgId: string; id: string }) {
  const { data } = useQuery(reconciliationQuery(orgId, id));
  const f = data?.findings.find((x) => x.kind === REJECTED_KIND && x.pipelineName);
  if (!f) return null;
  return (
    <p className='mt-1' data-testid='collector-rejected-pipeline'>
      {f.summary}
    </p>
  );
}

/**
 * The CollectorDetailPage "Reconciliation" tab (#110): declared-vs-served-vs-
 * observed drift for one collector, from FleetService.GetReconciliation. Split
 * out of CollectorDetailPage to keep that page under the file-size guard and to
 * own its own query (mounted only while the tab is active).
 */
export function CollectorReconciliation({
  orgId,
  id,
  status,
}: {
  orgId: string;
  id: string;
  /** The collector's remote_config_status, e.g. FAILED (upper-cased). */
  status: string;
}) {
  const { data, isLoading } = useQuery(reconciliationQuery(orgId, id));
  const findings = data?.findings ?? [];

  return (
    <div className='space-y-3' data-testid='reconciliation-tab'>
      <p className='text-xs text-muted-2'>
        Drift between what this collector's role declares, what Shepherd serves it, and what it is
        observed running. A managed pipeline running that is no longer served is drift the collector
        will clear on its next config reload. A pipeline matched but excluded by role is not served
        here until its signals or its matchers change.
      </p>
      {status === 'FAILED' && (
        // Reconciliation compares signals; it cannot see that the agent
        // rejected its served config. A FAILED collector is running its
        // previous config, so "served" is not what is running (#198). The
        // notice no longer points at a list "below" (#297 L3): when the
        // server can attribute the rejection, it is a finding of its own.
        <div
          className='rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400'
          data-testid='reconciliation-load-failed'
        >
          Not in sync — this collector rejected the config it is being served and is still running
          its previous one, so what it runs is not what it is served until that config loads. The
          error is shown above.
        </div>
      )}
      {isLoading ? (
        <p className='text-sm text-muted'>Checking…</p>
      ) : findings.length === 0 ? (
        status === 'FAILED' ? null : !data?.observedKnown ? (
          // No beacon inventory: observed is UNKNOWN, never agreement
          // (internal/reconcile). Saying "in sync" here read as a clean bill
          // of health for a collector Shepherd cannot see (0.22 -> 0.23
          // upgrade walkthrough).
          <div
            className={`rounded-lg border px-4 py-3 text-sm ${toneClass('neutral')}`}
            data-testid='reconciliation-observed-unknown'
          >
            <p className='font-medium'>
              Observed state unknown — no beacon data from this collector yet.
            </p>
            <p className='mt-1'>
              Declared and served agree, but nothing has reported what is running. The
              self-monitoring beacon in the served config writes back to Shepherd; with agent-token
              auth the collector must expose its token as <code>SHEPHERD_AGENT_TOKEN_ID</code> and{' '}
              <code>SHEPHERD_AGENT_TOKEN_SECRET</code>, and <code>server.base_url</code> must be
              reachable from its cluster.{' '}
              <a
                href='https://procoduck.github.io/shepherd/docs/collectors.html'
                target='_blank'
                rel='noreferrer'
                className='underline'
              >
                Collectors docs
              </a>
            </p>
          </div>
        ) : (
          <div
            className='rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-4 py-3 text-sm text-emerald-400'
            data-testid='reconciliation-in-sync'
          >
            In sync — declared, served and observed all agree.
          </div>
        )
      ) : (
        <ul className='space-y-2' data-testid='reconciliation-findings'>
          {findings.map((f, i) => (
            <li
              key={`${f.kind}-${f.controllerPath || f.pipelineName}-${i}`}
              className={
                f.kind === REJECTED_KIND
                  ? 'rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3'
                  : 'rounded-lg border border-amber-500/30 bg-amber-500/10 px-4 py-3'
              }
            >
              <div className='flex items-center gap-2'>
                <span
                  className={`text-xs font-medium uppercase tracking-wide ${
                    f.kind === REJECTED_KIND ? 'text-red-400' : 'text-amber-400'
                  }`}
                >
                  {findingLabel(f.kind)}
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
