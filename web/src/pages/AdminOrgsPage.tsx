import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Pencil, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input } from '@/components/ui/Field';
import type { Org } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { useMe } from '@/hooks/useMe';

const emptyCreateForm = {
  name: '',
  displayName: '',
  adminGroupId: '',
  editorGroupId: '',
  readerGroupId: '',
  tenantId: '',
};

function orgColumns(
  isAppAdmin: boolean,
  openEdit: (o: Org) => void,
  setDeleteOrg: (o: Org) => void,
): DataTableColumn<Org>[] {
  const cols: DataTableColumn<Org>[] = [
    {
      key: 'name',
      header: 'Name',
      cellClassName: 'px-4 py-2.5 font-mono text-xs',
      render: (o) => o.name,
    },
    { key: 'displayName', header: 'Display name', render: (o) => o.displayName },
    {
      key: 'tenant',
      header: 'Tenant',
      cellClassName: 'px-4 py-2.5 font-mono text-xs',
      render: (o) =>
        o.tenantId ? (
          o.tenantId
        ) : (
          // An org without a tenant cannot have tenant routes, so say that
          // rather than showing an empty cell an admin would read as
          // "nothing to do here".
          <span className='text-muted-3 italic'>not set &mdash; no routes possible</span>
        ),
    },
  ];
  if (isAppAdmin) {
    cols.push({
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (o) => (
        <div className='flex justify-end gap-3'>
          <button
            onClick={() => openEdit(o)}
            className='text-muted-3 transition-colors hover:text-indigo-400'
            aria-label={`Edit ${o.name}`}
          >
            <Pencil size={14} />
          </button>
          <button
            onClick={() => setDeleteOrg(o)}
            className='text-muted-3 transition-colors hover:text-red-400'
            aria-label={`Delete ${o.name}`}
          >
            <Trash2 size={14} />
          </button>
        </div>
      ),
    });
  }
  return cols;
}

