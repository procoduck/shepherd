import { Link } from '@tanstack/react-router';
import { DataTable } from '@/components/ui/DataTable';
import { toneClass } from '@/components/ui/statusTone';
import type {
  ContributingPipeline,
  ExcludedPipeline,
  GetServedConfigResponse,
} from '@/gen/shepherd/mgmt/v1/fleet_pb';
import {
  exclusionReasonLabel,
  type LineSpanLike,
  linesLabel,
  matcherEvidenceText,
} from '@/lib/servedProvenance';

/** A row of the Contributing table: a pipeline, or the D6 baseline. */
type ContributingRow =
  | { kind: 'pipeline'; pipeline: ContributingPipeline }
  | { kind: 'baseline'; lines: LineSpanLike };

/** What a "Lines N–M" button asks the served-config view to show. */
export interface ShowLines {
  name: string;
  lines: LineSpanLike;
}

const badge = 'text-xs font-medium px-2 py-0.5 rounded border';

function LinesButton({
  name,
  lines,
  onShowLines,
}: {
  name: string;
  lines: LineSpanLike | undefined;
  onShowLines: (s: ShowLines) => void;
}) {
  if (!lines || lines.first < 1) return <span className='text-muted-3'>—</span>;
  return (
    <button
      type='button'
      onClick={() => onShowLines({ name, lines })}
      className='font-mono text-xs text-indigo-400 underline underline-offset-2 hover:opacity-80'
      aria-label={`Show ${linesLabel(lines).toLowerCase()} of the served config (${name})`}
      data-testid='contributing-lines'
    >
      {linesLabel(lines)}
    </button>
  );
}

