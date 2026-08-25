import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { KeyRound, Plus, ShieldCheck, Trash2, UserCog } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import type { User } from '@/gen/shepherd/mgmt/v1/user_pb';
import { useMe } from '@/hooks/useMe';

/**
 * Admin → Users.
 *
 * Local accounts only. A user who signs in through an identity provider has no
 * row here — their access comes from the groups in their token — so this page
 * says so rather than presenting an empty list as "there are no users".
 */
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
    mutationFn: (v: CreateForm) =>
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
      <div
        data-testid='users-forbidden'
        className='rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-300'
      >
        User management is restricted to app admins.
      </div>
    );
  }
  if (isLoading) return <p className='text-sm text-muted'>Loading…</p>;
  if (isError) {
    return (
      <div
        data-testid='users-error'
        className='rounded-md border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400'
      >
        {toApiError(error).message || 'Could not load users.'}
      </div>
    );
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
        <button
          data-testid='user-new'
          onClick={() => setShowCreate(true)}
          className='flex shrink-0 items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
        >
          <Plus size={14} /> New user
        </button>
      </div>

      <div className='rounded-lg border border-border overflow-hidden'>
        <table className='w-full text-sm'>
          <thead className='bg-card text-muted'>
            <tr>
              <th className='px-4 py-3 text-left font-medium'>Login</th>
              <th className='px-4 py-3 text-left font-medium'>Name</th>
              <th className='px-4 py-3 text-left font-medium'>Organisations</th>
              <th className='px-4 py-3 text-left font-medium'>Status</th>
              <th className='px-4 py-3' />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id} data-testid={`user-row-${u.login}`} className='border-t border-border'>
                <td className='px-4 py-3'>
                  <span className='font-medium'>{u.login}</span>
                  {u.email && <span className='ml-2 text-xs text-muted-2'>{u.email}</span>}
                </td>
                <td className='px-4 py-3 text-muted'>{u.displayName || '—'}</td>
                <td className='px-4 py-3 text-muted'>
                  {u.orgs.length === 0 ? (
                    <span className='text-muted-2'>none</span>
                  ) : (
                    u.orgs.map((o) => (
                      <span key={o.id} className='mr-1 rounded bg-border px-1.5 py-0.5 text-xs'>
                        {o.name} · {o.role}
                      </span>
                    ))
                  )}
                </td>
                <td className='px-4 py-3'>
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
                </td>
                <td className='px-4 py-3 text-right whitespace-nowrap'>
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
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {showCreate && (
        <CreateUserModal
          pending={createMut.isPending}
          onCancel={() => setShowCreate(false)}
          onSubmit={(v) => createMut.mutate(v)}
        />
      )}

      {editUser && (
        <EditUserModal
          user={editUser}
          pending={updateMut.isPending}
          onCancel={() => setEditUser(null)}
          onSubmit={(v) => updateMut.mutate({ id: editUser.id, ...v })}
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

interface CreateForm {
  login: string;
  email: string;
  displayName: string;
  password: string;
  isAppAdmin: boolean;
  mustChangePassword: boolean;
}

const input =
  'w-full rounded-md border border-border-strong bg-border px-3 py-2 text-sm text-zinc-100';

function Labelled({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className='space-y-1'>
      <div className='text-xs font-medium text-muted'>{label}</div>
      {children}
      {hint && <p className='text-xs text-muted-2'>{hint}</p>}
    </div>
  );
}

function CreateUserModal({
  pending,
  onCancel,
  onSubmit,
}: {
  pending: boolean;
  onCancel: () => void;
  onSubmit: (v: CreateForm) => void;
}) {
  const [v, setV] = useState<CreateForm>({
    login: '',
    email: '',
    displayName: '',
    password: '',
    isAppAdmin: false,
    mustChangePassword: true,
  });
  return (
    <AdminModal title='New user' onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit(v);
        }}
        className='space-y-3'
      >
        <Labelled label='Login'>
          <input
            data-testid='user-login'
            value={v.login}
            onChange={(e) => setV({ ...v, login: e.target.value })}
            className={input}
            autoComplete='off'
          />
        </Labelled>
        <Labelled label='Display name'>
          <input
            data-testid='user-display-name'
            value={v.displayName}
            onChange={(e) => setV({ ...v, displayName: e.target.value })}
            className={input}
          />
        </Labelled>
        <Labelled label='Email'>
          <input
            data-testid='user-email'
            type='email'
            value={v.email}
            onChange={(e) => setV({ ...v, email: e.target.value })}
            className={input}
          />
        </Labelled>
        <Labelled
          label='Password'
          hint='At least 8 characters. Stored hashed; it is never shown again.'
        >
          <input
            data-testid='user-password'
            type='password'
            autoComplete='new-password'
            value={v.password}
            onChange={(e) => setV({ ...v, password: e.target.value })}
            className={input}
          />
        </Labelled>
        <label className='flex items-start gap-2 text-sm'>
          <input
            data-testid='user-must-change'
            type='checkbox'
            checked={v.mustChangePassword}
            onChange={(e) => setV({ ...v, mustChangePassword: e.target.checked })}
            className='mt-0.5'
          />
          <span>
            Require a password change at first sign-in
            <span className='block text-xs text-muted-2'>
              Recommended: you chose this password, so it is a handover rather than a credential.
              The user reaches nothing but the change screen until it is done.
            </span>
          </span>
        </label>
        <label className='flex items-start gap-2 text-sm'>
          <input
            data-testid='user-app-admin'
            type='checkbox'
            checked={v.isAppAdmin}
            onChange={(e) => setV({ ...v, isAppAdmin: e.target.checked })}
            className='mt-0.5'
          />
          <span>
            App administrator
            <span className='block text-xs text-muted-2'>
              Full access everywhere, including user management and single sign-on.
            </span>
          </span>
        </label>
        <AdminModalActions
          onCancel={onCancel}
          submitLabel='Create user'
          pendingLabel='Creating…'
          pending={pending}
        />
      </form>
    </AdminModal>
  );
}

