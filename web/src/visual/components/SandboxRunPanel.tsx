// SandboxRunPanel — S3 sandbox run UI (VB-1 design doc §6.4 step 4).
//
// Owns the whole "Simulate ▾ -> Sandbox run (30s)…" flow: submits the
// current graph, polls GetRun until terminal, and renders the progress and
// results views. Health results are pushed into the shared store
// (`simHealthByNode`) rather than kept local, because CanvasPane — a
// sibling of Toolbar, not a descendant — needs them for the per-node health
// badges (see PipelineNode's `health` field and CanvasPane's "controlled-mode
// contract" for why that has to go through the document-side projection
// rather than touching React Flow's own node objects).
import { useCallback, useEffect, useRef, useState } from 'react';
import { createSandboxRun, getSandboxRun, type SimulateRunResult } from '../../api/client';
import { toApiError } from '../../api/transport';
import { useVisualStore } from '../store';

// The design doc's own label ("Sandbox run (30s)…") names the requested
// duration; the server clamps to [1,120] and is free to return a different
// `requested_duration_seconds` on the run itself, which is what the
// countdown actually reads (see `remainingSeconds` below).
const REQUESTED_DURATION_SECONDS = 30;
const POLL_INTERVAL_MS = 500;

// UI progress states per §6.4 step 4: "validating -> transforming -> running
// (30s countdown) -> collecting". These don't map onto RunStatus 1:1 — the
// server only has queued/running/completed/failed/expired — so:
//   validating:   the CreateRun request itself is in flight (client-side).
//   transforming: status === 'queued' (the graph transform + stage1-2 gate
//                 run before the worker flips a run to 'running', and the
//                 server has no separate status for that window).
//   running:      status === 'running', counting down from
//                 requested_duration_seconds.
//   collecting:   status === 'running' but the countdown has already reached
//                 zero — the harness is still assembling results.
//   results/error: terminal.
type Phase = 'idle' | 'validating' | 'transforming' | 'running' | 'results' | 'error';
type ResultTab = 'metrics' | 'logs' | 'health';

const PHASE_STEPS: { key: Exclude<Phase, 'idle' | 'results' | 'error'>; label: string }[] = [
  { key: 'validating', label: 'Validating' },
  { key: 'transforming', label: 'Transforming' },
  { key: 'running', label: 'Running' },
];

function ProgressSteps({ phase, collecting }: { phase: Phase; collecting: boolean }) {
  const order = PHASE_STEPS.map((s) => s.key);
  const idx = order.indexOf(phase as (typeof order)[number]);
  return (
    <div className='flex gap-4' data-testid='sandbox-run-steps'>
      {PHASE_STEPS.map((step, i) => (
        <span
          key={step.key}
          data-testid={`sim-phase-${step.key}`}
          data-active={i === idx}
          className={
            i === idx ? 'text-accent font-medium' : i < idx ? 'text-emerald-400' : 'text-muted'
          }
        >
          {step.label}
        </span>
      ))}
      <span
        data-testid='sim-phase-collecting'
        data-active={collecting}
        className={collecting ? 'text-accent font-medium' : 'text-muted'}
      >
        Collecting
      </span>
    </div>
  );
}

