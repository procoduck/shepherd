import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, Copy, Plus } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import type { AgentToken } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { useMe } from '@/hooks/useMe';

export function AdminTokensPage() {
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const qc = useQueryClient();

  const [showCreate, setShowCreate] = useState(false);
  const [name, setName] = useState('');
  // The plaintext secret is held only in this component's own state, set
  // once from the CreateAgentToken response and never re-derived from any
  // query/cache — the server never returns it again, so this is the only
  // place it can ever live. Cleared on dialog close; never logged.
  const [newSecret, setNewSecret] = useState<{ name: string; secret: string } | null>(null);
  const [copied, setCopied] = useState(false);
  const [revokeToken, setRevokeToken] = useState<AgentToken | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['tokens'],
    queryFn: () => clients.admin.listAgentTokens({}),
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ['tokens'] });

  const createMut = useMutation({
    mutationFn: () => clients.admin.createAgentToken({ name }),
    onSuccess: (resp) => {
      invalidate();
      setShowCreate(false);
      setName('');
      setNewSecret({ name: resp.name, secret: resp.secret });
      setCopied(false);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to create token'),
  });

  const revokeMut = useMutation({
    mutationFn: () => clients.admin.revokeAgentToken({ id: revokeToken?.id ?? '' }),
    onSuccess: () => {
      toast.success('Token revoked');
      invalidate();
      setRevokeToken(null);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to revoke token'),
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
    <div className='space-y-4'>
      <div className='flex items-center justify-between'>
        <h1 className='text-xl font-semibold'>Agent Tokens</h1>
        {isAppAdmin && (
          <button
            onClick={() => setShowCreate(true)}
            className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New token
          </button>
        )}
      </div>

      {isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>No agent tokens yet.</p>
        </div>
      ) : (
        <div className='rounded-lg border border-border overflow-hidden'>
          <table className='w-full text-sm'>
            <thead className='bg-card text-muted'>
              <tr>
                <th className='px-4 py-3 text-left font-medium'>Name</th>
                <th className='px-4 py-3 text-left font-medium'>Status</th>
                <th className='px-4 py-3 text-left font-medium'>Created by</th>
                {isAppAdmin && <th className='px-4 py-3' />}
              </tr>
            </thead>
            <tbody>
              {(data?.items ?? []).map((t) => (
                <tr key={t.id} className='border-t border-border hover:bg-card/60'>
                  <td className='px-4 py-2.5'>{t.name}</td>
                  <td className='px-4 py-2.5'>
                    <span
                      className={`text-xs font-medium ${t.status === 'active' ? 'text-emerald-500' : 'text-muted-2'}`}
                    >
                      {t.status}
                    </span>
                  </td>
                  <td className='px-4 py-2.5 text-muted'>{t.createdBy}</td>
                  {isAppAdmin && (
                    <td className='px-4 py-2.5 text-right'>
                      {t.status === 'active' && (
                        <button
                          onClick={() => setRevokeToken(t)}
                          className='text-xs text-muted hover:text-red-400'
                        >
                          Revoke
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

      {showCreate && (
        <AdminModal title='New agent token' onClose={() => setShowCreate(false)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createMut.mutate();
            }}
            className='space-y-4'
          >
            <label className='block text-xs font-medium text-muted'>
              Name
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
                placeholder='prod-eu-1-agent'
              />
            </label>
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
          onCancel={() => setRevokeToken(null)}
        />
      )}
    </div>
  );
}
