import { timestampDate } from '@bufbuild/protobuf/wkt';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from '@tanstack/react-router';
import { ArrowLeft, CheckCircle2, ChevronDown, Save, XCircle } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { Input } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';
import { diffStats } from '@/editor/diffStats';
import { AlloyEditor, RevisionDiff } from '@/editor/LazyAlloyEditor';
import type { Diagnostic } from '@/gen/shepherd/mgmt/v1/common_pb';
import { useCanWrite, useOrgId } from '@/hooks/useOrg';

export function PipelineEditorPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const isNew = !id;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const orgId = useOrgId();
  const canWrite = useCanWrite();

  const [name, setName] = useState('');
  const [contents, setContents] = useState('');
  const [matchers, setMatchers] = useState<string[]>([]);
  const [newMatcher, setNewMatcher] = useState('');
  const [diagnostics, setDiagnostics] = useState<Diagnostic[]>([]);
  const [validating, setValidating] = useState(false);
  const [showRevisions, setShowRevisions] = useState(false);
  const [selectedRevision, setSelectedRevision] = useState<number | null>(null);
  const [confirmingRestore, setConfirmingRestore] = useState(false);

  const { data: pipeline, refetch: refetchPipeline } = useQuery({
    queryKey: ['pipeline', orgId, id],
    queryFn: () => clients.pipeline.getPipeline({ orgId, id: id! }),
    enabled: !!id && !!orgId,
  });

  const { data: revisionsData, refetch: refetchRevisions } = useQuery({
    queryKey: ['revisions', orgId, id],
    queryFn: () => clients.pipeline.listRevisions({ orgId: pipeline?.orgId ?? orgId, id: id! }),
    enabled: !!id && !!(pipeline?.orgId ?? orgId),
  });
  const revisions = revisionsData?.items ?? [];

  // GetRevision is org-reader, so any signed-in viewer can open a diff —
  // only Restore is gated on canWrite below.
  const { data: revisionDetail } = useQuery({
    queryKey: ['revision', orgId, id, selectedRevision],
    queryFn: () =>
      clients.pipeline.getRevision({
        orgId: pipeline?.orgId ?? orgId,
        id: id!,
        revision: selectedRevision!,
      }),
    enabled: !!id && !!(pipeline?.orgId ?? orgId) && selectedRevision != null,
  });

  // Seed the form ONCE per pipeline, keyed on its id rather than on the query
  // object.
  //
  // Depending on `pipeline` meant every refetch overwrote the form with the
  // server copy: staleTime is 30s and refetchOnWindowFocus defaults to true, so
  // editing for a couple of minutes and alt-tabbing away and back silently
  // discarded the work. Any cache invalidation elsewhere did the same.
  const seededFor = useRef<string | null>(null);
  useEffect(() => {
    if (!pipeline || seededFor.current === pipeline.id) return;
    seededFor.current = pipeline.id;
    setName(pipeline.name);
    setContents(pipeline.contents);
    setMatchers(pipeline.matchers);
  }, [pipeline]);

  // Debounced validation
  const validate = useCallback(
    async (c: string) => {
      if (!orgId || !c.trim()) return;
      setValidating(true);
      try {
        const result = await clients.pipeline.validatePipeline({
          orgId,
          name: name || 'preview',
          contents: c,
        });
        setDiagnostics(result.diagnostics ?? []);
      } catch (_) {
        /* ignore */
      } finally {
        setValidating(false);
      }
    },
    [name, orgId],
  );

  useEffect(() => {
    // Readers never validate: the server gates ValidatePipeline at org admin
    // (internal/mgmtapi/rpc_interceptor.go), so the call could only fail.
    if (!canWrite) return;
    const t = setTimeout(() => validate(contents), 800);
    return () => clearTimeout(t);
  }, [contents, validate, canWrite]);

  const saveMutation = useMutation({
    mutationFn: () => {
      const body = { orgId, name, contents, matchers };
      return isNew
        ? clients.pipeline.createPipeline(body)
        : clients.pipeline.updatePipeline({ ...body, id: id! });
    },
    onSuccess: (p) => {
      toast.success(isNew ? 'Pipeline created' : 'Pipeline saved');
      // Re-arm the seed guard for THIS pipeline id before the refetches
      // land, exactly like restoreMutation below — the form already holds
      // what was just submitted, so a refetch must not overwrite it.
      seededFor.current = p.id;
      qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
      if (!isNew) {
        // Calling each query's own refetch (rather than
        // qc.invalidateQueries on their keys) so the new revision and
        // "Updated by" show up right away regardless of which render's
        // `enabled` these two mutually-dependent queries last computed —
        // an explicit refetch runs unconditionally, an invalidation only
        // refetches a query considered active at that exact instant.
        refetchPipeline();
        refetchRevisions();
      }
      if (isNew) navigate({ to: '/pipelines/$id', params: { id: p.id } });
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(err.message || 'Save failed');
    },
  });

  const restoreMutation = useMutation({
    mutationFn: (revision: number) =>
      clients.pipeline.restoreRevision({ orgId: pipeline?.orgId ?? orgId, id: id!, revision }),
    onSuccess: (p, revision) => {
      toast.success(`Restored revision #${revision}`);
      // The form is seeded from the restore response right here, so the
      // guard is re-armed for THIS pipeline id rather than cleared: the
      // server copy has already won, and a later routine refetch (window
      // focus, a cache invalidation) must go back to protecting in-progress
      // edits. Nulling it would leave the guard disarmed whenever the
      // refetch returned a structurally identical pipeline and the seed
      // effect never re-ran.
      seededFor.current = p.id;
      setName(p.name);
      setContents(p.contents);
      setMatchers(p.matchers);
      qc.invalidateQueries({ queryKey: ['pipeline', orgId, id] });
      qc.invalidateQueries({ queryKey: ['revisions', orgId, id] });
      qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
      setConfirmingRestore(false);
      setSelectedRevision(null);
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(err.message || 'Restore failed');
    },
  });

  const hasErrors = diagnostics.length > 0;
  // Read-only for viewers; admins and editors both author (see useCanWrite)
  // and for git-managed pipelines (git is the source of truth).
  const readOnly = !canWrite || pipeline?.source === 'git';

  return (
    <div className='flex h-[calc(100vh-7rem)] gap-0'>
      {/* Left pane */}
      <div className='w-[380px] shrink-0 border-r border-border overflow-y-auto p-6 space-y-5'>
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

        <div className='space-y-2'>
          <label className='text-xs font-medium text-muted'>Matchers</label>
          {matchers.map((m, i) => (
            <div key={i} className='flex items-center gap-2'>
              <span className='flex-1 font-mono text-xs bg-border px-2 py-1 rounded'>{m}</span>
              <button
                onClick={() => setMatchers((ms) => ms.filter((_, j) => j !== i))}
                disabled={readOnly}
                className='text-muted-2 hover:text-red-400 text-xs'
              >
                ×
              </button>
            </div>
          ))}
          <div className='flex gap-2'>
            <input
              value={newMatcher}
              onChange={(e) => setNewMatcher(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && newMatcher.trim()) {
                  setMatchers((ms) => [...ms, newMatcher.trim()]);
                  setNewMatcher('');
                }
              }}
              className='flex-1 font-mono text-xs rounded border border-border-strong bg-card px-2 py-1 focus:outline-none focus:ring-1 focus:ring-indigo-500'
              placeholder='cluster="prod"  (Enter to add)'
              disabled={readOnly}
            />
          </div>
        </div>

        {pipeline?.source === 'git' && (
          <div className='rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-400'>
            Managed by Git — read only
          </div>
        )}

        {!isNew && revisions.length > 0 && (
          <div className='space-y-2'>
            <button
              onClick={() => setShowRevisions((r) => !r)}
              className='flex items-center gap-1 text-xs font-medium text-muted hover:text-zinc-200'
            >
              <ChevronDown
                size={12}
                className={`transition-transform ${showRevisions ? '' : '-rotate-90'}`}
              />
              Revision history ({revisions.length})
            </button>
            {showRevisions && (
              <div className='space-y-1 max-h-52 overflow-y-auto pr-1'>
                {revisions.map((r, i) => (
                  <div
                    key={i}
                    className='rounded border border-border bg-card/40 p-2 text-xs space-y-1'
                  >
                    <div className='flex items-center justify-between'>
                      <span className='font-medium text-zinc-300'>#{r.revision}</span>
                      <span className='text-muted-3'>
                        {r.changedAt ? timestampDate(r.changedAt).toLocaleDateString() : ''}
                      </span>
                    </div>
                    <div className='text-muted-2'>{r.changedBy}</div>
                    {r.changeNote && <div className='text-muted italic'>{r.changeNote}</div>}
                    <button
                      onClick={() => setSelectedRevision(r.revision)}
                      className='text-indigo-400 hover:text-indigo-300 text-xs'
                      data-testid='view-revision-btn'
                      data-revision={r.revision}
                    >
                      View diff
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        {pipeline && (
          <div className='text-xs text-muted-2 space-y-1'>
            <p>
              Source: <span className='text-zinc-300'>{pipeline.source}</span>
            </p>
            <p>
              Updated by: <span className='text-zinc-300'>{pipeline.updatedBy}</span>
            </p>
          </div>
        )}
      </div>

      {/* Right pane */}
      <div className='flex flex-1 flex-col overflow-hidden'>
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
              {canWrite && (
                <button
                  onClick={() => setConfirmingRestore(true)}
                  disabled={!revisionDetail}
                  className='rounded bg-indigo-600 px-3 py-1 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
                  data-testid='restore-btn'
                >
                  Restore this revision
                </button>
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
                  ) : (
                    <span className='flex items-center gap-1 text-emerald-500'>
                      <CheckCircle2 size={14} /> No problems
                    </span>
                  ))}
              </div>
              {!readOnly && (
                <button
                  onClick={() => saveMutation.mutate()}
                  disabled={saveMutation.isPending || hasErrors}
                  className='flex items-center gap-1.5 rounded bg-indigo-600 px-3 py-1 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
                >
                  <Save size={13} /> Save
                </button>
              )}
            </div>

            {/* Editor */}
            <div className='flex-1 overflow-hidden'>
              <AlloyEditor
                value={contents}
                onChange={setContents}
                readOnly={readOnly}
                diagnostics={diagnostics}
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
        <Modal
          title='Restore revision'
          onClose={() => setConfirmingRestore(false)}
          testId='restore-dialog'
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              restoreMutation.mutate(selectedRevision);
            }}
            className='space-y-4'
          >
            <p className='text-sm text-muted'>
              Restore revision #{selectedRevision}? This creates a new revision from its contents
              and matchers; the current text is kept in history.
            </p>
            {pipeline?.source === 'git' && (
              <p data-testid='restore-git-warning' className='text-sm text-amber-400'>
                This pipeline is managed by Git. The restore is written as a new revision now, but
                the next git sync will overwrite it — change the file in the repository to make it
                stick.
              </p>
            )}
            <ModalActions
              onCancel={() => setConfirmingRestore(false)}
              submitLabel='Restore'
              pendingLabel='Restoring…'
              pending={restoreMutation.isPending}
              submitTestId='confirm-restore-btn'
            />
          </form>
        </Modal>
      )}
    </div>
  );
}
