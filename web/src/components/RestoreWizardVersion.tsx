import { useQuery } from '@tanstack/react-query';
import { RotateCcw } from 'lucide-react';
import { useId } from 'react';
import { clients } from '@/api/transport';
import { Banner } from '@/components/ui/Banner';
import { toneClass } from '@/components/ui/statusTone';
import type { Pipeline, PipelineRevision } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { lastWizardRevision } from '@/lib/wizardRevisions';

/**
 * Whether `pipeline` is a wizard pipeline edited by hand since its wizard last
 * wrote it, so a destination change no longer regenerates it. This is the
 * server's own verdict, Pipeline.follows_wizard (#301): the check
 * UpdateDestination makes before it regenerates a pipeline or refuses it as
 * hand-edited.
 */
export function isHandEditedWizardPipeline(pipeline: Pipeline | undefined): boolean {
  return pipeline?.source === 'wizard' && !pipeline.followsWizard;
}

/**
 * The newest revision a hand-edited wizard `pipeline`'s wizard wrote, unless
 * it is already the current one: the revision whose restore re-attaches the
 * pipeline to its wizard (RestoreRevision puts back that revision's render
 * fingerprint). Null for a pipeline that still follows its wizard.
 */
function restorableWizardRevision(
  pipeline: Pipeline | undefined,
  revisions: readonly PipelineRevision[],
): number | null {
  if (!pipeline || !isHandEditedWizardPipeline(pipeline)) return null;
  const last = lastWizardRevision(revisions, pipeline.source);
  const current = revisions.reduce((max, r) => Math.max(max, r.revision), 0);
  return last != null && last !== current ? last : null;
}

/**
 * #301: the lasting mark that a wizard pipeline was edited by hand and stopped
 * following destination changes, in the pipeline's header and in the
 * pipelines list. The landing banner says so once; this stays for as long as
 * it is true, for every reader (the restore offer below is only for whoever
 * may write). `revision`, when the page knows it, is the wizard's last version.
 */
export function HandEditedBadge({
  pipeline,
  revision,
}: {
  pipeline: Pipeline | undefined;
  revision?: number | null;
}) {
  const descId = useId();
  if (!isHandEditedWizardPipeline(pipeline)) return null;
  const since =
    revision != null
      ? `since its wizard last generated it (revision #${revision})`
      : 'since its wizard last generated it';
  const description = `Edited by hand ${since}: it no longer follows destination changes.`;
  return (
    <>
      <span
        data-testid='pipeline-hand-edited-badge'
        aria-describedby={descId}
        title={description}
        className={`inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium ${toneClass('warn')}`}
      >
        Hand-edited
      </span>
      <span id={descId} className='sr-only'>
        {description}
      </span>
    </>
  );
}

/**
 * "Restore last wizard version" (M4, 2026-10-08 walkthrough). A wizard
 * pipeline whose text was edited by hand blocks every change to a
 * destination it ships to, and the refusal tells the person to restore its
 * last wizard-generated revision, detach it, or delete it. This makes the
 * first of those one click: shown while the pipeline no longer follows its
 * wizard (follows_wizard, #301) and has an older revision its wizard wrote
 * (lib/wizardRevisions), it opens the page's ordinary restore confirmation
 * for that revision — the existing RestoreRevision, nothing new on the server.
 * It waits for that revision's contents, which the confirmation's warnings
 * need (the query is shared with the revision diff view; GetRevision is
 * org-reader).
 *
 * The page passes no pipeline to a caller who cannot write (UpdatePipeline's
 * rule, which RestoreRevision shares), and it renders nothing for one that is
 * not a hand-edited wizard pipeline with such a revision.
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
  if (revision == null || !wizardRevision) return null;
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
