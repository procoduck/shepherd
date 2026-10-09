import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, Pencil, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import {
  authModeLabel,
  DestinationFormDialog,
  type DestinationFormState,
  EMPTY_FORM,
  extraWithScopes,
  isSecretMode,
  scopesFromExtra,
} from '@/components/DestinationFormDialog';
import { extraWithTLS, tlsFromExtra } from '@/components/DestinationTLSFields';
import { withPipelineLinks } from '@/components/PipelineNameLinks';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import type { Destination } from '@/gen/shepherd/mgmt/v1/destination_pb';
import { useCanAdminister, useOrg } from '@/hooks/useOrg';
import { formError } from '@/lib/formError';

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
  openEdit: (d: Destination) => void,
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
      key: 'tenant',
      header: 'Tenant',
      cellClassName: 'px-4 py-2.5 font-mono text-xs text-muted',
      render: (d) => d.tenantId || <span className='text-muted-3'>&mdash;</span>,
    },
    {
      key: 'auth',
      header: 'Auth',
      cellClassName: 'px-4 py-2.5 text-xs text-muted',
      render: (d) => (
        <>
          <span>{authModeLabel(d.authMode)}</span>
          {isSecretMode(d.authMode) && (d.secretNamespace || d.secretName) && (
            <span className='block font-mono text-2xs text-muted-3'>
              {d.secretNamespace}/{d.secretName}
            </span>
          )}
        </>
      ),
    },
    {
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (d) =>
        canAdminister && (
          <div className='flex justify-end gap-3'>
            <button
              onClick={() => openEdit(d)}
              className='text-muted-3 transition-colors hover:text-indigo-400'
              aria-label={`Edit ${d.name}`}
            >
              <Pencil size={14} />
            </button>
            <button
              onClick={() => setPendingDelete({ id: d.id, name: d.name })}
              className='text-muted-3 transition-colors hover:text-red-400'
              aria-label='Delete destination'
            >
              <Trash2 size={14} />
            </button>
          </div>
        ),
    },
  ];
}

export function DestinationsPage() {
  const { orgId, orgs } = useOrg();
  const orgTenant = orgs.find((o) => o.id === orgId)?.tenantId ?? '';
  // Destinations decide where telemetry ships, so the server requires org
  // admin. Offering the form to an editor or viewer only produces a rejection
  // after they have filled it in. Edit is gated exactly like create.
  const canAdminister = useCanAdminister();
  const qc = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [editing, setEditing] = useState<Destination | null>(null);
  const [pendingDelete, setPendingDelete] = useState<{ id: string; name: string } | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['destinations', orgId],
    queryFn: () => clients.destination.listDestinations({ orgId }),
    enabled: !!orgId,
  });

  const createMut = useMutation({
    mutationFn: (form: DestinationFormState) => {
      const secret = isSecretMode(form.authMode);
      return clients.destination.createDestination({
        orgId,
        name: form.name,
        type: form.type,
        url: form.url,
        authMode: form.authMode,
        tenantId: form.tenantId.trim(),
        secretName: secret ? form.secretName.trim() : '',
        secretNamespace: secret ? form.secretNamespace.trim() : '',
        extra: extraWithTLS(extraWithScopes(undefined, form), form.tls),
      });
    },
    onSuccess: () => {
      toast.success('Destination created');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
      setShowCreate(false);
    },
  });

  // UpdateDestination replaces every field, so the ones this form does not
  // show (extra, and the Secret reference while the mode is `none`) are
  // sent back unchanged rather than wiped.
  // The server re-renders every wizard pipeline that ships to a changed
  // destination (destination_rerender.go), so their cached copies — each
  // pipeline, its revisions, the list — are now behind. Prefix keys: all of
  // this org's pipelines.
  const invalidatePipelines = () => {
    qc.invalidateQueries({ queryKey: ['pipeline', orgId] });
    qc.invalidateQueries({ queryKey: ['revisions', orgId] });
    qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
  };

  const updateMut = useMutation({
    mutationFn: ({ d, form }: { d: Destination; form: DestinationFormState }) => {
      const secret = isSecretMode(form.authMode);
      return clients.destination.updateDestination({
        orgId,
        id: d.id,
        name: form.name,
        type: form.type,
        url: form.url,
        authMode: form.authMode,
        tenantId: form.tenantId.trim(),
        secretName: secret ? form.secretName.trim() : d.secretName,
        secretNamespace: secret ? form.secretNamespace.trim() : d.secretNamespace,
        extra: extraWithTLS(extraWithScopes(d.extra, form), form.tls),
      });
    },
    onSuccess: () => {
      toast.success('Destination updated');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
      invalidatePipelines();
      setEditing(null);
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => clients.destination.deleteDestination({ orgId, id }),
    onSuccess: () => {
      toast.success('Destination deleted');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
      invalidatePipelines();
      setPendingDelete(null);
    },
  });
  const closeDelete = () => {
    setPendingDelete(null);
    deleteMut.reset();
  };

  // failed_precondition: a wizard pipeline still names it (#262, the
  // message lists them); already_exists: a tenant binding points at it.
  const deleteError = (() => {
    if (!deleteMut.error) return null;
    const err = toApiError(deleteMut.error);
    if (err.code === 'failed_precondition' || err.code === 'already_exists') {
      return `Cannot delete: ${err.message || 'it is still in use'}`;
    }
    return formError(deleteMut.error, 'Failed to delete destination');
  })();

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
          body={`Delete "${pendingDelete.name}"? A destination a wizard pipeline still ships to cannot be deleted until each such pipeline is detached from its wizard or deleted.`}
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={deleteMut.isPending}
          onCancel={closeDelete}
          // Stays open on a refusal, which shows in the dialog with each
          // pipeline it names linked (#249, M4).
          onConfirm={() => deleteMut.mutate(pendingDelete.id)}
          // The wizard pipelines a refusal names (#262) are linked to
          // their pages, where each can be restored, detached or deleted.
          error={withPipelineLinks(deleteError, deleteMut.error)}
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
          columns={destinationColumns(canAdminister, setEditing, setPendingDelete)}
          rows={data?.items ?? []}
          rowKey={(d) => d.id}
        />
      )}

      {showCreate && (
        <DestinationFormDialog
          title='New destination'
          // The org's own tenant pre-fills a new destination (#261); an
          // external backend's tenant can replace it.
          initial={{ ...EMPTY_FORM, tenantId: orgTenant }}
          submitLabel='Create'
          pendingLabel='Creating…'
          pending={createMut.isPending}
          error={formError(createMut.error, 'Failed to create destination')}
          onCancel={() => {
            setShowCreate(false);
            createMut.reset();
          }}
          onSubmit={(form) => createMut.mutate(form)}
        />
      )}

      {editing && (
        <DestinationFormDialog
          title={`Edit ${editing.name}`}
          initial={{
            name: editing.name,
            type: editing.type,
            url: editing.url,
            authMode: editing.authMode,
            secretNamespace: editing.secretNamespace,
            secretName: editing.secretName,
            scopes: scopesFromExtra(editing.extra),
            tenantId: editing.tenantId,
            tls: tlsFromExtra(editing.extra),
          }}
          submitLabel='Save'
          pendingLabel='Saving…'
          pending={updateMut.isPending}
          error={withPipelineLinks(
            formError(updateMut.error, 'Failed to update destination'),
            updateMut.error,
          )}
          onCancel={() => {
            setEditing(null);
            updateMut.reset();
          }}
          onSubmit={(form) => updateMut.mutate({ d: editing, form })}
        />
      )}
    </div>
  );
}
