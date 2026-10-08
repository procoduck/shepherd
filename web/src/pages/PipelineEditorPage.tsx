import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from '@tanstack/react-router';
import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  PlayCircle,
  Save,
  Wand2,
  XCircle,
} from 'lucide-react';
import { useRef, useState } from 'react';
import { toast } from 'sonner';
import { clients } from '@/api/transport';
import { DetachFromWizard } from '@/components/DetachFromWizard';
import { PipelineActions } from '@/components/PipelineActions';
import { OpenInVisualBuilder, PipelineLandingBanner } from '@/components/PipelineLandingBanner';
import { PipelineMatchers } from '@/components/PipelineMatchers';
import { PipelineMatchPreview } from '@/components/PipelineMatchPreview';
import { PipelineOwner } from '@/components/PipelineOwner';
import { RevisionHistory } from '@/components/RevisionHistory';
import { Input } from '@/components/ui/Field';
import { FormError } from '@/components/ui/FormError';
import { Modal, ModalActions } from '@/components/ui/Modal';
import { diffStats } from '@/editor/diffStats';
import { AlloyEditor, RevisionDiff } from '@/editor/LazyAlloyEditor';
import { useCanAdminister, useCanWrite, useOrgId } from '@/hooks/useOrg';
import { type PipelineFormValues, usePipelineForm } from '@/hooks/usePipelineForm';
import { formError } from '@/lib/formError';

