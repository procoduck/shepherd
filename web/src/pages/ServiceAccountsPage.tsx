import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, Copy, Plus } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input, Select } from '@/components/ui/Field';
import type { ServiceAccount } from '@/gen/shepherd/mgmt/v1/service_account_pb';
import { useCanAdminister, useOrgId } from '@/hooks/useOrg';

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    toast.error('Copy failed — select and copy the value manually');
  }
}

// A capability/role pair rendered as two small chips: capability decides
// whether the credential may write once it reaches a procedure; role decides
// which procedures it may reach at all. They are orthogonal (see the proto).
function GrantCell({ account }: { account: ServiceAccount }) {
  return (
    <span className='flex flex-wrap gap-1'>
      <code className='rounded bg-border px-1.5 py-0.5 text-xs'>{account.capability}</code>
      <code className='rounded bg-border px-1.5 py-0.5 text-xs'>{account.role}</code>
    </span>
  );
}

function accountColumns(
  canAdminister: boolean,
  setRevoke: (a: ServiceAccount) => void,
): DataTableColumn<ServiceAccount>[] {
  const cols: DataTableColumn<ServiceAccount>[] = [
    { key: 'name', header: 'Name', render: (a) => a.name },
    {
      key: 'id',
      header: 'ID',
      cellClassName: 'px-4 py-2.5 font-mono text-xs',
      render: (a) => (
        <span className='inline-flex items-center gap-1.5'>
          <span className='select-all'>{a.id}</span>
          <button
            type='button'
            onClick={() => copyText(a.id)}
            aria-label='Copy service-account ID'
            className='shrink-0 text-muted-3 hover:text-indigo-400'
          >
            <Copy size={12} />
          </button>
        </span>
      ),
    },
    { key: 'grant', header: 'Grant', render: (a) => <GrantCell account={a} /> },
    {
      key: 'status',
      header: 'Status',
      render: (a) => (
        <span
          className={`text-xs font-medium ${a.status === 'active' ? 'text-emerald-500' : 'text-muted-2'}`}
        >
          {a.status}
        </span>
      ),
    },
    {
      key: 'createdBy',
      header: 'Created by',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (a) => a.createdBy,
    },
  ];
  if (canAdminister) {
    cols.push({
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (a) =>
        a.status === 'active' && (
          <button
            type='button'
            onClick={() => setRevoke(a)}
            data-testid={`sa-revoke-${a.name}`}
            className='text-xs text-muted hover:text-red-400'
          >
            Revoke
          </button>
        ),
    });
  }
  return cols;
}

const EMPTY_CREATE = { name: '', capability: 'propose', role: 'editor' };

/**
 * Service accounts (/service-accounts, org-scoped).
 *
 * Machine-caller identities for the management API: org-scoped Basic-auth
 * credentials, capability-scoped propose vs apply, at an editor or admin tier.
 * A service account never spans orgs and is never app-admin. Managing them
 * (list/create/revoke) is org-admin throughout (ServiceAccountService), so the
 * whole page is org-admin — there is no read-only view for a viewer.
 *
 * The secret is shown exactly once, on creation, mirroring agent tokens. There
 * is no "upgrade the grant" path: a capability or role change is minting a new
 * account and revoking the old one, so it always reads as two audited actions.
 */