function MatchedBy({ pipeline }: { pipeline: ContributingPipeline }) {
  if (pipeline.repoLink) {
    return <span className='text-xs text-muted'>Git repo link to this collector</span>;
  }
  return (
    <ul className='space-y-1'>
      {pipeline.matchers.map((m) => (
        <li key={m.matcher} className='flex flex-wrap items-center gap-2'>
          <code className='rounded bg-card px-1.5 py-0.5 font-mono text-xs text-zinc-300'>
            {m.matcher}
          </code>
          <span className='text-xs text-muted-2'>{matcherEvidenceText(m)}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * The collector's Pipelines tab (F-CONTRIB, #423): the pipelines its served
 * config is built from — each with the matchers that selected it and the
 * lines of its block in the served text — and the pipelines that matched but
 * were left out, with the reason. Read from GetServedConfig, which carries
 * what the recompute that wrote the served bytes recorded; nothing here
 * evaluates matchers itself.
 */
export function CollectorPipelines({
  served,
  loading,
  onShowLines,
}: {
  served: GetServedConfigResponse | undefined;
  loading: boolean;
  onShowLines: (s: ShowLines) => void;
}) {
  if (loading) return <p className='text-sm text-muted'>Loading pipelines…</p>;
  if (!served?.content) {
    return (
      <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
        <p className='text-sm text-muted-2'>No config served yet.</p>
        <p className='text-xs text-muted-3 mt-1'>
          Claim this cluster to an org and enable a matching pipeline.
        </p>
      </div>
    );
  }
  if (!served.provenanceRecorded) {
    return (
      <div
        className='rounded-lg border border-border bg-card/40 p-6 text-sm text-muted'
        data-testid='provenance-not-recorded'
      >
        Shepherd has not recorded which pipelines built this served config yet. It records them the
        next time the config is recomputed — on the collector's next poll.
      </div>
    );
  }

  const contributing: ContributingRow[] = served.contributing.map((pipeline) => ({
    kind: 'pipeline',
    pipeline,
  }));
  if (served.baselineLines && served.baselineLines.first > 0) {
    contributing.push({ kind: 'baseline', lines: served.baselineLines });
  }

  return (
    <div className='space-y-6'>
      <section className='space-y-2'>
        <h2 id='contributing-heading' className='text-sm font-medium text-zinc-300'>
          Contributing ({served.contributing.length})
        </h2>
        {contributing.length === 0 ? (
          <p className='text-sm text-muted-2' data-testid='contributing-empty'>
            No pipeline contributes to this collector's served config.
          </p>
        ) : (
          <DataTable<ContributingRow>
            testId='contributing-table'
            ariaLabelledBy='contributing-heading'
            rows={contributing}
            rowKey={(r) => (r.kind === 'pipeline' ? r.pipeline.pipelineId : 'baseline')}
            rowProps={(r) => ({
              'data-testid': r.kind === 'pipeline' ? 'contributing-row' : 'baseline-row',
            })}
            rowClassName='border-t border-border align-top'
            columns={[
              {
                key: 'pipeline',
                header: 'Pipeline',
                render: (r) =>
                  r.kind === 'pipeline' ? (
                    <div className='space-y-0.5'>
                      <Link
                        to='/pipelines/$id'
                        params={{ id: r.pipeline.pipelineId }}
                        className='font-medium text-zinc-200 underline underline-offset-2 hover:opacity-80'
                      >
                        {r.pipeline.name}
                      </Link>
                      <div className='text-xs text-muted-2'>
                        rev {r.pipeline.revision} · {r.pipeline.source}
                      </div>
                    </div>
                  ) : (
                    <div className='space-y-0.5'>
                      <span className='font-medium text-zinc-200'>Self-monitoring baseline</span>
                      <div>
                        <span className={`${badge} ${toneClass('neutral')}`}>baseline</span>
                      </div>
                    </div>
                  ),
              },
              {
                key: 'matched',
                header: 'Matched by',
                render: (r) =>
                  r.kind === 'pipeline' ? (
                    <MatchedBy pipeline={r.pipeline} />
                  ) : (
                    <span className='text-xs text-muted'>
                      Every collector (Shepherd's own beacon)
                    </span>
                  ),
              },
              {
                key: 'lines',
                header: 'Served lines',
                render: (r) =>
                  r.kind === 'pipeline' ? (
                    <LinesButton
                      name={r.pipeline.name}
                      lines={r.pipeline.lines}
                      onShowLines={onShowLines}
                    />
                  ) : (
                    <LinesButton
                      name='Self-monitoring baseline'
                      lines={r.lines}
                      onShowLines={onShowLines}
                    />
                  ),
              },
            ]}
          />
        )}
      </section>

      <section className='space-y-2'>
        <h2 id='excluded-heading' className='text-sm font-medium text-zinc-300'>
          Excluded ({served.excluded.length})
        </h2>
        {served.excluded.length === 0 ? (
          <p className='text-sm text-muted-2' data-testid='excluded-empty'>
            No pipeline that matches this collector was left out.
          </p>
        ) : (
          <DataTable<ExcludedPipeline>
            testId='excluded-table'
            ariaLabelledBy='excluded-heading'
            rows={served.excluded}
            rowKey={(e) => e.pipelineId || e.name}
            rowProps={() => ({ 'data-testid': 'excluded-row' })}
            rowClassName='border-t border-border align-top'
            columns={[
              {
                key: 'pipeline',
                header: 'Pipeline',
                render: (e) =>
                  e.pipelineId ? (
                    <Link
                      to='/pipelines/$id'
                      params={{ id: e.pipelineId }}
                      className='font-medium text-zinc-200 underline underline-offset-2 hover:opacity-80'
                    >
                      {e.name}
                    </Link>
                  ) : (
                    <span className='font-medium text-zinc-200'>{e.name}</span>
                  ),
              },
              {
                key: 'reason',
                header: 'Reason',
                render: (e) => (
                  <span
                    className={`${badge} whitespace-nowrap ${toneClass('warn')}`}
                    data-testid='excluded-reason'
                  >
                    {exclusionReasonLabel(e.reason)}
                  </span>
                ),
              },
              {
                key: 'message',
                header: 'Why',
                render: (e) => (
                  <span className='text-xs text-zinc-300 break-words'>{e.message}</span>
                ),
              },
            ]}
          />
        )}
      </section>
    </div>
  );
}
