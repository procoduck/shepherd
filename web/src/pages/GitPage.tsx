import { timestampDate } from '@bufbuild/protobuf/wkt';
import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import { useOrgId } from '@/hooks/useOrg';

export function GitPage() {
  const orgId = useOrgId();
  const { data, isLoading } = useQuery({
    queryKey: ['repo-links', orgId],
    queryFn: () => clients.gitOps.listRepoLinks({ orgId }),
    enabled: !!orgId,
  });

  return (
    <div className='space-y-4'>
      <h1 className='text-xl font-semibold'>Git sync</h1>
      {!orgId ? (
        <p className='text-sm text-zinc-400'>No organisation context.</p>
      ) : isLoading ? (
        <p className='text-sm text-zinc-400'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-zinc-800 bg-zinc-900/40 p-8 text-center'>
          <p className='text-sm text-zinc-400'>No repository links configured.</p>
          <p className='text-xs text-zinc-600 mt-1'>
            Connect a Git repository to sync pipelines automatically.
          </p>
        </div>
      ) : (
        <div className='rounded-lg border border-zinc-800 overflow-hidden'>
          <table className='w-full text-sm'>
            <thead className='bg-zinc-900 text-zinc-400'>
              <tr>
                <th className='px-4 py-3 text-left font-medium'>Repository</th>
                <th className='px-4 py-3 text-left font-medium'>Branch</th>
                <th className='px-4 py-3 text-left font-medium'>Status</th>
                <th className='px-4 py-3 text-left font-medium'>Last synced</th>
              </tr>
            </thead>
            <tbody>
              {(data?.items ?? []).map((rl) => (
                <tr key={rl.id} className='border-t border-zinc-800 hover:bg-zinc-900/60'>
                  <td className='px-4 py-2.5 font-mono text-xs'>{rl.repository}</td>
                  <td className='px-4 py-2.5 text-zinc-400'>{rl.branch}</td>
                  <td className='px-4 py-2.5'>
                    <span
                      data-testid={rl.syncStatus === 'error' ? 'sync-status-error' : undefined}
                      className={`text-xs px-1.5 py-0.5 rounded ${
                        rl.syncStatus === 'error'
                          ? 'text-red-400 bg-red-400/10'
                          : 'text-emerald-400 bg-emerald-400/10'
                      }`}
                    >
                      {rl.syncStatus || 'ok'}
                    </span>
                  </td>
                  <td className='px-4 py-2.5 text-zinc-400 text-xs'>
                    {rl.lastSyncedAt ? timestampDate(rl.lastSyncedAt).toLocaleString() : '—'}
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
