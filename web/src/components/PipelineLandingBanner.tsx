import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { Info, Workflow, X } from 'lucide-react';

/** The flow that just navigated to a pipeline's editor page — the `from`
 *  search param on `/pipelines/$id` (#251). */
export type PipelineLandedFrom = 'visual' | 'wizard';

/**
 * Says where a builder Save or a finished wizard has landed the user, and
 * why (#251): both open the pipeline's page, which is the text editor, and
 * used to do it without a word — after a builder Save it looked as if the
 * graph had been thrown away. Renders nothing unless the URL carries `from`;
 * dismissing it drops the param.
 */
export function PipelineLandingBanner({ pipelineId }: { pipelineId: string }) {
  const { from } = useSearch({ strict: false }) as { from?: PipelineLandedFrom };
  const navigate = useNavigate();
  if (from !== 'visual' && from !== 'wizard') return null;
  return (
    <div
      data-testid='editor-landing-banner'
      role='status'
      className='flex items-start gap-2 border-b border-indigo-500/30 bg-indigo-500/10 px-3 py-2 text-xs text-zinc-200 shrink-0'
    >
      <Info size={14} className='mt-px shrink-0 text-indigo-400' />
      <p className='flex-1'>
        {from === 'visual' ? (
          <>
            <strong>Saved from the visual builder.</strong> You're now in the text editor, which
            shows the Alloy config generated from your graph. To keep editing the graph,{' '}
            <Link
              to='/pipelines/$id/visual'
              params={{ id: pipelineId }}
              data-testid='editor-landing-open-visual'
              className='font-medium text-indigo-400 underline hover:text-indigo-300'
            >
              reopen it in the visual builder
            </Link>
            .
          </>
        ) : (
          <>
            <strong>Pipeline created from the wizard.</strong> You're now in the text editor,
            showing the Alloy config the wizard generated. It stays linked to the wizard: Shepherd
            re-renders it when a destination it ships to changes, and a hand edit here blocks that —
            use Detach from wizard first if you want to edit it by hand. A hand edit can be undone
            with Restore last wizard version, which this page offers once there is one.
          </>
        )}
      </p>
      <button
        type='button'
        data-testid='editor-landing-dismiss'
        aria-label='Dismiss'
        onClick={() =>
          navigate({ to: '/pipelines/$id', params: { id: pipelineId }, search: {}, replace: true })
        }
        className='shrink-0 text-muted hover:text-zinc-200'
      >
        <X size={14} />
      </button>
    </div>
  );
}

/** The way back into the builder from a visual pipeline's text editor.
 *  `readOnly`: the builder is gated on the org role, so for someone who may
 *  edit this pipeline only as a member of its owning team it opens read-only —
 *  the link says so before they click, and a note says what editing the
 *  generated text means — its first line says "do not edit by hand", and for
 *  them the text is the only way to edit. */
export function OpenInVisualBuilder({
  pipelineId,
  readOnly = false,
}: {
  pipelineId: string;
  readOnly?: boolean;
}) {
  const link = (
    <Link
      to='/pipelines/$id/visual'
      params={{ id: pipelineId }}
      data-testid='editor-open-visual'
      title={
        readOnly ? 'Opens read-only — the visual builder needs the org editor role' : undefined
      }
      className='flex items-center gap-1 text-indigo-400 hover:text-indigo-300'
    >
      <Workflow size={12} /> Open in visual builder
      {readOnly && <span className='text-muted-2'>(read-only)</span>}
    </Link>
  );
  if (!readOnly) return link;
  return (
    <>
      {link}
      <p
        data-testid='editor-visual-text-only-note'
        className='rounded-md border border-border bg-card/40 px-2 py-1.5 text-muted'
      >
        As a member of the owning team you can edit and save this text here; the visual builder
        needs the org editor role, so it opens read-only for you. The builder keeps its own graph:
        the next save from it regenerates this text and replaces edits made here.
      </p>
    </>
  );
}