export function PipelineEditorPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const isNew = !id;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const orgId = useOrgId();
  const canWrite = useCanWrite();
  // Gates the owning-team picker: SetPipelineOwner is org admin only.
  const canAdminister = useCanAdminister();

  const [selectedRevision, setSelectedRevision] = useState<number | null>(null);
  const [confirmingRestore, setConfirmingRestore] = useState(false);

  const { data: pipeline } = useQuery({
    queryKey: ['pipeline', orgId, id],
    queryFn: () => clients.pipeline.getPipeline({ orgId, id: id! }),
    enabled: !!id && !!orgId,
    // Every landing here reads the server copy, whatever the cache holds:
    // other surfaces change pipelines too (the visual builder, a wizard, a
    // destination change re-rendering wizard pipelines server-side), and a
    // copy still inside staleTime would otherwise be shown — and saved back.
    refetchOnMount: 'always',
  });
  // `||`, not `??`: an empty org id on the pipeline must fall back to the
  // selected org, not disable every query keyed on it.
  const pipelineOrgId = pipeline?.orgId || orgId;

  const { data: revisionsData } = useQuery({
    queryKey: ['revisions', orgId, id],
    queryFn: () => clients.pipeline.listRevisions({ orgId: pipelineOrgId, id: id! }),
    enabled: !!id && !!pipelineOrgId,
  });
  const revisions = revisionsData?.items ?? [];
  // The newest revision is what the pipeline already is — every content,
  // matcher, restore and detach change writes one — so restoring it would be
  // a no-op revision (#252). Pipeline.revision is not populated by the
  // server, hence the max over the history.
  const currentRevision = revisions.reduce((max, r) => Math.max(max, r.revision), 0);

  // GetRevision is org-reader, so any signed-in viewer can open a diff —
  // only Restore is gated on canWrite below.
  const { data: revisionDetail } = useQuery({
    queryKey: ['revision', orgId, id, selectedRevision],
    queryFn: () =>
      clients.pipeline.getRevision({
        orgId: pipelineOrgId,
        id: id!,
        revision: selectedRevision!,
      }),
    enabled: !!id && !!pipelineOrgId && selectedRevision != null,
  });

  // Form state, its server re-sync (H1) and validation: see the hook.
  const {
    name,
    setName,
    contents,
    setContents,
    matchers,
    setMatchers,
    diagnostics,
    blockingErrors,
    replaceIsEdit,
    skippedStages,
    validating,
    validate,
    replaceContents,
    loadForm,
    markSaved,
  } = usePipelineForm({ pipeline, orgId, canWrite });

  // Format runs the server-side `alloy fmt` equivalent and replaces the buffer
  // with the canonical form, then re-validates it. Unparseable input comes back
  // as an error (the editor's live diagnostics already show why), so the buffer
  // is left untouched.
  const formatMutation = useMutation({
    mutationFn: () => clients.pipeline.formatPipeline({ orgId, contents }),
    onSuccess: (result) => {
      replaceContents(result.formatted, true);
      validate(result.formatted);
    },
  });

  const submitted = useRef<PipelineFormValues>({ name, contents, matchers });
  const saveMutation = useMutation({
    mutationFn: () => {
      submitted.current = { name, contents, matchers: [...matchers] };
      const body = { orgId, name, contents, matchers };
      return isNew
        ? clients.pipeline.createPipeline(body)
        : clients.pipeline.updatePipeline({ ...body, id: id! });
    },
    onSuccess: (p) => {
      toast.success(isNew ? 'Pipeline created' : 'Pipeline saved');
      markSaved(p.id, submitted.current);
      qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
      if (!isNew) {
        // Mirror restoreMutation's invalidation set below so the new
        // revision and "Updated by" show up right away.
        qc.invalidateQueries({ queryKey: ['pipeline', orgId, id] });
        qc.invalidateQueries({ queryKey: ['revisions', orgId, id] });
      }
      if (isNew) navigate({ to: '/pipelines/$id', params: { id: p.id } });
    },
  });

  const restoreMutation = useMutation({
    mutationFn: (revision: number) =>
      clients.pipeline.restoreRevision({ orgId: pipelineOrgId, id: id!, revision }),
    onSuccess: (p, revision) => {
      toast.success(`Restored revision #${revision}`);
      // The server copy wins a restore: load it into the form (which also
      // makes it the snapshot later refetches compare against).
      loadForm(p, true);
      qc.invalidateQueries({ queryKey: ['pipeline', orgId, id] });
      qc.invalidateQueries({ queryKey: ['revisions', orgId, id] });
      qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
      setConfirmingRestore(false);
      setSelectedRevision(null);
    },
  });
  const closeRestore = () => {
    setConfirmingRestore(false);
    restoreMutation.reset();
  };

  // Save and Format refusals, shown above the editor (#249).
  const editorError =
    formError(saveMutation.error, 'Save failed') ??
    formError(formatMutation.error, 'Cannot format — fix the syntax errors first');

  const hasErrors = diagnostics.length > 0;
  // Read-only for viewers; admins and editors both author (see useCanWrite)
  // and for git-managed pipelines (git is the source of truth).
  const readOnly = !canWrite || pipeline?.source === 'git';

  return (
    <div className='flex h-[calc(100vh-7rem)] gap-0'>
      {/* Left pane */}
      <div className='w-[380px] shrink-0 border-r border-border overflow-y-auto p-6 space-y-5'>
        <div className='space-y-3'>
          <h1 className='text-xl font-semibold break-words'>
            {isNew ? 'New pipeline' : (pipeline?.name ?? 'Pipeline')}
          </h1>
          {!isNew && pipeline && (
            <PipelineActions
              pipeline={pipeline}
              orgId={pipelineOrgId}
              queryOrgId={orgId}
              canWrite={canWrite}
            />
          )}
        </div>

        <div className='space-y-1'>
          <label className='text-xs font-medium text-muted'>Name</label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={readOnly}
            className='focus:outline-none focus:ring-1 focus:ring-indigo-500'
            placeholder='my-pipeline'
          />
        </div>

        <PipelineMatchers
          matchers={matchers}
          onChange={setMatchers}
          orgId={orgId}
          readOnly={readOnly}
        />

        {!isNew && pipeline && (
          <PipelineMatchPreview pipeline={pipeline} orgId={pipelineOrgId} queryOrgId={orgId} />
        )}

        {pipeline?.source === 'git' && (
          <div className='rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-400'>
            Managed by Git — read only
          </div>
        )}

        {!isNew && revisions.length > 0 && (
          <RevisionHistory
            revisions={revisions}
            currentRevision={currentRevision}
            onView={setSelectedRevision}
          />
        )}

        {pipeline && (
          <div className='text-xs text-muted-2 space-y-1'>
            <p>
              Source: <span className='text-zinc-300'>{pipeline.source}</span>
            </p>
            {pipeline.source === 'visual' && <OpenInVisualBuilder pipelineId={pipeline.id} />}
            {canWrite && pipeline.source === 'wizard' && (
              <DetachFromWizard pipeline={pipeline} orgId={pipelineOrgId} queryOrgId={orgId} />
            )}
            <p>
              Updated by: <span className='text-zinc-300'>{pipeline.updatedBy}</span>
            </p>
            <PipelineOwner
              pipeline={pipeline}
              orgId={pipelineOrgId}
              queryOrgId={orgId}
              canAdminister={canAdminister}
            />
          </div>
        )}
      </div>

      {/* Right pane */}
      <div className='flex flex-1 flex-col overflow-hidden'>
        {id && <PipelineLandingBanner pipelineId={id} />}
        {selectedRevision != null ? (
          <>
            {/* Diff header — replaces the editor toolbar while a revision is
                selected. GetRevision is org-reader, so a reader can reach
                this; Restore is gated on canWrite, same as UpdatePipeline. */}
            <div className='h-11 border-b border-border px-3 flex items-center justify-between shrink-0'>
              <div className='flex items-center gap-3 text-xs'>
                <button
                  onClick={() => setSelectedRevision(null)}
                  className='flex items-center gap-1 text-muted hover:text-zinc-200'
                >
                  <ArrowLeft size={13} /> Back to editor
                </button>
                <span className='text-muted-2'>
                  Revision #{selectedRevision} vs current
                  {revisionDetail && (
                    <span className='ml-1'>
                      (
                      <span className='text-emerald-500'>
                        +{diffStats(revisionDetail.contents, contents).added}
                      </span>{' '}
                      <span className='text-red-400'>
                        −{diffStats(revisionDetail.contents, contents).removed}
                      </span>
                      )
                    </span>
                  )}
                </span>
              </div>
              {selectedRevision === currentRevision ? (
                <span className='text-xs text-muted-2' data-testid='current-revision-note'>
                  This is the current revision
                </span>
              ) : (
                canWrite && (
                  <button
                    onClick={() => setConfirmingRestore(true)}
                    disabled={!revisionDetail}
                    className='rounded bg-indigo-600 px-3 py-1 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
                    data-testid='restore-btn'
                  >
                    Restore this revision
                  </button>
                )
              )}
            </div>

            <div className='flex-1 overflow-hidden'>
              {revisionDetail && (
                <RevisionDiff oldText={revisionDetail.contents} newText={contents} height='100%' />
              )}
            </div>
          </>
        ) : (
          <>
            {/* Toolbar */}
            <div className='h-11 border-b border-border px-3 flex items-center justify-between shrink-0'>
              <div className='flex items-center gap-2 text-xs' aria-live='polite'>
                {/* Readers get no indicator at all: their content is never
                    validated (see the effect above), so "No problems" would be
                    a claim nothing checked. */}
                {canWrite &&
                  (validating ? (
                    <span className='text-muted'>Validating…</span>
                  ) : hasErrors ? (
                    <span className='flex items-center gap-1 text-red-400'>
                      <XCircle size={14} /> {diagnostics.length} problem
                      {diagnostics.length > 1 ? 's' : ''}
                    </span>
                  ) : skippedStages.includes(2) ? (
                    <span
                      className='flex items-center gap-1 text-amber-400'
                      data-testid='validate-skipped-note'
                      title='The server has no validate.alloy_binary configured, so only the Alloy syntax was checked. Component and argument errors will not show here.'
                    >
                      <AlertTriangle size={14} /> Syntax checked — alloy validate skipped (no Alloy
                      binary configured)
                    </span>
                  ) : (
                    <span className='flex items-center gap-1 text-emerald-500'>
                      <CheckCircle2 size={14} /> No problems
                    </span>
                  ))}
              </div>
              {!readOnly && (
                <div className='flex items-center gap-2'>
                  <button
                    type='button'
                    onClick={() => validate(contents)}
                    disabled={validating || !contents.trim()}
                    className='flex items-center gap-1.5 rounded border border-border px-3 py-1 text-xs font-medium text-muted hover:text-zinc-100 disabled:opacity-50'
                    data-testid='validate-btn'
                  >
                    <PlayCircle size={13} /> Validate
                  </button>
                  <button
                    type='button'
                    onClick={() => {
                      saveMutation.reset();
                      formatMutation.mutate();
                    }}
                    disabled={formatMutation.isPending || !contents.trim()}
                    className='flex items-center gap-1.5 rounded border border-border px-3 py-1 text-xs font-medium text-muted hover:text-zinc-100 disabled:opacity-50'
                    data-testid='format-btn'
                  >
                    <Wand2 size={13} /> Format
                  </button>
                  <button
                    onClick={() => {
                      formatMutation.reset();
                      saveMutation.mutate();
                    }}
                    disabled={saveMutation.isPending || blockingErrors}
                    className='flex items-center gap-1.5 rounded bg-indigo-600 px-3 py-1 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
                  >
                    <Save size={13} /> Save
                  </button>
                </div>
              )}
            </div>

            {editorError && (
              <div className='border-b border-border p-2'>
                <FormError message={editorError} />
              </div>
            )}

            {/* Editor */}
            <div className='flex-1 overflow-hidden'>
              <AlloyEditor
                value={contents}
                onChange={setContents}
                readOnly={readOnly}
                diagnostics={diagnostics}
                replaceIsEdit={replaceIsEdit}
                height='100%'
              />
            </div>

            {/* Problems panel */}
            {hasErrors && (
              <div className='max-h-40 overflow-y-auto border-t border-border bg-background p-2 space-y-1'>
                {diagnostics.map((d, i) => (
                  <p key={i} className='font-mono text-xs text-red-400'>
                    {d.line}:{d.col} &nbsp; {d.message}
                  </p>
                ))}
              </div>
            )}
          </>
        )}
      </div>

      {confirmingRestore && selectedRevision != null && (
        <Modal title='Restore revision' onClose={closeRestore} testId='restore-dialog'>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              restoreMutation.mutate(selectedRevision);
            }}
            className='space-y-4'
          >
            <p className='text-sm text-muted'>
              Restore revision #{selectedRevision}? This creates a new revision from its contents,
              matchers and enabled state; the current text is kept in history.
            </p>
            {revisionDetail && pipeline && revisionDetail.enabled !== pipeline.enabled && (
              <p data-testid='restore-enabled-change' className='text-sm text-amber-400'>
                {revisionDetail.enabled
                  ? 'This revision was saved while the pipeline was enabled, so restoring it enables the pipeline — it will be served to matching collectors.'
                  : 'This revision was saved while the pipeline was disabled, so restoring it disables the pipeline — it stops being served.'}
              </p>
            )}
            {pipeline?.source === 'git' && (
              <p data-testid='restore-git-warning' className='text-sm text-amber-400'>
                This pipeline is managed by Git. The restore is written as a new revision now, but
                the next git sync will overwrite it — change the file in the repository to make it
                stick.
              </p>
            )}
            <ModalActions
              onCancel={closeRestore}
              submitLabel='Restore'
              pendingLabel='Restoring…'
              pending={restoreMutation.isPending}
              submitTestId='confirm-restore-btn'
              error={formError(restoreMutation.error, 'Restore failed')}
            />
          </form>
        </Modal>
      )}
    </div>
  );
}
