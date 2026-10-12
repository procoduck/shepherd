import { Modal, ModalActions } from '@/components/ui/Modal';
import type { Pipeline } from '@/gen/shepherd/mgmt/v1/pipeline_pb';

/**
 * Whether saving `contents` would hand-edit a wizard pipeline that still
 * follows its wizard (Pipeline.follows_wizard, #301: the server's own
 * hand-edit verdict) by changing its text. A save that leaves the text as it
 * is asks nothing, and neither does one to a pipeline already hand-edited:
 * it stopped following destination changes with its first hand edit.
 */
export function wouldHandEdit(pipeline: Pipeline | undefined, contents: string): boolean {
  return pipeline?.source === 'wizard' && pipeline.followsWizard && contents !== pipeline.contents;
}

/**
 * Asked before a Save hand-edits a wizard pipeline's text (2026-10-12
 * walkthrough): the edit stops it following destination changes, and until
 * now only the landing banner — gone after one dismissal — said so.
 */
export function HandEditConfirmDialog({
  pending,
  error,
  onCancel,
  onConfirm,
}: {
  pending: boolean;
  error: string | null;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal title='Save a hand edit?' onClose={onCancel} testId='hand-edit-dialog'>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onConfirm();
        }}
        className='space-y-4'
      >
        <p className='text-sm text-muted'>
          A wizard generated this pipeline. Saving your edit to its text stops it following
          destination changes: Shepherd no longer re-renders it when a destination it ships to
          changes. Restore last wizard version undoes the edit; Detach from wizard makes it a plain
          pipeline.
        </p>
        <ModalActions
          onCancel={onCancel}
          submitLabel='Save hand edit'
          pendingLabel='Saving…'
          pending={pending}
          error={error}
        />
      </form>
    </Modal>
  );
}
