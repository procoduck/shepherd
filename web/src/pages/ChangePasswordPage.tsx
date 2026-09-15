import { useQueryClient } from '@tanstack/react-query';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { KeyRound } from 'lucide-react';
import { useState } from 'react';
import { changePassword, LocalAuthError } from '@/api/localAuth';
import { Banner } from '@/components/ui/Banner';
import { Field } from '@/components/ui/Field';

const inputClass =
  'w-full rounded-md border border-border-strong bg-border px-3 py-2 text-sm text-zinc-100';

export function ChangePasswordPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  // `required` is set when the server owes-a-change flow sent the user here;
  // it turns the copy from "you may" into "you must". The route's
  // validateSearch normalizes ?required=1 (a hard redirect) and required=true
  // (a link) to the same boolean.
  const { required } = useSearch({ strict: false }) as { required?: boolean };

  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError('');
    if (next !== confirm) {
      setError('The new password and its confirmation do not match.');
      return;
    }
    setSaving(true);
    try {
      await changePassword(current, next);
      // The session is now unblocked; drop every cached query so /api/me and
      // the org-scoped lists refetch without the old 403.
      queryClient.clear();
      navigate({ to: '/' });
    } catch (err) {
      setError(
        err instanceof LocalAuthError ? err.message : 'Could not change the password. Try again.',
      );
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className='flex min-h-screen items-center justify-center bg-background px-4'>
      <div className='w-full max-w-sm space-y-6 rounded-lg border border-border bg-card p-6'>
        <div className='flex flex-col items-center gap-2 text-center'>
          <KeyRound className='text-accent' size={28} />
          <h1 className='text-lg font-semibold'>Change your password</h1>
        </div>

        {required && (
          <Banner variant='warning' testId='change-password-required'>
            Your account is using a temporary password. Set a new one to continue.
          </Banner>
        )}

        <form onSubmit={handleSubmit} className='space-y-4'>
          <Field label='Current password'>
            <input
              data-testid='current-password'
              type='password'
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              autoComplete='current-password'
              className={inputClass}
            />
          </Field>
          <Field label='New password' hint='At least 8 characters.'>
            <input
              data-testid='new-password'
              type='password'
              value={next}
              onChange={(e) => setNext(e.target.value)}
              autoComplete='new-password'
              className={inputClass}
            />
          </Field>
          <Field label='Confirm new password'>
            <input
              data-testid='confirm-password'
              type='password'
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete='new-password'
              className={inputClass}
            />
          </Field>

          {error && (
            <p data-testid='change-password-error' className='text-sm text-red-400'>
              {error}
            </p>
          )}

          <button
            data-testid='change-password-submit'
            type='submit'
            disabled={saving}
            className='w-full rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-indigo-500 disabled:opacity-60'
          >
            {saving ? 'Saving…' : 'Change password'}
          </button>
        </form>
      </div>
    </div>
  );
}
