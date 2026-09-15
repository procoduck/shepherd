import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { KeyRound, Plus, ShieldCheck, Trash2, UserCog } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { CreateUserModal } from '@/components/admin/CreateUserModal';
import { EditUserModal } from '@/components/admin/EditUserModal';
import { ResetPasswordModal } from '@/components/admin/ResetPasswordModal';
import type { CreateUserFormState } from '@/components/admin/UserForms';
import { QueryError } from '@/components/QueryError';
import { Banner } from '@/components/ui/Banner';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import type { User } from '@/gen/shepherd/mgmt/v1/user_pb';
import { useMe } from '@/hooks/useMe';

/**
 * Admin → Users.
 *
 * Local accounts only. A user who signs in through an identity provider has no
 * row here — their access comes from the groups in their token — so this page
 * says so rather than presenting an empty list as "there are no users".
 */

function userColumns(
  setEditUser: (u: User) => void,
  setResetUser: (u: User) => void,
  setDeleteUser: (u: User) => void,
): DataTableColumn<User>[] {
  return [
    {
      key: 'login',
      header: 'Login',
      cellClassName: 'px-4 py-3',
      render: (u) => (
        <>
          <span className='font-medium'>{u.login}</span>
          {u.email && <span className='ml-2 text-xs text-muted-2'>{u.email}</span>}
        </>
      ),
    },
    {
      key: 'name',
      header: 'Name',
      cellClassName: 'px-4 py-3 text-muted',
      render: (u) => u.displayName || '—',
    },
    {
      key: 'orgs',
      header: 'Organisations',
      cellClassName: 'px-4 py-3 text-muted',
      render: (u) =>
        u.orgs.length === 0 ? (
          <span className='text-muted-2'>none</span>
        ) : (
          u.orgs.map((o) => (
            <span key={o.id} className='mr-1 rounded bg-border px-1.5 py-0.5 text-xs'>
              {o.name} · {o.role}
            </span>
          ))
        ),
    },
    {
      key: 'status',
      header: 'Status',
      cellClassName: 'px-4 py-3',
      render: (u) => (
        <div className='flex flex-wrap gap-1'>
          {u.isAppAdmin && (
            <span className='inline-flex items-center gap-1 rounded bg-indigo-500/15 px-1.5 py-0.5 text-xs text-indigo-300'>
              <ShieldCheck size={11} /> app admin
            </span>
          )}
          {u.disabled && (
            <span className='rounded bg-red-500/15 px-1.5 py-0.5 text-xs text-red-400'>
              disabled
            </span>
          )}
          {u.mustChangePassword && (
            <span className='rounded bg-amber-500/15 px-1.5 py-0.5 text-xs text-amber-300'>
              must change password
            </span>
          )}
          {!u.isAppAdmin && !u.disabled && !u.mustChangePassword && (
            <span className='text-xs text-muted-2'>active</span>
          )}
        </div>
      ),
    },
    {
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-3 text-right whitespace-nowrap',
      render: (u) => (
        <>
          <button
            data-testid={`user-edit-${u.login}`}
            onClick={() => setEditUser(u)}
            title='Edit'
            className='mr-2 text-muted-3 hover:text-zinc-200'
          >
            <UserCog size={15} />
          </button>
          <button
            data-testid={`user-reset-${u.login}`}
            onClick={() => setResetUser(u)}
            title='Reset password'
            className='mr-2 text-muted-3 hover:text-zinc-200'
          >
            <KeyRound size={15} />
          </button>
          <button
            data-testid={`user-delete-${u.login}`}
            onClick={() => setDeleteUser(u)}
            title='Delete'
            className='text-muted-3 hover:text-red-400'
          >
            <Trash2 size={15} />
          </button>
        </>
      ),
    },
  ];
}

