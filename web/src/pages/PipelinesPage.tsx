import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Plus } from 'lucide-react';
import { clients } from '@/api/transport';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import type { Pipeline } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { useCanWrite, useOrgId } from '@/hooks/useOrg';

function pipelineColumns(
  canWrite: boolean,
  onToggle: (p: Pipeline) => void,
  staleVersions: Map<string, string>,
): DataTableColumn<Pipeline>[] {
  return [
    {
      key: 'name',
      header: 'Name',
      render: (p) => (
        <Link to='/pipelines/$id' params={{ id: p.id }} className='hover:text-indigo-400'>
          {p.name}
        </Link>
      ),
    },
    {
      key: 'source',
      header: 'Source',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (p) => (
        <div className='flex flex-col items-start gap-1'>
          {p.source === 'visual' ? (
            <Link
              to='/pipelines/$id/visual'
              params={{ id: p.id }}
              className='text-indigo-400 hover:text-indigo-300'
            >
              visual ↗
            </Link>
          ) : (
            p.source
          )}
          {staleVersions.has(p.id) && (
            <Link
              data-testid='pipeline-stale-render'
              to='/pipelines/$id/visual'
              params={{ id: p.id }}
              className='text-[11px] leading-tight text-amber-400 hover:text-amber-300'
            >
              rendered under {staleVersions.get(p.id)} — open the builder and Save to re-render
            </Link>
          )}
        </div>
      ),
    },
    {
      key: 'matchers',
      header: 'Matchers',
      render: (p) => (
        <div className='flex flex-wrap gap-1'>
          {p.matchers.slice(0, 3).map((m) => (
            <span key={m} className='font-mono text-xs bg-border px-1.5 py-0.5 rounded'>
              {m}
            </span>
          ))}
        </div>
      ),
    },
    {
      key: 'enabled',
      header: 'Enabled',
      render: (p) =>
        canWrite ? (
          <button
            onClick={() => onToggle(p)}
            className={`w-9 h-5 rounded-full relative transition-colors ${p.enabled ? 'bg-emerald-600' : 'bg-border-strong'}`}
            aria-label={p.enabled ? 'Disable' : 'Enable'}
          >
            <span
              className={`absolute top-0.5 w-4 h-4 rounded-full bg-white shadow transition-transform ${p.enabled ? 'translate-x-4' : 'translate-x-0.5'}`}
            />
          </button>
        ) : (
          <span className='text-xs text-muted'>{p.enabled ? 'Enabled' : 'Disabled'}</span>
        ),
    },
  ];
}

export function PipelinesPage() {
  const orgId = useOrgId();
  const canWrite = useCanWrite();
  const qc = useQueryClient();
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['pipelines', orgId],
    queryFn: () => clients.pipeline.listPipelines({ orgId }),
    enabled: !!orgId,
  });

  // The server already knows which visual pipelines were rendered under an
  // older schema (needs_upgrade, rpc_pipeline.go) — this is UI-only: it never
  // re-renders anything, it just tells a viewer to open the builder and Save.
  const { data: staleData } = useQuery({
    queryKey: ['pipelines', orgId, 'needs-upgrade'],
    queryFn: () => clients.pipeline.listPipelines({ orgId, needsUpgrade: true }),
    enabled: !!orgId,
  });
  const staleVersions = new Map<string, string>(
    (staleData?.items ?? []).map((p) => [
      p.id,
      (p.wizardState as { schema_version?: string } | undefined)?.schema_version ?? '',
    ]),
  );

  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      enabled
        ? clients.pipeline.disablePipeline({ orgId, id })
        : clients.pipeline.enablePipeline({ orgId, id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['pipelines', orgId] }),
  });

  return (
    <div className='space-y-4'>
      <div className='flex items-center justify-between'>
        <h1 className='text-xl font-semibold'>Pipelines</h1>
        {canWrite && (
          <div className='flex items-center gap-2'>
            <Link
              data-testid='pipeline-visual-builder'
              to='/pipelines/visual/new'
              className='flex items-center gap-1.5 rounded-md border border-indigo-600 px-3 py-1.5 text-xs font-medium text-indigo-600 hover:bg-indigo-50'
            >
              <Plus size={14} /> Visual builder
            </Link>
            <Link
              data-testid='pipeline-new'
              to='/pipelines/new'
              className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
            >
              <Plus size={14} /> New pipeline
            </Link>
          </div>
        )}
      </div>

      {isError ? (
        <QueryError error={error} noun='pipelines' />
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>No pipelines yet.</p>
        </div>
      ) : (
        <DataTable
          columns={pipelineColumns(
            canWrite,
            (p) => toggle.mutate({ id: p.id, enabled: p.enabled }),
            staleVersions,
          )}
          rows={data?.items ?? []}
          rowKey={(p) => p.id}
          rowClassName='border-t border-border hover:bg-card/60 cursor-pointer'
          rowProps={(p) => ({ 'data-testid': `pipeline-row-${p.name}` })}
        />
      )}
    </div>
  );
}
