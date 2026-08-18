import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { type Destination, orgApi } from '@/api/client';
import { useOrgId } from '@/hooks/useOrg';

export function DestinationsPage() {
  const orgId = useOrgId();
  const qc = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ name: '', type: 'prometheus', url: '', auth_mode: 'none' });
  const [urlError, setUrlError] = useState('');

  const { data, isLoading } = useQuery({
    queryKey: ['destinations', orgId],
    queryFn: () => orgApi.listDestinations(orgId),
    enabled: !!orgId,
  });

  const createMut = useMutation({
    mutationFn: () =>
      orgApi.createDestination(orgId, {
        ...form,
        tenant_id: '',
        secret_name: '',
        secret_namespace: '',
      }),
    onSuccess: () => {
      toast.success('Destination created');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
      setShowCreate(false);
      setForm({ name: '', type: 'prometheus', url: '', auth_mode: 'none' });
    },
    onError: (e) => {
      const err = e as { message?: string };
      toast.error(err.message ?? 'Failed to create destination');
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => orgApi.deleteDestination(orgId, id),
    onSuccess: () => {
      toast.success('Destination deleted');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
    },
    onError: (e) => {
      const err = e as { code?: string; message?: string };
      toast.error(
        err.code === 'in_use'
          ? `Cannot delete: ${err.message ?? 'referenced by a pipeline'}`
          : (err.message ?? 'Failed to delete destination'),
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
        <p className='text-sm text-zinc-400'>No organisation context.</p>
      ) : isLoading ? (
        <p className='text-sm text-zinc-400'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-zinc-800 bg-zinc-900/40 p-8 text-center'>
          <p className='text-sm text-zinc-400'>No destinations yet.</p>
          <button
            onClick={() => setShowCreate(true)}
            className='mt-3 text-xs text-indigo-400 hover:text-indigo-300'
          >
            Add your first destination
          </button>
        </div>
      ) : (
        <div className='rounded-lg border border-zinc-800 overflow-hidden'>
          <table className='w-full text-sm'>
            <thead className='bg-zinc-900 text-zinc-400'>
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
                <tr key={d.id} className='border-t border-zinc-800 hover:bg-zinc-900/60'>
                  <td className='px-4 py-2.5 font-medium'>{d.name}</td>
                  <td className='px-4 py-2.5 text-zinc-400'>{d.type}</td>
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
                  <td className='px-4 py-2.5 text-xs text-zinc-400'>{d.auth_mode}</td>
                  <td className='px-4 py-2.5 text-right'>
                    <button
                      onClick={() => deleteMut.mutate(d.id)}
                      className='text-zinc-600 transition-colors hover:text-red-400'
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
          <div className='w-full max-w-md rounded-xl border border-zinc-800 bg-zinc-950 p-6 shadow-2xl'>
            <h2 className='mb-4 text-base font-semibold'>New destination</h2>
            <form onSubmit={handleCreate} className='space-y-4'>
              <label className='block text-xs font-medium text-zinc-400'>
                Name
                <input
                  value={form.name}
                  onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                  required
                  className='mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-sm'
                  placeholder='prom-prod'
                />
              </label>
              <label className='block text-xs font-medium text-zinc-400'>
                Type
                <select
                  value={form.type}
                  onChange={(e) => setForm((f) => ({ ...f, type: e.target.value }))}
                  className='mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-sm'
                >
                  <option value='prometheus'>Prometheus</option>
                  <option value='loki'>Loki</option>
                  <option value='tempo'>Tempo</option>
                </select>
              </label>
              <label className='block text-xs font-medium text-zinc-400'>
                URL
                <input
                  value={form.url}
                  onChange={(e) => {
                    setForm((f) => ({ ...f, url: e.target.value }));
                    setUrlError('');
                  }}
                  onBlur={() => form.url && validateUrl(form.url)}
                  required
                  className='mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-sm font-mono'
                  placeholder='http://prometheus:9090'
                />
                {urlError && <span className='mt-1 block text-xs text-red-400'>{urlError}</span>}
              </label>
              <label className='block text-xs font-medium text-zinc-400'>
                Auth mode
                <select
                  value={form.auth_mode}
                  onChange={(e) => setForm((f) => ({ ...f, auth_mode: e.target.value }))}
                  className='mt-1 w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-sm'
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
                  className='px-4 py-1.5 text-sm text-zinc-400 hover:text-zinc-200'
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
