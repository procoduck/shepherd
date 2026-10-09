import { useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { Modal } from '@/components/ui/Modal';
import type { Pipeline } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { formError, sentenceCase } from '@/lib/formError';

/**
 * Whether a save was refused because the pipeline changed since it was
 * loaded (F1): UpdatePipeline answers `aborted` when the expected_revision it
 * was sent is no longer the pipeline's current revision.
 */
export function isSaveConflict(e: unknown): boolean {
  return !!e && toApiError(e).code === 'aborted';
}

/**
 * The choice a refused save leaves (F1): load the server's newer copy and
 * drop the local edits, or — offered only when `onOverwrite` is given, i.e.
 * to someone who may write the pipeline — save the local copy over it anyway.
 * Overwriting is never a default: this dialog is the explicit confirmation,
 * and Reload is its primary action.
 */
export function SaveConflictDialog({
  error,
  onReload,
  onOverwrite,
  onClose,
  pending,
}: {
  /** The refusal, whose server text names both revisions. */
  error: unknown;
  onReload: () => void;
  /** Absent: no overwrite is offered. */
  onOverwrite?: () => void;
  onClose: () => void;
  pending?: boolean;
}) {
  const message = toApiError(error).message;
  return (
    <Modal
      title='This pipeline changed since you loaded it'
      onClose={onClose}
      testId='save-conflict'
    >
      <div className='space-y-4'>
        <p className='text-sm text-muted' data-testid='save-conflict-message'>
          {message
            ? `${sentenceCase(message)}.`
            : 'Someone else saved a newer version while you were editing.'}
        </p>
        <p className='text-sm text-muted'>
          Reload to see the current version — your unsaved changes here are discarded.
          {onOverwrite &&
            ' Or overwrite it with your version, replacing whatever changed since you loaded it.'}
        </p>
        <div className='flex justify-end gap-2'>
          <button
            type='button'
            onClick={onClose}
            className='rounded border border-border px-3 py-1.5 text-xs font-medium text-muted hover:text-zinc-100'
          >
            Cancel
          </button>
          {onOverwrite && (
            <button
              type='button'
              onClick={onOverwrite}
              disabled={pending}
              className='rounded border border-red-500/50 px-3 py-1.5 text-xs font-medium text-red-400 hover:bg-red-500/10 disabled:opacity-50'
              data-testid='save-conflict-overwrite'
            >
              Overwrite with my version
            </button>
          )}
          <button
            type='button'
            onClick={onReload}
            disabled={pending}
            className='rounded bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
            data-testid='save-conflict-reload'
          >
            Reload
          </button>
        </div>
      </div>
    </Modal>
  );
}

/**
 * The text editor's conflict dialog: Reload fetches the server's current
 * copy, makes it the page's cached pipeline and hands it to `onReloaded`
 * (which loads it into the form, discarding the local edits).
 */
export function EditorSaveConflict({
  error,
  id,
  orgId,
  queryOrgId,
  saving,
  onReloaded,
  onOverwrite,
  onClose,
}: {
  error: unknown;
  id: string;
  /** The pipeline's own org, which the RPC is sent to. */
  orgId: string;
  /** The page's org, which its query keys use. */
  queryOrgId: string;
  saving: boolean;
  onReloaded: (fresh: Pipeline) => void;
  onOverwrite?: () => void;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [reloading, setReloading] = useState(false);
  const reload = async () => {
    setReloading(true);
    try {
      const fresh = await clients.pipeline.getPipeline({ orgId, id });
      qc.setQueryData(['pipeline', queryOrgId, id], fresh);
      qc.invalidateQueries({ queryKey: ['revisions', queryOrgId, id] });
      onReloaded(fresh);
    } catch (e) {
      toast.error(formError(e, 'Could not load the current version') ?? '');
    } finally {
      setReloading(false);
    }
  };
  return (
    <SaveConflictDialog
      error={error}
      pending={reloading || saving}
      onClose={onClose}
      onReload={reload}
      onOverwrite={onOverwrite}
    />
  );
}
