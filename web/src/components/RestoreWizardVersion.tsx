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
 * The newest revision a wizard `pipeline` that no longer follows its wizard
 * had written by that wizard: the revision whose restore re-attaches it
 * (RestoreRevision puts back that revision's render fingerprint). It can be
 * the current revision: a pipeline written before Shepherd recorded what its
 * wizard wrote (migration 0030), by a wizard whose output has changed since,
 * reads as no longer following although nobody edited it, and restoring its
 * own current revision re-attaches it. Null for a pipeline that still
 * follows its wizard.
 */
function restorableWizardRevision(
  pipeline: Pipeline | undefined,
  revisions: readonly PipelineRevision[],
): number | null {
  if (!pipeline || !isHandEditedWizardPipeline(pipeline)) return null;
  return lastWizardRevision(revisions, pipeline.source);
}

/** The newest revision in `revisions`: what the pipeline is now. */
function currentRevisionOf(revisions: readonly PipelineRevision[]): number {
  return revisions.reduce((max, r) => Math.max(max, r.revision), 0);
}

/**
 * #301: the lasting mark that a wizard pipeline stopped following destination
 * changes, in the pipeline's header and in the pipelines list. The landing
 * banner says so once; this stays for as long as it is true, for every reader
 * (the restore offer below is only for whoever may write).
 *
 * The header passes the pipeline's revisions. When its current revision is
 * the wizard's own, nobody edited it — an older version of the wizard
 * generated it (see restorableWizardRevision) — so the badge does not say
 * "Hand-edited". The list passes none and cannot tell the two cases apart,
 * so it says only what is always true (review of #332).
 */
function badgeWording(
  pipeline: Pipeline,
  revisions: readonly PipelineRevision[] | undefined,
): { label: string; description: string } {
  if (!revisions) {
    return {
      label: 'Not following wizard',
      description: 'Destination changes no longer regenerate this pipeline; open it to see why.',
    };
  }
  const last = lastWizardRevision(revisions, pipeline.source);
  if (last != null && last === currentRevisionOf(revisions)) {
    return {
      label: 'Not following wizard',
      description: `An older version of its wizard generated this text (revision #${last}): it no longer follows destination changes until that revision is restored.`,
    };
  }
  const since = last != null ? ` (revision #${last})` : '';
  return {
    label: 'Hand-edited',
    description: `Edited by hand since its wizard last generated it${since}: it no longer follows destination changes.`,
  };
}

export function HandEditedBadge({
  pipeline,
  revisions,
}: {
  pipeline: Pipeline | undefined;
  revisions?: readonly PipelineRevision[];
}) {
  const descId = useId();
  if (!pipeline || !isHandEditedWizardPipeline(pipeline)) return null;
  const { label, description } = badgeWording(pipeline, revisions);
  return (
    <>
      <span
        data-testid='pipeline-hand-edited-badge'
        aria-describedby={descId}
        title={description}
        className={`inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium ${toneClass('warn')}`}
      >
        {label}
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
 * wizard (follows_wizard, #301) and has a revision its wizard wrote
 * (lib/wizardRevisions) — the current one included, see
 * restorableWizardRevision — it opens the page's ordinary restore confirmation
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
        {revision === currentRevisionOf(revisions)
          ? `An older version of this pipeline's wizard generated its text (revision #${revision}), and the wizard renders it differently today.`
          : `This pipeline was edited by hand since its wizard last generated it (revision #${revision}).`}{' '}
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
