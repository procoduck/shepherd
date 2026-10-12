import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import { useMe } from '@/hooks/useMe';
import { useOrg } from '@/hooks/useOrg';

/**
 * Overview tiles. Collectors and clusters are both derived from a single
 * ListCollectors call (clusters as the distinct cluster_id count across
 * that org's collectors) so the page issues exactly one org-scoped request
 * per tile that needs one — no per-cluster or per-pipeline fan-out. This
 * undercounts clusters claimed by the org with zero reporting collectors:
 * the exact figure lives behind AdminService.ListClusters, which is
 * app-admin-only and unusable from a page every org member can load.
 *
 * Each tile names what it counts across (#298): the organisations tile is
 * every org the user belongs to, the rest are the selected org only.
 */
export function OverviewPage() {
  const { data: me } = useMe();
  const { orgId, orgs } = useOrg();
  const orgCount = me?.orgs?.length ?? 0;
  const current = orgs.find((o) => o.id === orgId);
  const inOrg = current ? `In ${current.displayName || current.name}` : 'No organisation selected';

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

  const stats: Array<{ label: string; scope: string; value: string; loading: boolean }> = [
    {
      label: 'Your organisations',
      scope: 'Every organisation you belong to',
      value: String(orgCount),
      loading: false,
    },
    {
      label: 'Collectors',
      scope: inOrg,
      value: String(collectorsQuery.data?.total ?? collectorsQuery.data?.items?.length ?? 0),
      loading: collectorsQuery.isLoading,
    },
    {
      label: 'Active pipelines',
      scope: inOrg,
      value: String(activePipelineCount),
      loading: pipelinesQuery.isLoading,
    },
    {
      label: 'Clusters',
      // Derived from collectors (see above), so a claimed cluster with none
      // reporting is not counted — /admin/clusters lists every claim.
      scope: current ? `${inOrg}, with a reporting collector` : inOrg,
      value: String(clusterCount),
      loading: collectorsQuery.isLoading,
    },
  ];

  return (
    <div className='space-y-6'>
      <h1 className='text-xl font-semibold'>Overview</h1>
      <div className='grid grid-cols-4 gap-4'>
        {stats.map(({ label, scope, value, loading }) => (
          <div key={label} className='rounded-lg border border-border bg-card p-4'>
            <p className='text-xs text-muted'>{label}</p>
            <p className='text-2xl font-semibold tabular-nums mt-1'>
              {loading ? <span className='text-muted-3'>…</span> : value}
            </p>
            <p className='mt-1 text-2xs text-muted-3' data-testid='overview-tile-scope'>
              {scope}
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
