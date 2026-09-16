import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input } from '@/components/ui/Field';
import type { AgentIdentity } from '@/gen/shepherd/mgmt/v1/admin_pb';

// A comma/space-separated allowlist, shown as chips or an em dash for "any".
function AllowlistCell({ values }: { values: string[] }) {
  if (values.length === 0) {
    return <span className='text-muted-3'>any</span>;
  }
  return (
    <span className='flex flex-wrap gap-1'>
      {values.map((v) => (
        <code key={v} className='rounded bg-border px-1.5 py-0.5 text-xs'>
          {v}
        </code>
      ))}
    </span>
  );
}

function splitList(s: string): string[] {
  return s
    .split(/[,\s]+/)
    .map((v) => v.trim())
    .filter(Boolean);
}

function bindingColumns(
  isAppAdmin: boolean,
  setDelete: (b: AgentIdentity) => void,
): DataTableColumn<AgentIdentity>[] {
  const cols: DataTableColumn<AgentIdentity>[] = [
    {
      key: 'app',
      header: 'App ID',
      cellClassName: 'px-4 py-2.5 font-mono text-xs',
      render: (b) => b.appId,
    },
    {
      key: 'issuer',
      header: 'Issuer',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (b) => b.issuer,
    },
    { key: 'org', header: 'Organisation', render: (b) => b.orgName },
    { key: 'clusters', header: 'Clusters', render: (b) => <AllowlistCell values={b.clusters} /> },
    { key: 'roles', header: 'Roles', render: (b) => <AllowlistCell values={b.roles} /> },
  ];
  if (isAppAdmin) {
    cols.push({
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (b) => (
        <button
          type='button'
          onClick={() => setDelete(b)}
          aria-label={`Delete binding for ${b.appId}`}
          className='text-muted-3 hover:text-red-400'
          data-testid={`binding-delete-${b.appId}`}
        >
          <Trash2 size={14} />
        </button>
      ),
    });
  }
  return cols;
}

// CollectorBindingsSection lists and manages the collector-OIDC identity
// bindings (agent_identities): which token identity (issuer + app id) maps to
// which organisation. The IdP is never authoritative for org membership — the
// mapping lives here. Mirrors AdminTokensPage's list/create/delete shape.
export function CollectorBindingsSection({ isAppAdmin }: { isAppAdmin: boolean }) {
  const qc = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [issuer, setIssuer] = useState('');
  const [appId, setAppId] = useState('');
  const [org, setOrg] = useState('');
  const [clusters, setClusters] = useState('');
  const [roles, setRoles] = useState('');
  const [toDelete, setToDelete] = useState<AgentIdentity | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['agent-identities'],
    queryFn: () => clients.admin.listAgentIdentities({}),
  });
  const invalidate = () => qc.invalidateQueries({ queryKey: ['agent-identities'] });

  const createMut = useMutation({
    mutationFn: () =>
      clients.admin.createAgentIdentity({
        issuer: issuer.trim(),
        appId: appId.trim(),
        org: org.trim(),
        clusters: splitList(clusters),
        roles: splitList(roles),
      }),
    onSuccess: () => {
      invalidate();
      setShowCreate(false);
      setIssuer('');
      setAppId('');
      setOrg('');
      setClusters('');
      setRoles('');
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to create binding'),
  });

  const deleteMut = useMutation({
    mutationFn: () =>
      clients.admin.deleteAgentIdentity({
        issuer: toDelete?.issuer ?? '',
        appId: toDelete?.appId ?? '',
      }),
    onSuccess: () => {
      toast.success('Binding removed');
      invalidate();
      setToDelete(null);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to remove binding'),
  });

  return (
    <section className='space-y-3' data-testid='collector-bindings'>
      <div className='flex items-start justify-between gap-4'>
        <div>
          <h2 className='text-base font-semibold'>Collector identity bindings</h2>
          <p className='mt-1 text-sm text-muted'>
            Map a collector&rsquo;s OIDC identity (issuer and app ID) to an organisation. A
            collector authenticating with an access token is served that organisation&rsquo;s
            config.
          </p>
        </div>
        {isAppAdmin && (
          <button
            onClick={() => setShowCreate(true)}
            data-testid='binding-new'
            className='flex shrink-0 items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New binding
          </button>
        )}
      </div>

      {isError ? (
        <QueryError error={error} noun='collector bindings' />
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-6 text-center'>
          <p className='text-sm text-muted'>No collector identity bindings yet.</p>
        </div>
      ) : (
        <DataTable
          columns={bindingColumns(isAppAdmin, setToDelete)}
          rows={data?.items ?? []}
          rowKey={(b) => `${b.issuer}|${b.appId}`}
        />
      )}

      {showCreate && (
        <AdminModal title='New collector binding' onClose={() => setShowCreate(false)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createMut.mutate();
            }}
            className='space-y-4'
          >
            <Field label='Issuer' hint='The token issuer, exactly as it appears in `iss`.'>
              <Input
                value={issuer}
                onChange={(e) => setIssuer(e.target.value)}
                data-testid='binding-issuer'
              />
            </Field>
            <Field label='App ID' hint='The client identity claim (sub / azp / client_id).'>
              <Input
                value={appId}
                onChange={(e) => setAppId(e.target.value)}
                data-testid='binding-app-id'
              />
            </Field>
            <Field label='Organisation' hint='The organisation slug to bind to.'>
              <Input
                value={org}
                onChange={(e) => setOrg(e.target.value)}
                data-testid='binding-org'
              />
            </Field>
            <Field
              label='Clusters'
              optional
              hint='Comma-separated allowlist; empty means any cluster in the org.'
            >
              <Input
                value={clusters}
                onChange={(e) => setClusters(e.target.value)}
                data-testid='binding-clusters'
              />
            </Field>
            <Field label='Roles' optional hint='Comma-separated allowlist; empty means any role.'>
              <Input
                value={roles}
                onChange={(e) => setRoles(e.target.value)}
                data-testid='binding-roles'
              />
            </Field>
            <AdminModalActions
              onCancel={() => setShowCreate(false)}
              submitLabel='Create binding'
              pendingLabel='Creating…'
              pending={createMut.isPending}
              submitTestId='binding-submit'
            />
          </form>
        </AdminModal>
      )}

      {toDelete && (
        <AdminConfirmDialog
          title='Remove collector binding'
          body={`Remove the binding for ${toDelete.appId}? Collectors using this identity will fall back to being served empty config until an admin claims their cluster.`}
          confirmLabel='Remove'
          pendingLabel='Removing…'
          pending={deleteMut.isPending}
          onConfirm={() => deleteMut.mutate()}
          onCancel={() => setToDelete(null)}
        />
      )}
    </section>
  );
}
