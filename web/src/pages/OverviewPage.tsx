import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import { useMe } from '@/hooks/useMe';
import { useOrgId } from '@/hooks/useOrg';

/**
 * Overview tiles. Collectors and clusters are both derived from a single
 * ListCollectors call (clusters as the distinct cluster_id count across
 * that org's collectors) so the page issues exactly one org-scoped request
 * per tile that needs one — no per-cluster or per-pipeline fan-out. This
 * undercounts clusters claimed by the org with zero reporting collectors:
 * the exact figure lives behind AdminService.ListClusters, which is
 * app-admin-only and unusable from a page every org member can load.
 */
export function OverviewPage() {
  const { data: me } = useMe();
  const orgId = useOrgId();
  const orgCount = me?.orgs?.length ?? 0;

  const collectorsQuery = useQuery({
    queryKey: ['collectors', orgId],
    queryFn: () => clients.fleet.listCollectors({ orgId }),
    enabled: !!orgId,
  });

  const pipelinesQuery = useQuery({
    queryKey: ['pipelines', orgId],
    queryFn: () => clients.pipeline.listPipelines({ orgId }),
    enabled: !!orgId,
  });

  const clusterCount = new Set(
    (collectorsQuery.data?.items ?? []).map((c) => c.clusterId).filter(Boolean),
  ).size;
  const activePipelineCount = (pipelinesQuery.data?.items ?? []).filter((p) => p.enabled).length;

  const stats: Array<{ label: string; value: string; loading: boolean }> = [
    { label: 'Orgs', value: String(orgCount), loading: false },
    {
      label: 'Collectors',
      value: String(collectorsQuery.data?.total ?? collectorsQuery.data?.items?.length ?? 0),
      loading: collectorsQuery.isLoading,
    },
    {
      label: 'Active pipelines',
      value: String(activePipelineCount),
      loading: pipelinesQuery.isLoading,
    },
    { label: 'Clusters', value: String(clusterCount), loading: collectorsQuery.isLoading },
  ];

  return (
    <div className='space-y-6'>
      <h1 className='text-xl font-semibold'>Overview</h1>
      <div className='grid grid-cols-4 gap-4'>
        {stats.map(({ label, value, loading }) => (
          <div key={label} className='rounded-lg border border-border bg-card p-4'>
            <p className='text-xs text-muted'>{label}</p>
            <p className='text-2xl font-semibold tabular-nums mt-1'>
              {loading ? <span className='text-muted-3'>…</span> : value}
            </p>
          </div>
        ))}
      </div>
      {me?.orgs?.length === 0 && (
        <p className='text-sm text-muted-2'>No organisations assigned to your account.</p>
      )}
    </div>
  );
}