function EditUserModal({
  user,
  pending,
  onCancel,
  onSubmit,
}: {
  user: User;
  pending: boolean;
  onCancel: () => void;
  onSubmit: (v: {
    email: string;
    displayName: string;
    isAppAdmin: boolean;
    disabled: boolean;
  }) => void;
}) {
  const [email, setEmail] = useState(user.email);
  const [displayName, setDisplayName] = useState(user.displayName);
  const [isAppAdmin, setIsAppAdmin] = useState(user.isAppAdmin);
  const [disabled, setDisabled] = useState(user.disabled);
  return (
    <AdminModal title={`Edit ${user.login}`} onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit({ email, displayName, isAppAdmin, disabled });
        }}
        className='space-y-3'
      >
        <Labelled label='Display name'>
          <input
            data-testid='edit-display-name'
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className={input}
          />
        </Labelled>
        <Labelled label='Email'>
          <input
            data-testid='edit-email'
            type='email'
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className={input}
          />
        </Labelled>
        <label className='flex items-center gap-2 text-sm'>
          <input
            data-testid='edit-app-admin'
            type='checkbox'
            checked={isAppAdmin}
            onChange={(e) => setIsAppAdmin(e.target.checked)}
          />
          App administrator
        </label>
        <label className='flex items-start gap-2 text-sm'>
          <input
            data-testid='edit-disabled'
            type='checkbox'
            checked={disabled}
            onChange={(e) => setDisabled(e.target.checked)}
            className='mt-0.5'
          />
          <span>
            Disabled
            <span className='block text-xs text-muted-2'>
              Revokes access without deleting the account, so audit entries naming this user keep
              resolving to something.
            </span>
          </span>
        </label>
        <AdminModalActions
          onCancel={onCancel}
          submitLabel='Save'
          pendingLabel='Saving…'
          pending={pending}
        />
      </form>
    </AdminModal>
  );
}

function ResetPasswordModal({
  login,
  pending,
  onCancel,
  onSubmit,
}: {
  login: string;
  pending: boolean;
  onCancel: () => void;
  onSubmit: (pw: string) => void;
}) {
  const [pw, setPw] = useState('');
  return (
    <AdminModal title={`Reset password for ${login}`} onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit(pw);
        }}
        className='space-y-3'
      >
        <Labelled
          label='New password'
          hint='At least 8 characters. The user must change it at their next sign-in — a password you know is a handover, not a credential.'
        >
          <input
            data-testid='reset-password'
            type='password'
            autoComplete='new-password'
            value={pw}
            onChange={(e) => setPw(e.target.value)}
            className={input}
          />
        </Labelled>
        <AdminModalActions
          onCancel={onCancel}
          submitLabel='Reset password'
          pendingLabel='Resetting…'
          pending={pending}
          danger
        />
      </form>
    </AdminModal>
  );
}
