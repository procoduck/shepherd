import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { clients } from '@/api/transport';
import { useOrgId } from '@/hooks/useOrg';
import { formatTimestampRelative } from '@/lib/utils';

const STATUS_COLORS: Record<string, string> = {
  APPLIED: 'text-emerald-400 bg-emerald-400/10 border-emerald-400/20',
  APPLYING: 'text-yellow-400 bg-yellow-400/10 border-yellow-400/20',
  FAILED: 'text-red-400 bg-red-400/10 border-red-400/20',
};

export function CollectorsPage() {
  const orgId = useOrgId();
  const { data, isLoading } = useQuery({
    queryKey: ['collectors', orgId],
    queryFn: () => clients.fleet.listCollectors({ orgId }),
    enabled: !!orgId,
  });

  return (
    <div className='space-y-4'>
      <div className='flex items-center justify-between'>
        <h1 className='text-xl font-semibold'>Collectors</h1>
      </div>
      {isLoading ? (
        <p className='text-sm text-zinc-400'>Loading…</p>
      ) : (
        <div className='rounded-lg border border-zinc-800 overflow-hidden'>
          <table className='w-full text-sm'>
            <thead className='bg-zinc-900 text-zinc-400'>
              <tr>
                <th className='px-4 py-3 text-left font-medium'>Cluster</th>
                <th className='px-4 py-3 text-left font-medium'>Role</th>
                <th className='px-4 py-3 text-left font-medium'>Status</th>
                <th className='px-4 py-3 text-left font-medium'>Last Seen</th>
                <th className='px-4 py-3 text-left font-medium'>Version</th>
              </tr>
            </thead>
            <tbody>
              {(data?.items ?? []).map((c) => {
                const status = c.remoteConfigStatus?.toUpperCase() ?? '';
                const statusColor =
                  STATUS_COLORS[status] ?? 'text-zinc-400 bg-zinc-800 border-zinc-700';
                return (
                  <tr
                    key={c.id}
                    className='border-t border-zinc-800 hover:bg-zinc-900/60 cursor-pointer'
                  >
                    <td className='px-4 py-2.5'>
                      <Link to='/collectors/$id' params={{ id: c.id }}>
                        {c.cluster}
                      </Link>
                    </td>
                    <td className='px-4 py-2.5 text-zinc-400'>{c.role}</td>
                    <td className='px-4 py-2.5'>
                      <span
                        className={`text-xs font-medium px-2 py-0.5 rounded border ${statusColor}`}
                      >
                        {status || 'UNKNOWN'}
                      </span>
                    </td>
                    <td className='px-4 py-2.5 text-zinc-400'>
                      {formatTimestampRelative(c.lastSeen)}
                    </td>
                    <td className='px-4 py-2.5 text-zinc-400'>{c.alloyVersion || '—'}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