export function ServiceAccountsPage() {
  const orgId = useOrgId();
  const canAdminister = useCanAdminister();
  const qc = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState(EMPTY_CREATE);
  const [newSecret, setNewSecret] = useState<{
    id: string;
    name: string;
    secret: string;
    capability: string;
    role: string;
  } | null>(null);
  const [copied, setCopied] = useState(false);
  const [toRevoke, setToRevoke] = useState<ServiceAccount | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['service-accounts', orgId],
    queryFn: () => clients.serviceAccount.listServiceAccounts({ orgId }),
    enabled: !!orgId,
  });
  const invalidate = () => qc.invalidateQueries({ queryKey: ['service-accounts', orgId] });

  const createMut = useMutation({
    mutationFn: () =>
      clients.serviceAccount.createServiceAccount({
        orgId,
        name: form.name.trim(),
        capability: form.capability,
        role: form.role,
      }),
    onSuccess: (resp) => {
      invalidate();
      setShowCreate(false);
      setForm(EMPTY_CREATE);
      setNewSecret({
        id: resp.id,
        name: resp.name,
        secret: resp.secret,
        capability: resp.capability,
        role: resp.role,
      });
      setCopied(false);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to create service account'),
  });

  const revokeMut = useMutation({
    mutationFn: () =>
      clients.serviceAccount.revokeServiceAccount({ orgId, id: toRevoke?.id ?? '' }),
    onSuccess: () => {
      toast.success('Service account revoked');
      invalidate();
      setToRevoke(null);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to revoke service account'),
  });

  async function copySecret() {
    if (!newSecret) return;
    try {
      await navigator.clipboard.writeText(newSecret.secret);
      setCopied(true);
    } catch {
      toast.error('Copy failed — select and copy the secret manually');
    }
  }

  return (
    <div className='space-y-4' data-testid='service-accounts'>
      <div className='flex items-start justify-between gap-4'>
        <div>
          <h1 className='text-xl font-semibold'>Service accounts</h1>
          <p className='mt-1 text-sm text-muted'>
            Machine credentials for the management API, scoped to this organisation. The grant is
            fixed at creation — to change it, revoke and mint a new one.
          </p>
        </div>
        {!!orgId && canAdminister && (
          <button
            onClick={() => setShowCreate(true)}
            data-testid='sa-new'
            className='flex shrink-0 items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New service account
          </button>
        )}
      </div>

      {!orgId ? (
        <p className='text-sm text-muted'>No organisation context.</p>
      ) : isError ? (
        <QueryError error={error} noun='service accounts' />
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>No service accounts yet.</p>
        </div>
      ) : (
        <DataTable
          columns={accountColumns(canAdminister, setToRevoke)}
          rows={data?.items ?? []}
          rowKey={(a) => a.id}
        />
      )}

      {showCreate && (
        <AdminModal title='New service account' onClose={() => setShowCreate(false)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createMut.mutate();
            }}
            className='space-y-4'
          >
            <Field label='Name'>
              <Input
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                required
                data-testid='sa-name'
                placeholder='ci-deployer'
              />
            </Field>
            <Field
              label='Capability'
              hint='Whether the credential may write once it reaches a procedure.'
            >
              <Select
                value={form.capability}
                onChange={(e) => setForm((f) => ({ ...f, capability: e.target.value }))}
                data-testid='sa-capability'
              >
                <option value='propose'>Propose (read + propose changes)</option>
                <option value='apply'>Apply (write directly)</option>
              </Select>
            </Field>
            <Field label='Role' hint='Which procedures the credential may reach at all.'>
              <Select
                value={form.role}
                onChange={(e) => setForm((f) => ({ ...f, role: e.target.value }))}
                data-testid='sa-role'
              >
                <option value='editor'>Editor</option>
                <option value='admin'>Admin</option>
              </Select>
            </Field>
            <AdminModalActions
              onCancel={() => setShowCreate(false)}
              submitLabel='Create'
              pendingLabel='Creating…'
              pending={createMut.isPending}
            />
          </form>
        </AdminModal>
      )}

      {newSecret && (
        <AdminModal
          title='Service account created'
          onClose={() => {
            setNewSecret(null);
            setCopied(false);
          }}
        >
          <div className='space-y-4'>
            <p className='text-sm text-muted'>
              This is the only time{' '}
              <span className='font-medium text-zinc-200'>{newSecret.name}</span>&rsquo;s secret
              will be shown. Copy it now — it cannot be retrieved again. Grant:{' '}
              <code className='rounded bg-border px-1 py-0.5 text-xs'>{newSecret.capability}</code>{' '}
              <code className='rounded bg-border px-1 py-0.5 text-xs'>{newSecret.role}</code>.
            </p>
            <div className='space-y-1'>
              <p className='text-xs text-muted-2'>Basic-auth username</p>
              <div className='flex items-center gap-2 rounded-md border border-border-strong bg-card px-3 py-2'>
                <code className='flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs select-all'>
                  {newSecret.id}
                </code>
                <button
                  type='button'
                  onClick={() => copyText(newSecret.id)}
                  aria-label='Copy service-account ID'
                  className='shrink-0 text-muted-3 hover:text-indigo-400'
                >
                  <Copy size={14} />
                </button>
              </div>
            </div>
            <div className='space-y-1'>
              <p className='text-xs text-muted-2'>Secret</p>
              <div className='flex items-center gap-2 rounded-md border border-border-strong bg-card px-3 py-2'>
                <code
                  className='flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs select-all'
                  data-testid='sa-secret'
                >
                  {newSecret.secret}
                </code>
                <button
                  type='button'
                  onClick={copySecret}
                  aria-label='Copy secret'
                  className='shrink-0 text-muted-3 hover:text-indigo-400'
                >
                  {copied ? <Check size={14} className='text-emerald-500' /> : <Copy size={14} />}
                </button>
              </div>
            </div>
            <div className='flex justify-end pt-2'>
              <button
                type='button'
                onClick={() => {
                  setNewSecret(null);
                  setCopied(false);
                }}
                className='rounded-md bg-indigo-600 px-4 py-1.5 text-sm text-white hover:bg-indigo-500'
              >
                Done
              </button>
            </div>
          </div>
        </AdminModal>
      )}

      {toRevoke && (
        <AdminConfirmDialog
          title={`Revoke ${toRevoke.name}?`}
          body='Any machine still using this credential will be rejected on its next request. This cannot be undone.'
          confirmLabel='Revoke'
          pendingLabel='Revoking…'
          pending={revokeMut.isPending}
          onConfirm={() => revokeMut.mutate()}
          onCancel={() => setToRevoke(null)}
        />
      )}
    </div>
  );
}
