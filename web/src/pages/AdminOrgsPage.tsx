import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';

export function AdminOrgsPage() {
  const { data } = useQuery({
    queryKey: ['orgs'],
    queryFn: () => clients.admin.listOrgs({}),
  });
  return (
    <div className='space-y-4'>
      <h1 className='text-xl font-semibold'>Organisations</h1>
      <div className='rounded-lg border border-zinc-800 overflow-hidden'>
        <table className='w-full text-sm'>
          <thead className='bg-zinc-900 text-zinc-400'>
            <tr>
              <th className='px-4 py-3 text-left font-medium'>Name</th>
              <th className='px-4 py-3 text-left font-medium'>Display name</th>
            </tr>
          </thead>
          <tbody>
            {(data?.items ?? []).map((o) => (
              <tr key={o.id} className='border-t border-zinc-800 hover:bg-zinc-900/60'>
                <td className='px-4 py-2.5 font-mono text-xs'>{o.name}</td>
                <td className='px-4 py-2.5'>{o.displayName}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
