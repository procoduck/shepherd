import { type ReactNode, useState } from 'react';

/**
 * Shared modal shell for the admin pages (AdminOrgsPage, AdminClustersPage,
 * AdminTokensPage). Matches the overlay/panel styling DestinationsPage
 * established (fixed inset-0 backdrop + centered bordered panel) so the
 * admin surface and the rest of the app stay visually consistent.
 */
let modalSeq = 0;

export function AdminModal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const [titleId] = useState(() => `admin-modal-title-${++modalSeq}`);
  return (
    <div className='fixed inset-0 z-50 flex items-center justify-center bg-black/60'>
      <div
        role='dialog'
        aria-modal='true'
        aria-labelledby={titleId}
        className='w-full max-w-md rounded-xl border border-border bg-background p-6 shadow-2xl'
      >
        <div className='mb-4 flex items-center justify-between'>
          <h2 id={titleId} className='text-base font-semibold'>
            {title}
          </h2>
          <button
            type='button'
            onClick={onClose}
            aria-label='Close'
            className='text-muted-3 hover:text-zinc-200'
          >
            ×
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}

/** Shared Cancel/Submit button row for admin modal forms. */
export function AdminModalActions({
  onCancel,
  submitLabel,
  pendingLabel,
  pending,
  danger,
}: {
  onCancel: () => void;
  submitLabel: string;
  pendingLabel: string;
  pending: boolean;
  danger?: boolean;
}) {
  return (
    <div className='flex justify-end gap-2 pt-2'>
      <button
        type='button'
        onClick={onCancel}
        className='px-4 py-1.5 text-sm text-muted hover:text-zinc-200'
      >
        Cancel
      </button>
      <button
        type='submit'
        disabled={pending}
        className={`rounded-md px-4 py-1.5 text-sm text-white disabled:opacity-50 ${
          danger ? 'bg-red-600 hover:bg-red-500' : 'bg-indigo-600 hover:bg-indigo-500'
        }`}
      >
        {pending ? pendingLabel : submitLabel}
      </button>
    </div>
  );
}
