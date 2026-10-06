import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Select } from '@/components/ui/Field';
import type { Cluster } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { useMe } from '@/hooks/useMe';
import { formError } from '@/lib/formError';

function clusterColumns(
  isAppAdmin: boolean,
  orgLabel: (orgId: string) => string,
  onUnclaim: (c: Cluster) => void,
  onClaim: (c: Cluster) => void,
): DataTableColumn<Cluster>[] {
  const cols: DataTableColumn<Cluster>[] = [
    {
      key: 'name',
      header: 'Cluster',
      cellClassName: 'px-4 py-2.5 font-mono text-xs',
      render: (c) => c.name,
    },
    {
      key: 'org',
      header: 'Org',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (c) => (c.orgId ? orgLabel(c.orgId) : '—'),
    },
  ];
  if (isAppAdmin) {
    cols.push({
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (c) =>
        c.orgId ? (
          <button onClick={() => onUnclaim(c)} className='text-xs text-muted hover:text-red-400'>
            Unclaim
          </button>
        ) : (
          <button
            onClick={() => onClaim(c)}
            className='text-xs text-indigo-400 hover:text-indigo-300'
          >
            Claim
          </button>
        ),
    });
  }
  return cols;
}

export function AdminClustersPage() {
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const qc = useQueryClient();

  const [unclaimedOnly, setUnclaimedOnly] = useState(false);
  const [claimCluster, setClaimCluster] = useState<Cluster | null>(null);
  const [claimOrgId, setClaimOrgId] = useState('');
  const [unclaimCluster, setUnclaimCluster] = useState<Cluster | null>(null);

  const { data, isLoading, isError, error } = useQuery({
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
  });

  const unclaimMut = useMutation({
    mutationFn: () => clients.admin.unclaimCluster({ cluster: unclaimCluster?.name ?? '' }),
    onSuccess: () => {
      toast.success('Cluster unclaimed');
      invalidate();
      setUnclaimCluster(null);
    },
  });
  const closeClaim = () => {
    setClaimCluster(null);
    claimMut.reset();
  };
  const closeUnclaim = () => {
    setUnclaimCluster(null);
    unclaimMut.reset();
  };

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

      {isError ? (
        <QueryError error={error} noun='clusters' />
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>
            {unclaimedOnly ? 'No unclaimed clusters.' : 'No clusters yet.'}
          </p>
        </div>
      ) : (
        <DataTable
          columns={clusterColumns(isAppAdmin, orgLabel, setUnclaimCluster, (c) => {
            setClaimCluster(c);
            setClaimOrgId('');
          })}
          rows={data?.items ?? []}
          rowKey={(c) => c.id}
        />
      )}

      {claimCluster && (
        <AdminModal title={`Claim ${claimCluster.name}`} onClose={closeClaim}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              claimMut.mutate();
            }}
            className='space-y-4'
          >
            <Field label='Organisation'>
              <Select value={claimOrgId} onChange={(e) => setClaimOrgId(e.target.value)} required>
                <option value='' disabled>
                  Select an organisation…
                </option>
                {(orgsData?.items ?? []).map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.displayName || o.name}
                  </option>
                ))}
              </Select>
            </Field>
            <AdminModalActions
              onCancel={closeClaim}
              submitLabel='Claim'
              pendingLabel='Claiming…'
              pending={claimMut.isPending}
              error={formError(claimMut.error, 'Failed to claim cluster')}
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
          onCancel={closeUnclaim}
          error={formError(unclaimMut.error, 'Failed to unclaim cluster')}
        />
      )}
    </div>
  );
}