function ResultsView({
  run,
  tab,
  setTab,
  seriesFilter,
  setSeriesFilter,
}: {
  run: SimulateRunResult;
  tab: ResultTab;
  setTab: (t: ResultTab) => void;
  seriesFilter: string;
  setSeriesFilter: (v: string) => void;
}) {
  const failed = run.status === 'failed' || run.status === 'expired';
  const filteredSeries = run.captured_series.filter((series) =>
    series.name.toLowerCase().includes(seriesFilter.toLowerCase()),
  );
  return (
    <div data-testid='sandbox-run-results'>
      {failed && (
        <div
          data-testid='sandbox-run-status-error'
          className='mb-3 border border-red-500/50 bg-red-500/10 text-red-400 rounded p-2'
        >
          <strong>{run.status === 'expired' ? 'Run expired' : 'Run failed'}</strong>
          {run.error_code && <span className='ml-1 font-mono'>({run.error_code})</span>}
          {run.error_message && <div className='mt-1'>{run.error_message}</div>}
        </div>
      )}
      {/* §6.5: every results view carries a one-line fidelity note naming its tier's limits. */}
      <div data-testid='sandbox-run-fidelity-note' className='mb-3 text-muted italic'>
        {run.fidelity_note}
      </div>
      <div className='flex gap-3 mb-2 border-b border-border pb-2'>
        <button
          type='button'
          data-testid='sim-results-tab-metrics'
          onClick={() => setTab('metrics')}
          className={
            tab === 'metrics' ? 'font-medium text-accent' : 'text-muted hover:text-muted-2'
          }
        >
          Metrics captured ({run.captured_series.length})
        </button>
        <button
          type='button'
          data-testid='sim-results-tab-logs'
          onClick={() => setTab('logs')}
          className={tab === 'logs' ? 'font-medium text-accent' : 'text-muted hover:text-muted-2'}
        >
          Logs captured ({run.captured_log_lines.length})
        </button>
        <button
          type='button'
          data-testid='sim-results-tab-health'
          onClick={() => setTab('health')}
          className={tab === 'health' ? 'font-medium text-accent' : 'text-muted hover:text-muted-2'}
        >
          Component health ({run.component_health.length})
        </button>
      </div>

      {tab === 'metrics' && (
        <div data-testid='sim-results-metrics'>
          <input
            data-testid='sim-series-filter'
            placeholder='Filter series by name…'
            value={seriesFilter}
            onChange={(e) => setSeriesFilter(e.target.value)}
            className='border rounded px-2 py-1 mb-2 w-full bg-background'
          />
          {filteredSeries.length === 0 ? (
            <p className='text-muted' data-testid='sim-series-empty'>
              No series captured.
            </p>
          ) : (
            <table className='w-full text-left' data-testid='sim-series-table'>
              <thead className='text-muted-2'>
                <tr>
                  <th className='font-normal'>Name</th>
                  <th className='font-normal'>Labels</th>
                  <th className='font-normal'>Samples</th>
                </tr>
              </thead>
              <tbody>
                {filteredSeries.map((series) => (
                  <tr
                    key={`${series.name}-${JSON.stringify(series.labels)}`}
                    data-testid='sim-series-row'
                  >
                    <td className='font-mono'>{series.name}</td>
                    <td className='font-mono text-muted'>{JSON.stringify(series.labels)}</td>
                    <td>{series.sample_count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {tab === 'logs' && (
        <div data-testid='sim-results-logs'>
          {run.captured_log_lines.length === 0 ? (
            <p className='text-muted' data-testid='sim-logs-empty'>
              No log lines captured.
            </p>
          ) : (
            run.captured_log_lines.map((line) => (
              <div
                key={`${JSON.stringify(line.labels)}-${line.line}`}
                data-testid='sim-log-row'
                className='border-b border-border py-1'
              >
                <div className='font-mono text-muted'>{JSON.stringify(line.labels)}</div>
                <div className='font-mono'>{line.line}</div>
              </div>
            ))
          )}
        </div>
      )}

      {tab === 'health' && (
        <div data-testid='sim-results-health'>
          {run.component_health.length === 0 ? (
            <p className='text-muted' data-testid='sim-health-empty'>
              No component health reported.
            </p>
          ) : (
            <table className='w-full text-left' data-testid='sim-health-table'>
              <thead className='text-muted-2'>
                <tr>
                  <th className='font-normal'>Node</th>
                  <th className='font-normal'>Component</th>
                  <th className='font-normal'>State</th>
                  <th className='font-normal'>Message</th>
                </tr>
              </thead>
              <tbody>
                {run.component_health.map((h) => (
                  <tr
                    key={h.node_id}
                    data-testid='sim-health-row'
                    data-health-state={h.health_state}
                  >
                    <td>{h.node_label}</td>
                    <td className='font-mono'>{h.component}</td>
                    <td>{h.health_state}</td>
                    <td>{h.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          {/* The sandbox Alloy's own stderr (design §6.4 step 3). Health says a
              component is unhealthy; stderr is usually the only thing that says
              WHY — and when a run captures nothing at all, it is the sole
              signal the user has. It was plumbed all the way from simsvc to
              api/client.ts and then never rendered. */}
          {run.stderr_tail && (
            <details className='mt-4' data-testid='sim-health-stderr'>
              <summary className='cursor-pointer text-muted-2'>Sandbox stderr</summary>
              <pre className='mt-2 max-h-48 overflow-auto whitespace-pre-wrap rounded border border-border bg-background p-2 font-mono text-[11px] text-muted'>
                {run.stderr_tail}
              </pre>
            </details>
          )}
        </div>
      )}

      {/* "What was rewritten" disclosure — design doc §6.4 step 4, keeps
          fidelity honest by listing every transform actually applied. Left
          open by default: this is the security-relevant half of the
          results (destination rewrites, secret drops), not a footnote. */}
      <details className='mt-4 border-t border-border pt-2' data-testid='sandbox-run-rewrites' open>
        <summary className='cursor-pointer text-muted-2'>
          What was rewritten ({run.rewrites.length})
        </summary>
        {run.rewrites.length === 0 ? (
          <p className='text-muted mt-1'>No rewrites were needed.</p>
        ) : (
          <ul className='mt-1 space-y-1'>
            {run.rewrites.map((rw) => (
              <li
                key={`${rw.node_id}-${rw.kind}-${rw.detail}`}
                data-testid='sandbox-run-rewrite-row'
              >
                <span className='font-mono'>{rw.node_label}</span>{' '}
                <span className='text-muted'>({rw.component})</span> —{' '}
                <span className='text-accent'>{rw.kind}</span>
                {rw.detail ? `: ${rw.detail}` : ''}
              </li>
            ))}
          </ul>
        )}
      </details>
    </div>
  );
}

export function SandboxRunPanel({ orgId }: { orgId: string | undefined }) {
  const doc = useVisualStore((s) => s.doc);
  const setSimHealthByNode = useVisualStore((s) => s.setSimHealthByNode);

  const [menuOpen, setMenuOpen] = useState(false);
  const [open, setOpen] = useState(false);
  const [phase, setPhase] = useState<Phase>('idle');
  const [run, setRun] = useState<SimulateRunResult | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [resultTab, setResultTab] = useState<ResultTab>('metrics');
  const [seriesFilter, setSeriesFilter] = useState('');
  const [nowTick, setNowTick] = useState(() => Date.now());

  const pollRef = useRef<number | null>(null);

  const stopPolling = useCallback(() => {
    if (pollRef.current !== null) {
      window.clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);

  // Stop the poll timer (never the shared health projection — see the
  // dedicated cleanup effect below) on unmount.
  useEffect(() => stopPolling, [stopPolling]);

  const close = useCallback(() => {
    stopPolling();
    setOpen(false);
    setPhase('idle');
    setRun(null);
    setErrorMessage(null);
    setSimHealthByNode(null);
  }, [stopPolling, setSimHealthByNode]);

  // Clear a lingering health projection if the whole builder unmounts while
  // a run's results are still on screen (navigating away mid-view).
  useEffect(() => () => setSimHealthByNode(null), [setSimHealthByNode]);

  const poll = useCallback(
    async (pollOrgId: string, runId: string): Promise<'ongoing' | 'terminal' | 'error'> => {
      try {
        const r = await getSandboxRun(pollOrgId, runId);
        setRun(r);
        if (r.component_health.length > 0) {
          const byNode: Record<string, { health_state: string; message: string }> = {};
          for (const h of r.component_health) {
            byNode[h.node_id] = { health_state: h.health_state, message: h.message };
          }
          setSimHealthByNode(byNode);
        }
        if (r.status === 'queued') {
          setPhase('transforming');
          return 'ongoing';
        }
        if (r.status === 'running') {
          setPhase('running');
          return 'ongoing';
        }
        // completed | failed | expired
        setPhase('results');
        return 'terminal';
      } catch (e) {
        setPhase('error');
        setErrorMessage(toApiError(e).message || 'Failed to fetch run status');
        return 'error';
      }
    },
    [setSimHealthByNode],
  );

  const start = useCallback(async () => {
    if (!orgId) return;
    setMenuOpen(false);
    setOpen(true);
    setPhase('validating');
    setRun(null);
    setErrorMessage(null);
    setResultTab('metrics');
    setSeriesFilter('');
    setSimHealthByNode(null);
    try {
      const { run_id } = await createSandboxRun(orgId, doc, REQUESTED_DURATION_SECONDS);
      setPhase('transforming');
      const outcome = await poll(orgId, run_id);
      if (outcome === 'ongoing') {
        pollRef.current = window.setInterval(() => {
          poll(orgId, run_id).then((o) => {
            if (o !== 'ongoing') stopPolling();
          });
        }, POLL_INTERVAL_MS);
      }
    } catch (e) {
      setPhase('error');
      setErrorMessage(toApiError(e).message || 'Failed to start sandbox run');
    }
  }, [orgId, doc, poll, stopPolling, setSimHealthByNode]);

  // Ticks once a second while running, purely to re-render the countdown —
  // the source of truth for elapsed time is always `run.started_at`, never
  // this timer's own count.
  useEffect(() => {
    if (phase !== 'running') return;
    const id = window.setInterval(() => setNowTick(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [phase]);

  const requestedSeconds = run?.requested_duration_seconds || REQUESTED_DURATION_SECONDS;
  const remainingSeconds = run?.started_at
    ? Math.max(
        0,
        requestedSeconds - Math.floor((nowTick - new Date(run.started_at).getTime()) / 1000),
      )
    : requestedSeconds;
  const collecting = phase === 'running' && remainingSeconds <= 0;

  return (
    <div className='relative'>
      <button
        type='button'
        data-testid='simulate-menu-trigger'
        onClick={() => setMenuOpen((v) => !v)}
        disabled={!orgId}
        className='text-sm px-3 py-1 rounded border shrink-0 disabled:opacity-50 disabled:cursor-not-allowed'
      >
        Simulate ▾
      </button>
      {menuOpen && (
        <div
          data-testid='simulate-menu'
          className='absolute z-20 top-full left-0 mt-1 bg-card border border-border rounded shadow-md text-xs whitespace-nowrap'
        >
          <button
            type='button'
            data-testid='simulate-menu-sandbox-run'
            onClick={start}
            className='block w-full text-left px-3 py-2 hover:bg-accent/10'
          >
            Sandbox run (30s)…
          </button>
        </div>
      )}

      {open && (
        <div
          data-testid='sandbox-run-overlay'
          className='fixed inset-0 z-50 bg-black/50 flex items-center justify-center'
          onClick={close}
        >
          <div
            data-testid='sandbox-run-dialog'
            className='bg-panel border border-border rounded-lg w-[720px] max-h-[85vh] flex flex-col text-xs'
            onClick={(e) => e.stopPropagation()}
          >
            <div className='flex items-center justify-between px-4 py-2 border-b border-border shrink-0'>
              <span className='text-sm font-medium'>Sandbox run</span>
              <button
                type='button'
                data-testid='sandbox-run-close'
                aria-label='Close'
                onClick={close}
                className='text-muted hover:text-muted-2'
              >
                ×
              </button>
            </div>
            <div className='flex-1 overflow-y-auto p-4'>
              {phase === 'error' ? (
                <div data-testid='sandbox-run-error' className='text-red-500'>
                  {errorMessage}
                </div>
              ) : phase === 'results' && run ? (
                <ResultsView
                  run={run}
                  tab={resultTab}
                  setTab={setResultTab}
                  seriesFilter={seriesFilter}
                  setSeriesFilter={setSeriesFilter}
                />
              ) : (
                <div data-testid='sandbox-run-progress'>
                  <ProgressSteps phase={phase} collecting={collecting} />
                  {phase === 'running' && (
                    <div
                      data-testid='sandbox-run-countdown'
                      className='mt-4 text-2xl font-mono text-center'
                    >
                      {collecting ? 'Collecting…' : `${remainingSeconds}s`}
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
