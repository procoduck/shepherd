import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import type { Destination } from '@/gen/shepherd/mgmt/v1/destination_pb';
import { useOrgId } from '@/hooks/useOrg';

export function DestinationsPage() {
  const orgId = useOrgId();
  const qc = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ name: '', type: 'prometheus', url: '', authMode: 'none' });
  const [urlError, setUrlError] = useState('');

  const { data, isLoading } = useQuery({
    queryKey: ['destinations', orgId],
    queryFn: () => clients.destination.listDestinations({ orgId }),
    enabled: !!orgId,
  });

  const createMut = useMutation({
    mutationFn: () =>
      clients.destination.createDestination({
        orgId,
        ...form,
        tenantId: '',
        secretName: '',
        secretNamespace: '',
      }),
    onSuccess: () => {
      toast.success('Destination created');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
      setShowCreate(false);
      setForm({ name: '', type: 'prometheus', url: '', authMode: 'none' });
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(err.message || 'Failed to create destination');
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => clients.destination.deleteDestination({ orgId, id }),
    onSuccess: () => {
      toast.success('Destination deleted');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(
        err.code === 'already_exists'
          ? `Cannot delete: ${err.message || 'referenced by a pipeline'}`
          : err.message || 'Failed to delete destination',
      );
    },
  });

  function validateUrl(url: string): boolean {
    try {
      new URL(url);
      setUrlError('');
      return true;
    } catch {
      setUrlError('Enter a valid URL (e.g. http://prometheus:9090)');
      return false;
    }
  }

  function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    if (validateUrl(form.url)) createMut.mutate();
  }

  return (
    <div className='space-y-4'>
      <div className='flex items-center justify-between'>
        <h1 className='text-xl font-semibold'>Destinations</h1>
        {!!orgId && (
          <button
            onClick={() => setShowCreate(true)}
            className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New destination
          </button>
        )}
      </div>

      {!orgId ? (
        <p className='text-sm text-muted'>No organisation context.</p>
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>No destinations yet.</p>
          <button
            onClick={() => setShowCreate(true)}
            className='mt-3 text-xs text-indigo-400 hover:text-indigo-300'
          >
            Add your first destination
          </button>
        </div>
      ) : (
        <div className='rounded-lg border border-border overflow-hidden'>
          <table className='w-full text-sm'>
            <thead className='bg-card text-muted'>
              <tr>
                <th className='px-4 py-3 text-left font-medium'>Name</th>
                <th className='px-4 py-3 text-left font-medium'>Type</th>
                <th className='px-4 py-3 text-left font-medium'>URL</th>
                <th className='px-4 py-3 text-left font-medium'>Auth</th>
                <th className='px-4 py-3' />
              </tr>
            </thead>
            <tbody>
              {(data?.items ?? []).map((d: Destination) => (
                <tr key={d.id} className='border-t border-border hover:bg-card/60'>
                  <td className='px-4 py-2.5 font-medium'>{d.name}</td>
                  <td className='px-4 py-2.5 text-muted'>{d.type}</td>
                  <td className='px-4 py-2.5 font-mono text-xs text-zinc-300'>
                    <a
                      href={d.url}
                      target='_blank'
                      rel='noreferrer'
                      className='flex items-center gap-1 hover:text-indigo-400'
                    >
                      {new URL(d.url).host}
                      <ExternalLink size={10} />
                    </a>
                  </td>
                  <td className='px-4 py-2.5 text-xs text-muted'>{d.authMode}</td>
                  <td className='px-4 py-2.5 text-right'>
                    <button
                      onClick={() => deleteMut.mutate(d.id)}
                      className='text-muted-3 transition-colors hover:text-red-400'
                      aria-label='Delete destination'
                    >
                      <Trash2 size={14} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {showCreate && (
        <div className='fixed inset-0 z-50 flex items-center justify-center bg-black/60'>
          <div className='w-full max-w-md rounded-xl border border-border bg-background p-6 shadow-2xl'>
            <h2 className='mb-4 text-base font-semibold'>New destination</h2>
            <form onSubmit={handleCreate} className='space-y-4'>
              <label className='block text-xs font-medium text-muted'>
                Name
                <input
                  value={form.name}
                  onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                  required
                  className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
                  placeholder='prom-prod'
                />
              </label>
              <label className='block text-xs font-medium text-muted'>
                Type
                <select
                  value={form.type}
                  onChange={(e) => setForm((f) => ({ ...f, type: e.target.value }))}
                  className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
                >
                  <option value='prometheus'>Prometheus</option>
                  <option value='loki'>Loki</option>
                  <option value='tempo'>Tempo</option>
                </select>
              </label>
              <label className='block text-xs font-medium text-muted'>
                URL
                <input
                  value={form.url}
                  onChange={(e) => {
                    setForm((f) => ({ ...f, url: e.target.value }));
                    setUrlError('');
                  }}
                  onBlur={() => form.url && validateUrl(form.url)}
                  required
                  className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm font-mono'
                  placeholder='http://prometheus:9090'
                />
                {urlError && <span className='mt-1 block text-xs text-red-400'>{urlError}</span>}
              </label>
              <label className='block text-xs font-medium text-muted'>
                Auth mode
                <select
                  value={form.authMode}
                  onChange={(e) => setForm((f) => ({ ...f, authMode: e.target.value }))}
                  className='mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
                >
                  <option value='none'>None</option>
                  <option value='oauth2_secret'>OAuth2 secret</option>
                  <option value='basic_secret'>Basic secret</option>
                </select>
              </label>
              <div className='flex justify-end gap-2 pt-2'>
                <button
                  type='button'
                  onClick={() => {
                    setShowCreate(false);
                    setUrlError('');
                  }}
                  className='px-4 py-1.5 text-sm text-muted hover:text-zinc-200'
                >
                  Cancel
                </button>
                <button
                  type='submit'
                  disabled={createMut.isPending}
                  className='rounded-md bg-indigo-600 px-4 py-1.5 text-sm text-white hover:bg-indigo-500 disabled:opacity-50'
                >
                  {createMut.isPending ? 'Creating…' : 'Create'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}
