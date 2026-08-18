import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { orgApi } from '@/api/client';
import { useOrgId } from '@/hooks/useOrg';

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
              </tr>
            </thead>
            <tbody>
              {(data?.items ?? []).map((c) => (
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
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
