import { useMe } from '@/hooks/useMe';

export function OverviewPage() {
  const { data: me } = useMe();
  const orgCount = me?.orgs?.length ?? 0;
  const stats = [
    { label: 'Orgs', value: orgCount > 0 ? String(orgCount) : '—' },
    { label: 'Collectors', value: '—' },
    { label: 'Active pipelines', value: '—' },
    { label: 'Clusters', value: '—' },
  ];

  return (
    <div className='space-y-6'>
      <h1 className='text-xl font-semibold'>Overview</h1>
      <div className='grid grid-cols-4 gap-4'>
        {stats.map(({ label, value }) => (
          <div key={label} className='rounded-lg border border-zinc-800 bg-zinc-900 p-4'>
            <p className='text-xs text-zinc-400'>{label}</p>
            <p className='text-2xl font-semibold tabular-nums mt-1'>{value}</p>
          </div>
        ))}
      </div>
      {me?.orgs?.length === 0 && (
        <p className='text-sm text-zinc-500'>No organisations assigned to your account.</p>
      )}
    </div>
  );
}
