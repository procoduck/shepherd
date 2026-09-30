import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ExternalLink, Pencil, Plus, Trash2 } from 'lucide-react';
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

/**
 * Human labels for the auth modes the schema admits (0001_init: `none`,
 * `oauth2_secret`, `basic_secret`). An unknown value — possible, since the API
 * stores whatever it is sent — is shown verbatim rather than hidden.
 */
const AUTH_MODE_LABELS: Record<string, string> = {
  none: 'None',
  basic_secret: 'Basic auth (Kubernetes Secret)',
  oauth2_secret: 'OAuth2 (Kubernetes Secret)',
};

function authModeLabel(mode: string): string {
  return AUTH_MODE_LABELS[mode] ?? mode;
}

function isSecretMode(mode: string): boolean {
  return mode === 'basic_secret' || mode === 'oauth2_secret';
}

/**
 * What a secret-based auth mode does today. This text states what the code
 * does, not what docs/spec.md §11 intends: the server stores the mode and the
 * Secret reference, but no pipeline renderer reads them — the wizards emit
 * `url = sys.env("SHEPHERD_DEST_<NAME>_URL")` and no auth block — and nothing
 * defines which keys the Secret must hold. Change this text when that changes.
 */
function SecretModeExplanation({ mode }: { mode: string }) {
  const kind = mode === 'basic_secret' ? 'HTTP basic auth' : 'OAuth2 client credentials';
  return (
    <div
      data-testid='auth-mode-explanation'
      className='rounded-md border border-border bg-card/40 px-3 py-2 text-2xs text-muted'
    >
      <p>
        For {kind} kept in a Kubernetes Secret that already exists on each spoke cluster. Shepherd
        stores only the Secret's namespace and name &mdash; never the credential itself.
      </p>
      <p className='mt-1'>
        Not applied yet: generated pipelines do not read this Secret or add an auth block, and the
        keys the Secret must hold are not defined. Until they are, configure the credentials on the
        collector itself.
      </p>
    </div>
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

interface DestinationFormState {
  name: string;
  type: string;
  url: string;
  authMode: string;
  secretNamespace: string;
  secretName: string;
}

const EMPTY_FORM: DestinationFormState = {
  name: '',
  type: 'prometheus',
  url: '',
  authMode: 'none',
  secretNamespace: '',
  secretName: '',
};

function validateUrl(url: string): string {
  try {
    const parsed = new URL(url);
    // new URL() accepts javascript: and data: quite happily, so parsing is
    // not validation. Only http(s) is a destination Shepherd can ship to.
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return 'Only http:// and https:// destinations are supported';
    }
    return '';
  } catch {
    return 'Enter a valid URL (e.g. http://prometheus:9090)';
  }
}

/**
 * The create and edit dialog share one form. The secret reference fields only
 * appear for a secret-based auth mode; the parent decides what to send for
 * them when the mode is `none`.
 */
function DestinationFormDialog({
  title,
  initial,
  submitLabel,
  pendingLabel,
  pending,
  onCancel,
  onSubmit,
}: {
  title: string;
  initial: DestinationFormState;
  submitLabel: string;
  pendingLabel: string;
  pending: boolean;
  onCancel: () => void;
  onSubmit: (form: DestinationFormState) => void;
}) {
  const [form, setForm] = useState(initial);
  const [urlError, setUrlError] = useState('');
  const secretMode = isSecretMode(form.authMode);

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const err = validateUrl(form.url);
    setUrlError(err);
    if (!err) onSubmit(form);
  }

  return (
    <Modal title={title} onClose={onCancel}>
      <form onSubmit={handleSubmit} className='space-y-4'>
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
            onBlur={() => form.url && setUrlError(validateUrl(form.url))}
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
            {!(form.authMode in AUTH_MODE_LABELS) && (
              <option value={form.authMode}>{form.authMode}</option>
            )}
            <option value='none'>{AUTH_MODE_LABELS.none}</option>
            <option value='basic_secret'>{AUTH_MODE_LABELS.basic_secret}</option>
            <option value='oauth2_secret'>{AUTH_MODE_LABELS.oauth2_secret}</option>
          </Select>
        </Field>
        {secretMode && (
          <>
            <SecretModeExplanation mode={form.authMode} />
            <div className='flex gap-3'>
              <Field label='Secret namespace' className='flex-1'>
                <Input
                  value={form.secretNamespace}
                  onChange={(e) => setForm((f) => ({ ...f, secretNamespace: e.target.value }))}
                  required
                  mono
                  placeholder='monitoring'
                />
              </Field>
              <Field label='Secret name' className='flex-1'>
                <Input
                  value={form.secretName}
                  onChange={(e) => setForm((f) => ({ ...f, secretName: e.target.value }))}
                  required
                  mono
                  placeholder='mimir-credentials'
                />
              </Field>
            </div>
          </>
        )}
        <ModalActions
          onCancel={onCancel}
          submitLabel={submitLabel}
          pendingLabel={pendingLabel}
          pending={pending}
        />
      </form>
    </Modal>
  );
}

export function DestinationsPage() {
  const orgId = useOrgId();
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
        tenantId: '',
        secretName: secret ? form.secretName.trim() : '',
        secretNamespace: secret ? form.secretNamespace.trim() : '',
      });
    },
    onSuccess: () => {
      toast.success('Destination created');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
      setShowCreate(false);
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(err.message || 'Failed to create destination');
    },
  });

  // UpdateDestination replaces every field, so the ones this form does not
  // show (tenant ID, extra, and the Secret reference while the mode is
  // `none`) are sent back unchanged rather than wiped.
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
        tenantId: d.tenantId,
        secretName: secret ? form.secretName.trim() : d.secretName,
        secretNamespace: secret ? form.secretNamespace.trim() : d.secretNamespace,
        extra: d.extra,
      });
    },
    onSuccess: () => {
      toast.success('Destination updated');
      qc.invalidateQueries({ queryKey: ['destinations', orgId] });
      setEditing(null);
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(err.message || 'Failed to update destination');
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
          columns={destinationColumns(canAdminister, setEditing, setPendingDelete)}
          rows={data?.items ?? []}
          rowKey={(d) => d.id}
        />
      )}

      {showCreate && (
        <DestinationFormDialog
          title='New destination'
          initial={EMPTY_FORM}
          submitLabel='Create'
          pendingLabel='Creating…'
          pending={createMut.isPending}
          onCancel={() => setShowCreate(false)}
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
          }}
          submitLabel='Save'
          pendingLabel='Saving…'
          pending={updateMut.isPending}
          onCancel={() => setEditing(null)}
          onSubmit={(form) => updateMut.mutate({ d: editing, form })}
        />
      )}
    </div>
  );
}
