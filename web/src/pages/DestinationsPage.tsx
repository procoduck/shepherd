import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input, Select } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';
import type { Destination } from '@/gen/shepherd/mgmt/v1/destination_pb';
import { useCanAdminister, useOrgId } from '@/hooks/useOrg';

/**
 * A destination URL, rendered as a link only when it is safe to click.
 *
 * Two separate problems, both reachable because the server stores the URL
 * verbatim and the API can be called without this form: a `javascript:` URL in
 * an href executes in the app's own origin when clicked, which turns "can
 * create a destination" into "can run script as whoever views the page"; and
 * `new URL(...)` throws on anything unparsable, which took down the whole route
 * rather than one cell.
 */
function DestinationUrl({ url }: { url: string }) {
  let host: string | null = null;
  try {
    const parsed = new URL(url);
    // Allow-list, not deny-list. Anything that is not plain web traffic is
    // shown as text rather than guessed at.
    if (parsed.protocol === 'http:' || parsed.protocol === 'https:') {
      host = parsed.host;
    }
  } catch {
    host = null;
  }

  if (host === null) {
    return <span title={url}>{url}</span>;
  }
  return (
    <a
      href={url}
      target='_blank'
      rel='noreferrer'
      className='flex items-center gap-1 hover:text-indigo-400'
    >
      {host}
      <ExternalLink size={10} />
    </a>
  );
}

function destinationColumns(
  canAdminister: boolean,
  setPendingDelete: (d: { id: string; name: string } | null) => void,
): DataTableColumn<Destination>[] {
  return [
    {
      key: 'name',
      header: 'Name',
      cellClassName: 'px-4 py-2.5 font-medium',
      render: (d) => d.name,
    },
    { key: 'type', header: 'Type', cellClassName: 'px-4 py-2.5 text-muted', render: (d) => d.type },
    {
      key: 'url',
      header: 'URL',
      cellClassName: 'px-4 py-2.5 font-mono text-xs text-zinc-300',
      render: (d) => <DestinationUrl url={d.url} />,
    },
    {
      key: 'auth',
      header: 'Auth',
      cellClassName: 'px-4 py-2.5 text-xs text-muted',
      render: (d) => d.authMode,
    },
    {
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (d) =>
        canAdminister && (
          <button
            onClick={() => setPendingDelete({ id: d.id, name: d.name })}
            className='text-muted-3 transition-colors hover:text-red-400'
            aria-label='Delete destination'
          >
            <Trash2 size={14} />
          </button>
        ),
    },
  ];
}

export function DestinationsPage() {
  const orgId = useOrgId();
  // Destinations decide where telemetry ships, so the server requires org
  // admin. Offering the form to an editor or viewer only produces a rejection
  // after they have filled it in.
  const canAdminister = useCanAdminister();
  const qc = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState({ name: '', type: 'prometheus', url: '', authMode: 'none' });
  const [urlError, setUrlError] = useState('');
  const [pendingDelete, setPendingDelete] = useState<{ id: string; name: string } | null>(null);

  const { data, isLoading, isError, error } = useQuery({
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
      const parsed = new URL(url);
      // new URL() accepts javascript: and data: quite happily, so parsing is
      // not validation. Only http(s) is a destination Shepherd can ship to.
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
        setUrlError('Only http:// and https:// destinations are supported');
        return false;
      }
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
        {!!orgId && canAdminister && (
          <button
            onClick={() => setShowCreate(true)}
            className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New destination
          </button>
        )}
      </div>

      {pendingDelete && (
        <AdminConfirmDialog
          title='Delete destination'
          body={`Delete "${pendingDelete.name}"? Pipelines that ship to it will stop resolving this destination.`}
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={deleteMut.isPending}
          onCancel={() => setPendingDelete(null)}
          onConfirm={() => {
            deleteMut.mutate(pendingDelete.id);
            setPendingDelete(null);
          }}
        />
      )}

      {!orgId ? (
        <p className='text-sm text-muted'>No organisation context.</p>
      ) : isError ? (
        <QueryError error={error} noun='destinations' />
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
        <DataTable
          columns={destinationColumns(canAdminister, setPendingDelete)}
          rows={data?.items ?? []}
          rowKey={(d) => d.id}
        />
      )}

      {showCreate && (
        <Modal
          title='New destination'
          onClose={() => {
            setShowCreate(false);
            setUrlError('');
          }}
        >
          <form onSubmit={handleCreate} className='space-y-4'>
            <Field label='Name'>
              <Input
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                required
                placeholder='prom-prod'
              />
            </Field>
            <Field label='Type'>
              <Select
                value={form.type}
                onChange={(e) => setForm((f) => ({ ...f, type: e.target.value }))}
              >
                <option value='prometheus'>Prometheus</option>
                <option value='loki'>Loki</option>
                <option value='otlp'>Tempo (OTLP)</option>
              </Select>
            </Field>
            <Field label='URL' error={urlError}>
              <Input
                value={form.url}
                onChange={(e) => {
                  setForm((f) => ({ ...f, url: e.target.value }));
                  setUrlError('');
                }}
                onBlur={() => form.url && validateUrl(form.url)}
                required
                mono
                placeholder='http://prometheus:9090'
              />
            </Field>
            <Field label='Auth mode'>
              <Select
                value={form.authMode}
                onChange={(e) => setForm((f) => ({ ...f, authMode: e.target.value }))}
              >
                <option value='none'>None</option>
                <option value='oauth2_secret'>OAuth2 secret</option>
                <option value='basic_secret'>Basic secret</option>
              </Select>
            </Field>
            <ModalActions
              onCancel={() => {
                setShowCreate(false);
                setUrlError('');
              }}
              submitLabel='Create'
              pendingLabel='Creating…'
              pending={createMut.isPending}
            />
          </form>
        </Modal>
      )}
    </div>
  );
}
