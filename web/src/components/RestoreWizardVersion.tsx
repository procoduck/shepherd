import { useQuery } from '@tanstack/react-query';
import { RotateCcw } from 'lucide-react';
import { clients } from '@/api/transport';
import { Banner } from '@/components/ui/Banner';
import type { Pipeline, PipelineRevision } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { lastWizardRevision } from '@/lib/wizardRevisions';

/** The newest revision `pipeline`'s wizard wrote, unless it is already the current one. */
function restorableWizardRevision(
  pipeline: Pipeline | undefined,
  revisions: readonly PipelineRevision[],
): number | null {
  if (!pipeline) return null;
  const last = lastWizardRevision(revisions, pipeline.source);
  const current = revisions.reduce((max, r) => Math.max(max, r.revision), 0);
  return last != null && last !== current ? last : null;
}

/**
 * "Restore last wizard version" (M4, 2026-10-08 walkthrough). A wizard
 * pipeline whose text was edited by hand blocks every change to a
 * destination it ships to, and the refusal tells the person to restore its
 * last wizard-generated revision, detach it, or delete it. This makes the
 * first of those one click: shown while the pipeline's saved text differs
 * from the newest revision its wizard wrote (lib/wizardRevisions), it opens
 * the page's ordinary restore confirmation for that revision — the existing
 * RestoreRevision, nothing new on the server.
 *
 * The page passes no pipeline to a caller who cannot write (UpdatePipeline's
 * rule, which RestoreRevision shares), and it renders nothing for one that is
 * not a wizard pipeline with such a revision.
 */
export function RestoreWizardVersion({
  pipeline,
  revisions,
  orgId,
  queryOrgId,
  onRestore,
}: {
  /** Undefined while it loads, and for a caller who cannot write. */
  pipeline: Pipeline | undefined;
  revisions: readonly PipelineRevision[];
  /** The pipeline's own org, which the RPC is sent to. */
  orgId: string;
  /** The page's org, which its query keys use (shares the diff view's cache). */
  queryOrgId: string;
  onRestore: (revision: number) => void;
}) {
  const revision = restorableWizardRevision(pipeline, revisions);
  const { data: wizardRevision } = useQuery({
    queryKey: ['revision', queryOrgId, pipeline?.id, revision],
    queryFn: () => clients.pipeline.getRevision({ orgId, id: pipeline!.id, revision: revision! }),
    enabled: revision != null,
  });
  if (revision == null || !wizardRevision || wizardRevision.contents === pipeline?.contents) {
    return null;
  }
  return (
    <Banner variant='warning' testId='restore-wizard-version'>
      <p>
        This pipeline was edited by hand since its wizard last generated it (revision #{revision}).
        A change to a destination it ships to is refused until you restore that version, detach it
        from the wizard, or delete it.
      </p>
      <button
        type='button'
        onClick={() => onRestore(revision)}
        className='mt-2 flex items-center gap-1 font-medium underline underline-offset-2 hover:opacity-80'
      >
        <RotateCcw size={12} /> Restore last wizard version
      </button>
    </Banner>
  );
}
