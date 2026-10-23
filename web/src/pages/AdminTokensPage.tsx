import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, Copy, Plus } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import {
  AllowlistFields,
  copyText,
  OrgField,
  tokenColumns,
} from '@/components/admin/agentTokenParts';
import { QueryError } from '@/components/QueryError';
import { DataTable } from '@/components/ui/DataTable';
import { Field, Input } from '@/components/ui/Field';
import type { AgentToken } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { useMe } from '@/hooks/useMe';
import { useOrg } from '@/hooks/useOrg';
import { bindableOrgs, isUnbound, sortTokens, splitAllowlist, tokenScope } from '@/lib/agentTokens';
import { formError, formErrors } from '@/lib/formError';

export function AdminTokensPage() {
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const { orgId, orgs: sessionOrgs } = useOrg();
  const scope = tokenScope(isAppAdmin, orgId);
  const qc = useQueryClient();

  const [showCreate, setShowCreate] = useState(false);
  const [name, setName] = useState('');
  const [createOrg, setCreateOrg] = useState('');
  const [clusters, setClusters] = useState('');
  const [roles, setRoles] = useState('');
  // The plaintext secret is held only in this component's own state, set
  // once from the CreateAgentToken response and never re-derived from any
  // query/cache — the server never returns it again, so this is the only
  // place it can ever live. Cleared on dialog close; never logged. `id` is
  // not secret (it's also shown in the table below) — it rides along so the
  // dialog can show it as the "remotecfg username" the agent authenticates
  // with, right above the secret it pairs with.
  const [newSecret, setNewSecret] = useState<{ id: string; name: string; secret: string } | null>(
    null,
  );
  const [copied, setCopied] = useState(false);
  const [revokeToken, setRevokeToken] = useState<AgentToken | null>(null);
  const [bindToken, setBindToken] = useState<AgentToken | null>(null);
  const [bindOrg, setBindOrg] = useState('');
  const [bindClusters, setBindClusters] = useState('');
  const [bindRoles, setBindRoles] = useState('');

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['tokens', scope],
    queryFn: () => clients.admin.listAgentTokens({ orgId: scope }),
    // An org admin's list needs the org in view; wait for it.
    enabled: isAppAdmin || orgId !== '',
  });

  // Every organisation, for an app admin's org picker (an org admin binds only
  // to the org in view, which their session already names).
  const orgsQuery = useQuery({
    queryKey: ['orgs'],
    queryFn: () => clients.admin.listOrgs({}),
    enabled: isAppAdmin && (showCreate || bindToken !== null),
  });
  const orgOptions = bindableOrgs(isAppAdmin, orgId, sessionOrgs, orgsQuery.data?.items ?? []);

  const invalidate = () => qc.invalidateQueries({ queryKey: ['tokens'] });

  const openCreate = () => {
    setCreateOrg(orgId);
    setShowCreate(true);
  };

  const createMut = useMutation({
    mutationFn: () =>
      clients.admin.createAgentToken({
        name,
        orgId: isAppAdmin ? createOrg : orgId,
        clusters: splitAllowlist(clusters),
        roles: splitAllowlist(roles),
      }),
    onSuccess: (resp) => {
      invalidate();
      setShowCreate(false);
      setName('');
      setClusters('');
      setRoles('');
      setNewSecret({ id: resp.id, name: resp.name, secret: resp.secret });
      setCopied(false);
    },
  });
  const createErrors = formErrors(createMut.error, 'Failed to create token', { roles: 'role ' });

  const revokeMut = useMutation({
    mutationFn: () => clients.admin.revokeAgentToken({ id: revokeToken?.id ?? '', orgId: scope }),
    onSuccess: () => {
      toast.success('Token revoked');
      invalidate();
      setRevokeToken(null);
    },
  });

  const openBind = (t: AgentToken) => {
    setBindToken(t);
    setBindOrg(t.orgId || orgId);
    setBindClusters(t.clusters.join(', '));
    setBindRoles(t.roles.join(', '));
  };
  const bindMut = useMutation({
    mutationFn: () =>
      clients.admin.bindAgentToken({
        id: bindToken?.id ?? '',
        orgId: bindOrg,
        clusters: splitAllowlist(bindClusters),
        roles: splitAllowlist(bindRoles),
      }),
    onSuccess: (tok) => {
      toast.success(`Token bound to ${tok.orgName}`);
      invalidate();
      setBindToken(null);
    },
  });
  const bindErrors = formErrors(bindMut.error, 'Failed to bind token', { roles: 'role ' });
  const closeBind = () => {
    setBindToken(null);
    bindMut.reset();
  };

  const closeCreate = () => {
    setShowCreate(false);
    createMut.reset();
  };

  async function copySecret() {
    if (!newSecret) return;
    try {
      await navigator.clipboard.writeText(newSecret.secret);
      setCopied(true);
    } catch {
      toast.error('Copy failed — select and copy the secret manually');
    }
  }

  const rows = sortTokens(data?.items ?? []);
  const unboundCount = rows.filter((t) => isUnbound(t) && t.status === 'active').length;

  return (
    <div className='space-y-4'>
      <div className='flex items-center justify-between'>
        <div>
          <h1 className='text-xl font-semibold'>Agent Tokens</h1>
          <p className='mt-1 text-sm text-muted'>
            {isAppAdmin
              ? 'Every agent token, across all organisations. Each token is bound to an organisation, optionally narrowed to clusters and roles.'
              : 'Agent tokens bound to this organisation, optionally narrowed to clusters and roles.'}
          </p>
        </div>
        <button
          onClick={openCreate}
          className='flex shrink-0 items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
        >
          <Plus size={14} /> New token
        </button>
      </div>

      {isAppAdmin && unboundCount > 0 && (
        <div
          role='status'
          data-testid='tokens-unbound-notice'
          className='rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-300'
        >
          {unboundCount === 1 ? '1 token is' : `${unboundCount} tokens are`} not bound to an
          organisation. Collectors using an unbound token are refused until it is bound — binding
          keeps the secret, so they need no new credentials.
        </div>
      )}

      {isError ? (
        <QueryError error={error} noun='agent tokens' />
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : rows.length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>No agent tokens yet.</p>
        </div>
      ) : (
        <DataTable
          columns={tokenColumns(isAppAdmin, setRevokeToken, openBind)}
          rows={rows}
          rowKey={(t) => t.id}
        />
      )}

      {showCreate && (
        <AdminModal title='New agent token' onClose={closeCreate}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createMut.mutate();
            }}
            className='space-y-4'
          >
            <Field label='Name'>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                placeholder='prod-eu-1-agent'
              />
            </Field>
            <OrgField
              isAppAdmin={isAppAdmin}
              options={orgOptions}
              value={createOrg}
              onChange={setCreateOrg}
              loading={orgsQuery.isLoading}
            />
            <AllowlistFields
              clusters={clusters}
              setClusters={setClusters}
              roles={roles}
              setRoles={setRoles}
              rolesError={createErrors.field.roles}
            />
            <AdminModalActions
              onCancel={closeCreate}
              error={createErrors.form}
              submitLabel='Create'
              pendingLabel='Creating…'
              pending={createMut.isPending}
            />
          </form>
        </AdminModal>
      )}

      {bindToken && (
        <AdminModal
          title={`${isUnbound(bindToken) ? 'Bind' : 'Rebind'} ${bindToken.name}`}
          onClose={closeBind}
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              bindMut.mutate();
            }}
            className='space-y-4'
          >
            <p className='text-sm text-muted'>
              The secret does not change: collectors using this token keep their credentials and are
              served the bound organisation&rsquo;s config from their next poll.
            </p>
            <OrgField
              isAppAdmin={isAppAdmin}
              options={orgOptions}
              value={bindOrg}
              onChange={setBindOrg}
              loading={orgsQuery.isLoading}
            />
            <AllowlistFields
              clusters={bindClusters}
              setClusters={setBindClusters}
              roles={bindRoles}
              setRoles={setBindRoles}
              rolesError={bindErrors.field.roles}
            />
            <AdminModalActions
              onCancel={closeBind}
              error={bindErrors.form}
              submitLabel='Bind'
              pendingLabel='Binding…'
              pending={bindMut.isPending}
              submitTestId='token-bind-submit'
            />
          </form>
        </AdminModal>
      )}

      {newSecret && (
        <AdminModal
          title='Token created'
          onClose={() => {
            setNewSecret(null);
            setCopied(false);
          }}
        >
          <div className='space-y-4'>
            <p className='text-sm text-muted'>
              This is the only time{' '}
              <span className='font-medium text-zinc-200'>{newSecret.name}</span>'s secret will be
              shown. Copy it now — it cannot be retrieved again.
            </p>
            <div className='space-y-1'>
              <p className='text-xs text-muted-2'>remotecfg username</p>
              <div className='flex items-center gap-2 rounded-md border border-border-strong bg-card px-3 py-2'>
                <code className='flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs select-all'>
                  {newSecret.id}
                </code>
                <button
                  type='button'
                  onClick={() => copyText(newSecret.id)}
                  aria-label='Copy token ID'
                  className='shrink-0 text-muted-3 hover:text-indigo-400'
                >
                  <Copy size={14} />
                </button>
              </div>
            </div>
            <div className='flex items-center gap-2 rounded-md border border-border-strong bg-card px-3 py-2'>
              <code className='flex-1 overflow-x-auto whitespace-nowrap font-mono text-xs select-all'>
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

      {revokeToken && (
        <AdminConfirmDialog
          title={`Revoke ${revokeToken.name}?`}
          body='Any agent still using this token will be rejected on its next poll. This cannot be undone.'
          confirmLabel='Revoke'
          pendingLabel='Revoking…'
          pending={revokeMut.isPending}
          onConfirm={() => revokeMut.mutate()}
          onCancel={() => {
            setRevokeToken(null);
            revokeMut.reset();
          }}
          error={formError(revokeMut.error, 'Failed to revoke token')}
        />
      )}
    </div>
  );
}
