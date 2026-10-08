import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import type { Pipeline, PipelineRevision } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { lastWizardRevision } from '@/lib/wizardRevisions';

/** Matcher lists as sets: order carries no meaning. */
function sameMatchers(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false;
  const sb = [...b].sort();
  return [...a].sort().every((m, i) => m === sb[i]);
}

/** The destination names a wizard state names (its `*_dest_name` answers). */
function destinationNames(state: PipelineRevision['wizardState']): string[] {
  if (!state) return [];
  return Object.entries(state)
    .filter(([k, v]) => k.endsWith('_dest_name') && typeof v === 'string' && v !== '')
    .map(([, v]) => v as string);
}

/**
 * What else a restore changes, said before it happens. RestoreRevision puts
 * back the revision's contents AND its matchers and enabled state, so a
 * restore — including "Restore last wizard version" (M4) — can change which
 * collectors receive the pipeline, or whether it is served at all.
 * `detail` is the revision in full (GetRevision); until it loads only the
 * notes that need no revision data show.
 */
export function RestoreRevisionNotes({
  orgId,
  pipeline,
  revisions,
  revision,
  detail,
}: {
  /** The page's org: the destinations query shares the Destinations page's key. */
  orgId: string;
  pipeline: Pipeline;
  revisions: readonly PipelineRevision[];
  revision: number;
  detail: PipelineRevision | undefined;
}) {
  const isLastWizard = lastWizardRevision(revisions, pipeline.source) === revision;
  // A wizard revision that names a destination since deleted restores text
  // that ships to it, but no destination change will ever reach it again.
  const named = pipeline.source === 'wizard' ? destinationNames(detail?.wizardState) : [];
  const { data: dests } = useQuery({
    queryKey: ['destinations', orgId],
    queryFn: () => clients.destination.listDestinations({ orgId }),
    enabled: !!orgId && named.length > 0,
  });
  const missing = dests ? named.filter((n) => !dests.items.some((d) => d.name === n)) : [];
  return (
    <>
      {isLastWizard && (
        <p data-testid='restore-wizard-note' className='text-sm text-muted'>
          It is the last version this pipeline&rsquo;s wizard generated: restoring it drops the hand
          edits since, so a destination change can regenerate it again. If a destination change is
          still refused afterwards, detach the pipeline from the wizard or delete it.
        </p>
      )}
      {missing.length > 0 && (
        <p data-testid='restore-missing-destination' className='text-sm text-amber-400'>
          This version writes to destination{missing.length > 1 ? 's' : ''}{' '}
          {missing.map((n) => `“${n}”`).join(', ')}, which no longer exist
          {missing.length > 1 ? '' : 's'} — it will not follow destination changes.
        </p>
      )}
      {detail && !sameMatchers(detail.matchers, pipeline.matchers) && (
        <div data-testid='restore-matchers-change' className='text-sm text-amber-400'>
          <p>
            Restoring it also puts back this revision&rsquo;s matchers, which changes which
            collectors receive the pipeline:
          </p>
          <p className='mt-1 font-mono text-xs'>
            now: {pipeline.matchers.length ? pipeline.matchers.join(', ') : '(none)'}
          </p>
          <p className='font-mono text-xs'>
            restored: {detail.matchers.length ? detail.matchers.join(', ') : '(none)'}
          </p>
        </div>
      )}
      {detail && detail.enabled !== pipeline.enabled && (
        <p data-testid='restore-enabled-change' className='text-sm text-amber-400'>
          {detail.enabled
            ? 'This revision was saved while the pipeline was enabled, so restoring it enables the pipeline — it will be served to matching collectors.'
            : 'This revision was saved while the pipeline was disabled, so restoring it disables the pipeline — it stops being served.'}
        </p>
      )}
      {pipeline.source === 'git' && (
        <p data-testid='restore-git-warning' className='text-sm text-amber-400'>
          This pipeline is managed by Git. The restore is written as a new revision now, but the
          next git sync will overwrite it — change the file in the repository to make it stick.
        </p>
      )}
    </>
  );
}
