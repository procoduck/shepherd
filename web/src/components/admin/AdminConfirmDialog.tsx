import { AdminModal, AdminModalActions } from './AdminModal';

/** Shared yes/no confirmation dialog for destructive admin actions (delete org, revoke token, unclaim cluster). */
export function AdminConfirmDialog({
  title,
  body,
  confirmLabel,
  pendingLabel,
  pending,
  onConfirm,
  onCancel,
}: {
  title: string;
  body: string;
  confirmLabel: string;
  pendingLabel: string;
  pending: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <AdminModal title={title} onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onConfirm();
        }}
        className='space-y-4'
      >
        <p className='text-sm text-muted'>{body}</p>
        <AdminModalActions
          onCancel={onCancel}
          submitLabel={confirmLabel}
          pendingLabel={pendingLabel}
          pending={pending}
          danger
        />
      </form>
    </AdminModal>
  );
}
