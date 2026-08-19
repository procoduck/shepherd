import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import type { Cluster } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { useMe } from '@/hooks/useMe';

export function AdminClustersPage() {
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const qc = useQueryClient();

  const [unclaimedOnly, setUnclaimedOnly] = useState(false);
  const [claimCluster, setClaimCluster] = useState<Cluster | null>(null);
  const [claimOrgId, setClaimOrgId] = useState('');
  const [unclaimCluster, setUnclaimCluster] = useState<Cluster | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['clusters', unclaimedOnly],
    queryFn: () => clients.admin.listClusters({ unclaimed: unclaimedOnly }),
  });

  const { data: orgsData } = useQuery({
    queryKey: ['orgs'],
    queryFn: () => clients.admin.listOrgs({}),
    enabled: isAppAdmin,
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ['clusters'] });

  const claimMut = useMutation({
    mutationFn: () =>
      clients.admin.claimCluster({ cluster: claimCluster?.name ?? '', orgId: claimOrgId }),
    onSuccess: () => {
      toast.success('Cluster claimed');
      invalidate();
      setClaimCluster(null);
      setClaimOrgId('');
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to claim cluster'),
  });

  const unclaimMut = useMutation({
    mutationFn: () => clients.admin.unclaimCluster({ cluster: unclaimCluster?.name ?? '' }),
    onSuccess: () => {
      toast.success('Cluster unclaimed');
      invalidate();
      setUnclaimCluster(null);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to unclaim cluster'),
  });

  function orgLabel(orgId: string): string {
    const o = orgsData?.items.find((org) => org.id === orgId);
    return o ? o.displayName || o.name : orgId;
  }

  return (
    <div className='space-y-4'>
      <div className='flex items-center justify-between'>
        <h1 className='text-xl font-semibold'>Clusters</h1>
        <label className='flex items-center gap-2 text-xs text-muted'>
          <input
            type='checkbox'
            checked={unclaimedOnly}
            onChange={(e) => setUnclaimedOnly(e.target.checked)}
            className='rounded border-border-strong'
          />
          Unclaimed only
        </label>
      </div>

      {isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>
            {unclaimedOnly ? 'No unclaimed clusters.' : 'No clusters yet.'}
          </p>
        </div>
      ) : (
        <div className='rounded-lg border border-border overflow-hidden'>
          <table className='w-full text-sm'>
            <thead className='bg-card text-muted'>
              <tr>
                <th className='px-4 py-3 text-left font-medium'>Cluster</th>
                <th className='px-4 py-3 text-left font-medium'>Org</th>
                {isAppAdmin && <th className='px-4 py-3' />}
              </tr>
            </thead>
            <tbody>
              {(data?.items ?? []).map((c) => (
                <tr key={c.id} className='border-t border-border hover:bg-card/60'>
                  <td className='px-4 py-2.5 font-mono text-xs'>{c.name}</td>
                  <td className='px-4 py-2.5 text-muted'>{c.orgId ? orgLabel(c.orgId) : '—'}</td>
                  {isAppAdmin && (
                    <td className='px-4 py-2.5 text-right'>
                      {c.orgId ? (
                        <button
                          onClick={() => setUnclaimCluster(c)}
                          className='text-xs text-muted hover:text-red-400'
                        >
                          Unclaim
                        </button>
                      ) : (
                        <button
                          onClick={() => {
                            setClaimCluster(c);
                            setClaimOrgId('');
                          }}
                          className='text-xs text-indigo-400 hover:text-indigo-300'
                        >
                          Claim
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {claimCluster && (
        <AdminModal title={`Claim ${claimCluster.name}`} onClose={() => setClaimCluster(null)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              claimMut.mutate();
            }}
            className='space-y-4'
          >
            <label className='block text-xs font-medium text-muted'>
              Organisation
              <select
                value={claimOrgId}
                onChange={(e) => setClaimOrgId(e.target.value)}
                required
                className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
              >
                <option value='' disabled>
                  Select an organisation…
                </option>
                {(orgsData?.items ?? []).map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.displayName || o.name}
                  </option>
                ))}
              </select>
            </label>
            <AdminModalActions
              onCancel={() => setClaimCluster(null)}
              submitLabel='Claim'
              pendingLabel='Claiming…'
              pending={claimMut.isPending}
            />
          </form>
        </AdminModal>
      )}

      {unclaimCluster && (
        <AdminConfirmDialog
          title={`Unclaim ${unclaimCluster.name}?`}
          body='The cluster will be released back to the unclaimed pool and will stop receiving org-scoped config until claimed again.'
          confirmLabel='Unclaim'
          pendingLabel='Unclaiming…'
          pending={unclaimMut.isPending}
          onConfirm={() => unclaimMut.mutate()}
          onCancel={() => setUnclaimCluster(null)}
        />
      )}
    </div>
  );
}
