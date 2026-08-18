import { useQuery } from '@tanstack/react-query';
import { adminApi } from '@/api/client';

export function AdminTokensPage() {
  const { data } = useQuery({ queryKey: ['tokens'], queryFn: adminApi.listTokens });
  return (
    <div className='space-y-4'>
      <h1 className='text-xl font-semibold'>Agent Tokens</h1>
      <div className='rounded-lg border border-zinc-800 overflow-hidden'>
        <table className='w-full text-sm'>
          <thead className='bg-zinc-900 text-zinc-400'>
            <tr>
              <th className='px-4 py-3 text-left font-medium'>Name</th>
              <th className='px-4 py-3 text-left font-medium'>Status</th>
              <th className='px-4 py-3 text-left font-medium'>Created by</th>
            </tr>
          </thead>
          <tbody>
            {(data?.items ?? []).map((t) => (
              <tr key={t.id} className='border-t border-zinc-800 hover:bg-zinc-900/60'>
                <td className='px-4 py-2.5'>{t.name}</td>
                <td className='px-4 py-2.5'>
                  <span
                    className={`text-xs font-medium ${t.status === 'active' ? 'text-emerald-500' : 'text-zinc-500'}`}
                  >
                    {t.status}
                  </span>
                </td>
                <td className='px-4 py-2.5 text-zinc-400'>{t.created_by}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