export function AdminUsersPage() {
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const qc = useQueryClient();

  const [showCreate, setShowCreate] = useState(false);
  const [editUser, setEditUser] = useState<User | null>(null);
  const [resetUser, setResetUser] = useState<User | null>(null);
  const [deleteUser, setDeleteUser] = useState<User | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['users'],
    queryFn: () => clients.user.listUsers({}),
    enabled: isAppAdmin,
  });

  const invalidate = () => qc.invalidateQueries({ queryKey: ['users'] });
  const fail = (verb: string) => (e: unknown) =>
    toast.error(toApiError(e).message || `Failed to ${verb}`);

  const createMut = useMutation({
    mutationFn: (v: CreateUserFormState) =>
      clients.user.createUser({
        login: v.login,
        email: v.email,
        displayName: v.displayName,
        password: v.password,
        isAppAdmin: v.isAppAdmin,
        // An account whose password was chosen by someone other than its owner
        // is a handover, not a credential. Default on, and say why.
        mustChangePassword: v.mustChangePassword,
      }),
    onSuccess: () => {
      invalidate();
      setShowCreate(false);
      toast.success('User created');
    },
    onError: fail('create the user'),
  });

  const updateMut = useMutation({
    mutationFn: (u: {
      id: string;
      email: string;
      displayName: string;
      isAppAdmin: boolean;
      disabled: boolean;
    }) => clients.user.updateUser(u),
    onSuccess: () => {
      invalidate();
      setEditUser(null);
      toast.success('User updated');
    },
    onError: fail('update the user'),
  });

  const resetMut = useMutation({
    mutationFn: (v: { id: string; newPassword: string }) =>
      clients.user.resetUserPassword({ id: v.id, newPassword: v.newPassword }),
    onSuccess: () => {
      setResetUser(null);
      toast.success('Password reset — the user must change it at next sign-in');
    },
    onError: fail('reset the password'),
  });

  // An account with no org membership can sign in and see nothing, so
  // assigning one is part of creating a usable user rather than a separate
  // administrative act. Both calls invalidate the user list, which is where
  // the memberships are rendered.
  const setOrgRoleMut = useMutation({
    mutationFn: (v: { orgId: string; userId: string; role: string }) =>
      clients.user.setOrgMember(v),
    onSuccess: () => {
      invalidate();
      toast.success('Organisation role updated');
    },
    onError: fail('set the organisation role'),
  });

  const removeOrgMut = useMutation({
    mutationFn: (v: { orgId: string; userId: string }) => clients.user.removeOrgMember(v),
    onSuccess: () => {
      invalidate();
      toast.success('Removed from the organisation');
    },
    onError: fail('remove the organisation membership'),
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => clients.user.deleteUser({ id }),
    onSuccess: () => {
      invalidate();
      setDeleteUser(null);
      toast.success('User deleted');
    },
    onError: fail('delete the user'),
  });

  if (!isAppAdmin) {
    return (
      <Banner variant='warning' testId='users-forbidden'>
        User management is restricted to app admins.
      </Banner>
    );
  }
  if (isLoading) return <p className='text-sm text-muted'>Loading…</p>;
  if (isError) {
    return <QueryError error={error} noun='users' testId='users-error' />;
  }

  const users = data?.items ?? [];

  return (
    <div className='space-y-4'>
      <div className='flex items-start justify-between gap-4'>
        <div>
          <h1 className='text-xl font-semibold'>Users</h1>
          <p className='mt-1 text-sm text-muted'>
            Local accounts that sign in with a username and password. People who sign in through
            your identity provider do not appear here — their access comes from their groups.
          </p>
        </div>
        <div className='flex shrink-0 items-center gap-2'>
          {me?.authMethod === 'local' && (
            // The shell has no user menu (struck 2026-09-11), so the one
            // self-service action a local account has lives beside the
            // accounts it administers. Hidden for identity-provider sessions:
            // their password is not Shepherd's to change.
            <Link
              to='/change-password'
              search={{ required: false }}
              data-testid='change-my-password'
              className='flex items-center gap-1.5 rounded-md border border-border-strong px-3 py-1.5 text-xs font-medium hover:bg-border'
            >
              <KeyRound size={14} /> Change my password
            </Link>
          )}
          <button
            data-testid='user-new'
            onClick={() => setShowCreate(true)}
            className='flex shrink-0 items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New user
          </button>
        </div>
      </div>

      <DataTable
        columns={userColumns(setEditUser, setResetUser, setDeleteUser)}
        rows={users}
        rowKey={(u) => u.id}
        rowProps={(u) => ({ 'data-testid': `user-row-${u.login}` })}
      />

      {showCreate && (
        <CreateUserModal
          pending={createMut.isPending}
          onCancel={() => setShowCreate(false)}
          onSubmit={(v) => createMut.mutate(v)}
        />
      )}

      {editUser && (
        <EditUserModal
          // The live row, not the snapshot taken when the modal opened: an org
          // membership added or removed from inside the modal has to show
          // there, and editUser would still hold the state from before it.
          key={editUser.id}
          user={users.find((u) => u.id === editUser.id) ?? editUser}
          pending={updateMut.isPending}
          onCancel={() => setEditUser(null)}
          onSubmit={(v) => updateMut.mutate({ id: editUser.id, ...v })}
          onSetOrgRole={(orgId, role) => setOrgRoleMut.mutate({ orgId, userId: editUser.id, role })}
          onRemoveOrg={(orgId) => removeOrgMut.mutate({ orgId, userId: editUser.id })}
          orgPending={setOrgRoleMut.isPending || removeOrgMut.isPending}
        />
      )}

      {resetUser && (
        <ResetPasswordModal
          login={resetUser.login}
          pending={resetMut.isPending}
          onCancel={() => setResetUser(null)}
          onSubmit={(pw) => resetMut.mutate({ id: resetUser.id, newPassword: pw })}
        />
      )}

      {deleteUser && (
        <AdminConfirmDialog
          title={`Delete ${deleteUser.login}?`}
          body={`This removes the account, its organisation roles and any active sessions. ${
            deleteUser.isAppAdmin ? 'This user is an app admin. ' : ''
          }It cannot be undone.`}
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={deleteMut.isPending}
          onCancel={() => setDeleteUser(null)}
          onConfirm={() => deleteMut.mutate(deleteUser.id)}
        />
      )}
    </div>
  );
}
