import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { Plus } from 'lucide-react';
import { clients } from '@/api/transport';
import { useCanWrite, useOrgId } from '@/hooks/useOrg';

export function PipelinesPage() {
  const orgId = useOrgId();
  const canWrite = useCanWrite();
  const qc = useQueryClient();
  const { data, isLoading, isError } = useQuery({
    queryKey: ['pipelines', orgId],
    queryFn: () => clients.pipeline.listPipelines({ orgId }),
    enabled: !!orgId,
  });

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
              to='/pipelines/visual/new'
              className='flex items-center gap-1.5 rounded-md border border-indigo-600 px-3 py-1.5 text-xs font-medium text-indigo-600 hover:bg-indigo-50'
            >
              <Plus size={14} /> Visual builder
            </Link>
            <Link
              to='/pipelines/new'
              className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
            >
              <Plus size={14} /> New pipeline
            </Link>
          </div>
        )}
      </div>

      {isError ? (
        <div
          role='alert'
          className='rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400'
        >
          Failed to load pipelines. Please retry.
        </div>
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>No pipelines yet.</p>
        </div>
      ) : (
        <div className='rounded-lg border border-border overflow-hidden'>
          <table className='w-full text-sm'>
            <thead className='bg-card text-muted'>
              <tr>
                <th className='px-4 py-3 text-left font-medium'>Name</th>
                <th className='px-4 py-3 text-left font-medium'>Source</th>
                <th className='px-4 py-3 text-left font-medium'>Matchers</th>
                <th className='px-4 py-3 text-left font-medium'>Enabled</th>
              </tr>
            </thead>
            <tbody>
              {(data?.items ?? []).map((p) => (
                <tr key={p.id} className='border-t border-border hover:bg-card/60 cursor-pointer'>
                  <td className='px-4 py-2.5'>
                    <Link
                      to='/pipelines/$id'
                      params={{ id: p.id }}
                      className='hover:text-indigo-400'
                    >
                      {p.name}
                    </Link>
                  </td>
                  <td className='px-4 py-2.5 text-muted'>
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
                  </td>
                  <td className='px-4 py-2.5'>
                    <div className='flex flex-wrap gap-1'>
                      {p.matchers.slice(0, 3).map((m) => (
                        <span key={m} className='font-mono text-xs bg-border px-1.5 py-0.5 rounded'>
                          {m}
                        </span>
                      ))}
                    </div>
                  </td>
                  <td className='px-4 py-2.5'>
                    {canWrite ? (
                      <button
                        onClick={() => toggle.mutate({ id: p.id, enabled: p.enabled })}
                        className={`w-9 h-5 rounded-full relative transition-colors ${p.enabled ? 'bg-emerald-600' : 'bg-border-strong'}`}
                        aria-label={p.enabled ? 'Disable' : 'Enable'}
                      >
                        <span
                          className={`absolute top-0.5 w-4 h-4 rounded-full bg-white shadow transition-transform ${p.enabled ? 'translate-x-4' : 'translate-x-0.5'}`}
                        />
                      </button>
                    ) : (
                      <span className='text-xs text-muted'>
                        {p.enabled ? 'Enabled' : 'Disabled'}
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
