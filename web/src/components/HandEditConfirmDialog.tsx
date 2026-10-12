import { useHandEditedWizardRevision } from '@/components/RestoreWizardVersion';
import { Modal, ModalActions } from '@/components/ui/Modal';
import type { Pipeline, PipelineRevision } from '@/gen/shepherd/mgmt/v1/pipeline_pb';

/**
 * Whether saving `contents` would hand-edit a wizard pipeline that still
 * follows its wizard: one with no hand edit since its wizard last wrote it
 * (HandEditedBadge's query, shared), whose text this save changes. A save
 * that leaves the text as the wizard wrote it asks nothing.
 */
export function useWouldHandEdit(
  pipeline: Pipeline | undefined,
  revisions: readonly PipelineRevision[],
  orgId: string,
  queryOrgId: string,
  contents: string,
): boolean {
  const handEdited = useHandEditedWizardRevision(pipeline, revisions, orgId, queryOrgId);
  return pipeline?.source === 'wizard' && handEdited == null && contents !== pipeline.contents;
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
