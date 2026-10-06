import { useState } from 'react';
import { Field, Input } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';

/** Admin → Users → "Reset password" (S9b). */
export function ResetPasswordModal({
  login,
  pending,
  error,
  onCancel,
  onSubmit,
}: {
  login: string;
  pending: boolean;
  /** The server's refusal, shown above the buttons (#249). */
  error?: string | null;
  onCancel: () => void;
  onSubmit: (pw: string) => void;
}) {
  const [pw, setPw] = useState('');
  return (
    <Modal title={`Reset password for ${login}`} onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit(pw);
        }}
        className='space-y-3'
      >
        <Field
          label='New password'
          hint='At least 8 characters. The user must change it at their next sign-in — a password you know is a handover, not a credential.'
        >
          <Input
            data-testid='reset-password'
            type='password'
            autoComplete='new-password'
            value={pw}
            onChange={(e) => setPw(e.target.value)}
          />
        </Field>
        <ModalActions
          onCancel={onCancel}
          submitLabel='Reset password'
          pendingLabel='Resetting…'
          pending={pending}
          error={error}
          danger
        />
      </form>
    </Modal>
  );
}
