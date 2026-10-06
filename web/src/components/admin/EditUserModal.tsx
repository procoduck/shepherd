import { useQuery } from '@tanstack/react-query';
import { Trash2 } from 'lucide-react';
import { useState } from 'react';
import { clients } from '@/api/transport';
import { Field, Input } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';
import type { User } from '@/gen/shepherd/mgmt/v1/user_pb';
import { ORG_ROLES } from './UserForms';

/** Admin → Users → "Edit user" (S9b), including its org-membership editor. */
export function EditUserModal({
  user,
  pending,
  error,
  onCancel,
  onSubmit,
  onSetOrgRole,
  onRemoveOrg,
  orgPending,
}: {
  user: User;
  pending: boolean;
  /** The server's refusal, shown above the buttons (#249). */
  error?: string | null;
  onCancel: () => void;
  onSubmit: (v: {
    email: string;
    displayName: string;
    isAppAdmin: boolean;
    disabled: boolean;
  }) => void;
  onSetOrgRole: (orgId: string, role: string) => void;
  onRemoveOrg: (orgId: string) => void;
  orgPending: boolean;
}) {
  const [email, setEmail] = useState(user.email);
  const [displayName, setDisplayName] = useState(user.displayName);
  const [isAppAdmin, setIsAppAdmin] = useState(user.isAppAdmin);
  const [disabled, setDisabled] = useState(user.disabled);
  const [addOrgId, setAddOrgId] = useState('');
  const [addRole, setAddRole] = useState('viewer');

  // Every org, so a membership can be added. This is the app-admin surface, so
  // the list is not filtered by the editing admin's own memberships.
  const { data: orgsData } = useQuery({
    queryKey: ['orgs'],
    queryFn: () => clients.admin.listOrgs({}),
  });
  const memberOf = new Set(user.orgs.map((o) => o.id));
  const joinable = (orgsData?.items ?? []).filter((o) => !memberOf.has(o.id));

  return (
    <Modal title={`Edit ${user.login}`} onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit({ email, displayName, isAppAdmin, disabled });
        }}
        className='space-y-3'
      >
        <Field label='Display name'>
          <Input
            data-testid='edit-display-name'
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        </Field>
        <Field label='Email'>
          <Input
            data-testid='edit-email'
            type='email'
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </Field>
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
        <div className='space-y-2 rounded-md border border-border p-3'>
          <p className='text-xs font-medium text-muted'>Organisations</p>
          {user.isAppAdmin && (
            <p className='text-2xs text-muted-3'>
              An app administrator already reaches every organisation; these roles apply if that is
              ever turned off.
            </p>
          )}
          {user.orgs.length === 0 ? (
            <p data-testid='edit-orgs-empty' className='text-xs text-muted-2'>
              None. Without one, this account can sign in but see nothing.
            </p>
          ) : (
            <ul className='space-y-1.5'>
              {user.orgs.map((o) => (
                <li
                  key={o.id}
                  data-testid={`edit-org-${o.name}`}
                  className='flex items-center gap-2 text-sm'
                >
                  <span className='flex-1 truncate'>{o.displayName || o.name}</span>
                  <select
                    data-testid={`edit-org-role-${o.name}`}
                    aria-label={`Role in ${o.displayName || o.name}`}
                    value={o.role}
                    disabled={orgPending}
                    onChange={(e) => onSetOrgRole(o.id, e.target.value)}
                    className='rounded-md border border-border-strong bg-card px-2 py-1 text-xs'
                  >
                    {ORG_ROLES.map((r) => (
                      <option key={r} value={r}>
                        {r}
                      </option>
                    ))}
                  </select>
                  <button
                    type='button'
                    data-testid={`edit-org-remove-${o.name}`}
                    disabled={orgPending}
                    onClick={() => onRemoveOrg(o.id)}
                    title='Remove from this organisation'
                    className='text-muted-3 hover:text-red-400 disabled:opacity-50'
                  >
                    <Trash2 size={14} />
                  </button>
                </li>
              ))}
            </ul>
          )}
          {joinable.length > 0 && (
            <div className='flex items-center gap-2 pt-1'>
              <select
                data-testid='edit-org-add-id'
                aria-label='Organisation to add'
                value={addOrgId}
                onChange={(e) => setAddOrgId(e.target.value)}
                className='flex-1 rounded-md border border-border-strong bg-card px-2 py-1 text-xs'
              >
                <option value=''>Add to an organisation…</option>
                {joinable.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.displayName || o.name}
                  </option>
                ))}
              </select>
              <select
                data-testid='edit-org-add-role'
                aria-label='Role in the organisation to add'
                value={addRole}
                onChange={(e) => setAddRole(e.target.value)}
                className='rounded-md border border-border-strong bg-card px-2 py-1 text-xs'
              >
                {ORG_ROLES.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
              <button
                type='button'
                data-testid='edit-org-add'
                disabled={!addOrgId || orgPending}
                onClick={() => {
                  onSetOrgRole(addOrgId, addRole);
                  setAddOrgId('');
                }}
                className='rounded-md bg-indigo-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
              >
                Add
              </button>
            </div>
          )}
        </div>
        <ModalActions
          onCancel={onCancel}
          submitLabel='Save'
          pendingLabel='Saving…'
          pending={pending}
          error={error}
        />
      </form>
    </Modal>
  );
}