export function AdminOrgsPage() {
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const qc = useQueryClient();

  const [showCreate, setShowCreate] = useState(false);
  const [createForm, setCreateForm] = useState(emptyCreateForm);
  const [editOrg, setEditOrg] = useState<Org | null>(null);
  const [editForm, setEditForm] = useState({
    displayName: '',
    adminGroupId: '',
    editorGroupId: '',
    readerGroupId: '',
    allowExperimentalComponents: false,
  });
  const [deleteOrg, setDeleteOrg] = useState<Org | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['orgs'],
    queryFn: () => clients.admin.listOrgs({}),
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ['orgs'] });

  const createMut = useMutation({
    mutationFn: () => clients.admin.createOrg({ ...createForm }),
    onSuccess: () => {
      toast.success('Organisation created');
      invalidate();
      setShowCreate(false);
      setCreateForm(emptyCreateForm);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to create organisation'),
  });

  const updateMut = useMutation({
    mutationFn: () => clients.admin.updateOrg({ orgId: editOrg?.id ?? '', ...editForm }),
    onSuccess: () => {
      toast.success('Organisation updated');
      invalidate();
      setEditOrg(null);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to update organisation'),
  });

  const deleteMut = useMutation({
    mutationFn: () => clients.admin.deleteOrg({ orgId: deleteOrg?.id ?? '' }),
    onSuccess: () => {
      toast.success('Organisation deleted');
      invalidate();
      setDeleteOrg(null);
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(
        err.code === 'already_exists'
          ? `Cannot delete: ${err.message || 'organisation is not empty'}`
          : err.message || 'Failed to delete organisation',
      );
      setDeleteOrg(null);
    },
  });

  function openEdit(o: Org) {
    setEditOrg(o);
    setEditForm({
      displayName: o.displayName,
      adminGroupId: o.adminGroupId,
      editorGroupId: o.editorGroupId,
      readerGroupId: o.readerGroupId,
      allowExperimentalComponents: o.allowExperimentalComponents,
    });
  }

  return (
    <div className='space-y-4'>
      <div className='flex items-center justify-between'>
        <h1 className='text-xl font-semibold'>Organisations</h1>
        {isAppAdmin && (
          <button
            onClick={() => setShowCreate(true)}
            className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New organisation
          </button>
        )}
      </div>

      {isError ? (
        <QueryError error={error} noun='organisations' />
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : (data?.items ?? []).length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>No organisations yet.</p>
          {isAppAdmin && (
            <button
              onClick={() => setShowCreate(true)}
              className='mt-3 text-xs text-indigo-400 hover:text-indigo-300'
            >
              Create your first organisation
            </button>
          )}
        </div>
      ) : (
        <DataTable
          columns={orgColumns(isAppAdmin, openEdit, setDeleteOrg)}
          rows={data?.items ?? []}
          rowKey={(o) => o.id}
        />
      )}

      {showCreate && (
        <AdminModal title='New organisation' onClose={() => setShowCreate(false)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createMut.mutate();
            }}
            className='space-y-4'
          >
            <Field label='Name'>
              <Input
                value={createForm.name}
                onChange={(e) => setCreateForm((f) => ({ ...f, name: e.target.value }))}
                required
                mono
                placeholder='prod-org'
              />
            </Field>
            <Field label='Display name'>
              <Input
                value={createForm.displayName}
                onChange={(e) => setCreateForm((f) => ({ ...f, displayName: e.target.value }))}
                required
                placeholder='Production Org'
              />
            </Field>
            <Field label='Admin group ID'>
              <Input
                value={createForm.adminGroupId}
                onChange={(e) => setCreateForm((f) => ({ ...f, adminGroupId: e.target.value }))}
                required
                mono
                placeholder='11111111-1111-1111-1111-111111111111'
              />
            </Field>
            <Field
              label='Editor group ID'
              optional
              hint='Members may author pipelines, wizards and simulations, but cannot change destinations, tenant routes, git credentials or teams. Leave empty for no editor tier.'
            >
              <Input
                value={createForm.editorGroupId}
                onChange={(e) => setCreateForm((f) => ({ ...f, editorGroupId: e.target.value }))}
                mono
                placeholder='33333333-3333-3333-3333-333333333333'
              />
            </Field>
            <Field label='Reader group ID' optional>
              <Input
                value={createForm.readerGroupId}
                onChange={(e) => setCreateForm((f) => ({ ...f, readerGroupId: e.target.value }))}
                mono
                placeholder='22222222-2222-2222-2222-222222222222'
              />
            </Field>
            <Field
              label='Tenant ID'
              optional
              hint={
                <>
                  The tenant this org&rsquo;s telemetry ships under, sent downstream as
                  X-Scope-OrgID. Only an application administrator sets it, and it cannot be changed
                  afterwards &mdash; routes already issued would keep working while naming the wrong
                  tenant. Leave blank to decide later; the org cannot have tenant routes until it is
                  set.
                </>
              }
            >
              <Input
                value={createForm.tenantId}
                onChange={(e) => setCreateForm((f) => ({ ...f, tenantId: e.target.value }))}
                mono
                placeholder='acme'
              />
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

      {editOrg && (
        <AdminModal title={`Edit ${editOrg.name}`} onClose={() => setEditOrg(null)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              updateMut.mutate();
            }}
            className='space-y-4'
          >
            <Field label='Display name'>
              <Input
                value={editForm.displayName}
                onChange={(e) => setEditForm((f) => ({ ...f, displayName: e.target.value }))}
                required
              />
            </Field>
            <Field label='Admin group ID'>
              <Input
                value={editForm.adminGroupId}
                onChange={(e) => setEditForm((f) => ({ ...f, adminGroupId: e.target.value }))}
                required
                mono
              />
            </Field>
            <Field
              label='Editor group ID'
              optional
              hint='Members may author pipelines, wizards and simulations, but cannot change destinations, tenant routes, git credentials or teams.'
            >
              <Input
                value={editForm.editorGroupId}
                onChange={(e) => setEditForm((f) => ({ ...f, editorGroupId: e.target.value }))}
                mono
              />
            </Field>
            <Field label='Reader group ID' optional>
              <Input
                value={editForm.readerGroupId}
                onChange={(e) => setEditForm((f) => ({ ...f, readerGroupId: e.target.value }))}
                mono
              />
            </Field>
            <label className='flex items-start gap-2 text-sm'>
              <input
                type='checkbox'
                data-testid='org-allow-experimental'
                checked={editForm.allowExperimentalComponents}
                onChange={(e) =>
                  setEditForm((f) => ({ ...f, allowExperimentalComponents: e.target.checked }))
                }
                className='mt-0.5'
              />
              <span>
                <span className='text-zinc-200'>Allow experimental components</span>
                <span className='block text-xs text-muted-2'>
                  Lets this org use experimental Alloy components in the visual builder. Off by
                  default — the builder hides them and the server refuses to render a graph that
                  uses one.
                </span>
              </span>
            </label>
            <AdminModalActions
              onCancel={() => setEditOrg(null)}
              submitLabel='Save'
              pendingLabel='Saving…'
              pending={updateMut.isPending}
            />
          </form>
        </AdminModal>
      )}

      {deleteOrg && (
        <AdminConfirmDialog
          title={`Delete ${deleteOrg.name}?`}
          body='This permanently deletes the organisation. Only empty organisations (no clusters or pipelines) can be deleted.'
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={deleteMut.isPending}
          onConfirm={() => deleteMut.mutate()}
          onCancel={() => setDeleteOrg(null)}
        />
      )}
    </div>
  );
}
