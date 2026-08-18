import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { orgApi } from '@/api/client';
import { useOrgId } from '@/hooks/useOrg';

const STATUS_COLORS: Record<string, string> = {
  APPLIED: 'text-emerald-400 bg-emerald-400/10 border-emerald-400/20',
  APPLYING: 'text-yellow-400 bg-yellow-400/10 border-yellow-400/20',
  FAILED: 'text-red-400 bg-red-400/10 border-red-400/20',
};

function relativeTime(ts: string | undefined): string {
  if (!ts) return 'unknown';
  const diff = Date.now() - new Date(ts).getTime();
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return 'just now';
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function CollectorsPage() {
  const orgId = useOrgId();
  const { data, isLoading } = useQuery({
    queryKey: ['collectors', orgId],
    queryFn: () => orgApi.listCollectors(orgId),
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
                const status = c.remote_config_status?.toUpperCase() ?? '';
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
                    <td className='px-4 py-2.5 text-zinc-400'>{relativeTime(c.last_seen)}</td>
                    <td className='px-4 py-2.5 text-zinc-400'>{c.alloy_version ?? '—'}</td>
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
