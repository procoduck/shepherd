import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';

export function AdminClustersPage() {
  const { data } = useQuery({
    queryKey: ['clusters'],
    queryFn: () => clients.admin.listClusters({ unclaimed: true }),
  });
  return (
    <div className='space-y-4'>
      <h1 className='text-xl font-semibold'>Clusters</h1>
      <div className='rounded-lg border border-zinc-800 overflow-hidden'>
        <table className='w-full text-sm'>
          <thead className='bg-zinc-900 text-zinc-400'>
            <tr>
              <th className='px-4 py-3 text-left font-medium'>Cluster</th>
              <th className='px-4 py-3 text-left font-medium'>Org</th>
            </tr>
          </thead>
          <tbody>
            {(data?.items ?? []).map((c) => (
              <tr key={c.id} className='border-t border-zinc-800 hover:bg-zinc-900/60'>
                <td className='px-4 py-2.5 font-mono text-xs'>{c.name}</td>
                <td className='px-4 py-2.5 text-zinc-400'>{c.orgId || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
