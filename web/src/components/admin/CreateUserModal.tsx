import { useState } from 'react';
import { Field, Input } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';
import { type CreateUserFormState, emptyCreateUserForm } from './UserForms';

/** Admin → Users → "New user" (S9b). */
export function CreateUserModal({
  pending,
  error,
  onCancel,
  onSubmit,
}: {
  pending: boolean;
  /** The server's refusal, shown above the buttons (#249). */
  error?: string | null;
  onCancel: () => void;
  onSubmit: (v: CreateUserFormState) => void;
}) {
  const [v, setV] = useState<CreateUserFormState>(emptyCreateUserForm);
  return (
    <Modal title='New user' onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit(v);
        }}
        className='space-y-3'
      >
        <Field label='Login'>
          <Input
            data-testid='user-login'
            value={v.login}
            onChange={(e) => setV({ ...v, login: e.target.value })}
            autoComplete='off'
          />
        </Field>
        <Field label='Display name'>
          <Input
            data-testid='user-display-name'
            value={v.displayName}
            onChange={(e) => setV({ ...v, displayName: e.target.value })}
          />
        </Field>
        <Field label='Email'>
          <Input
            data-testid='user-email'
            type='email'
            value={v.email}
            onChange={(e) => setV({ ...v, email: e.target.value })}
          />
        </Field>
        <Field
          label='Password'
          hint='At least 8 characters. Stored hashed; it is never shown again.'
        >
          <Input
            data-testid='user-password'
            type='password'
            autoComplete='new-password'
            value={v.password}
            onChange={(e) => setV({ ...v, password: e.target.value })}
          />
        </Field>
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
        <ModalActions
          onCancel={onCancel}
          submitLabel='Create user'
          pendingLabel='Creating…'
          pending={pending}
          error={error}
        />
      </form>
    </Modal>
  );
}
