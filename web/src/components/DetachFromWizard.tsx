import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Unlink } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients } from '@/api/transport';
import { Modal, ModalActions } from '@/components/ui/Modal';
import type { Pipeline } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { formError } from '@/lib/formError';

/**
 * "Detach from wizard" (#262 follow-up): turns a wizard pipeline into an
 * ordinary editor pipeline, in place (PipelineService.DetachFromWizard). A
 * wizard pipeline is regenerated whenever a destination it ships to changes,
 * and a hand edit to one blocks that change; detaching keeps the text as it
 * is and takes the pipeline out of both. Rendered only for a wizard pipeline
 * and a caller who can write (the page passes canWrite), mirroring
 * UpdatePipeline's authorization.
 */
export function DetachFromWizard({
  pipeline,
  orgId,
  queryOrgId = orgId,
  onDetached,
}: {
  pipeline: Pipeline;
  /** The org the RPC runs against (the pipeline's own). */
  orgId: string;
  /**
   * The org id the page keys its queries on (useOrgId), when it differs from
   * the RPC's — as PipelineActions does: invalidating under the RPC org id
   * missed the editor's ['pipeline', orgId, id] entry entirely.
   */
  queryOrgId?: string;
  onDetached?: (p: Pipeline) => void;
}) {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const mutation = useMutation({
    mutationFn: () => clients.pipeline.detachFromWizard({ orgId, id: pipeline.id }),
    onSuccess: (p) => {
      toast.success('Detached from wizard');
      qc.invalidateQueries({ queryKey: ['pipeline', queryOrgId, pipeline.id] });
      qc.invalidateQueries({ queryKey: ['revisions', queryOrgId, pipeline.id] });
      qc.invalidateQueries({ queryKey: ['pipelines', queryOrgId] });
      setConfirming(false);
      onDetached?.(p);
    },
  });
  const close = () => {
    setConfirming(false);
    mutation.reset();
  };

  return (
    <>
      <button
        type='button'
        onClick={() => setConfirming(true)}
        className='flex items-center gap-1 text-xs text-muted hover:text-zinc-200'
        data-testid='detach-wizard-btn'
      >
        <Unlink size={12} /> Detach from wizard
      </button>
      {confirming && (
        <Modal title='Detach from wizard' onClose={close} testId='detach-wizard-dialog'>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              mutation.mutate();
            }}
            className='space-y-4'
          >
            <p className='text-sm text-muted'>
              Detach &ldquo;{pipeline.name}&rdquo; from its wizard? It becomes an ordinary pipeline
              with the same text, matchers, owner and history.
            </p>
            <p className='text-sm text-amber-400'>
              It will no longer follow destination changes: editing a destination it ships to will
              not update it, so a new URL or credential has to be edited in by hand. This cannot be
              undone.
            </p>
            <ModalActions
              onCancel={close}
              submitLabel='Detach'
              pendingLabel='Detaching…'
              pending={mutation.isPending}
              danger
              submitTestId='confirm-detach-btn'
              error={formError(mutation.error, 'Detach failed')}
            />
          </form>
        </Modal>
      )}
    </>
  );
}
