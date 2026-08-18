import { timestampDate } from '@bufbuild/protobuf/wkt';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from '@tanstack/react-router';
import { CheckCircle2, ChevronDown, Save, XCircle } from 'lucide-react';
import { useCallback, useEffect, useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AlloyEditor } from '@/editor/AlloyEditor';
import type { Diagnostic } from '@/gen/shepherd/mgmt/v1/common_pb';
import { useOrgId } from '@/hooks/useOrg';

export function PipelineEditorPage() {
  const { id } = useParams({ strict: false }) as { id?: string };
  const isNew = !id;
  const navigate = useNavigate();
  const qc = useQueryClient();
  const orgId = useOrgId();

  const [name, setName] = useState('');
  const [contents, setContents] = useState('');
  const [matchers, setMatchers] = useState<string[]>([]);
  const [newMatcher, setNewMatcher] = useState('');
  const [diagnostics, setDiagnostics] = useState<Diagnostic[]>([]);
  const [validating, setValidating] = useState(false);
  const [showRevisions, setShowRevisions] = useState(false);

  const { data: pipeline } = useQuery({
    queryKey: ['pipeline', id],
    queryFn: () => clients.pipeline.getPipeline({ orgId, id: id! }),
    enabled: !!id && !!orgId,
  });

  const { data: revisionsData } = useQuery({
    queryKey: ['revisions', orgId, id],
    queryFn: () => clients.pipeline.listRevisions({ orgId: pipeline?.orgId ?? orgId, id: id! }),
    enabled: !!id && !!(pipeline?.orgId ?? orgId),
  });
  const revisions = revisionsData?.items ?? [];

  useEffect(() => {
    if (pipeline) {
      setName(pipeline.name);
      setContents(pipeline.contents);
      setMatchers(pipeline.matchers);
    }
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
    const t = setTimeout(() => validate(contents), 800);
    return () => clearTimeout(t);
  }, [contents, validate]);

  const saveMutation = useMutation({
    mutationFn: () => {
      const body = { orgId, name, contents, matchers };
      return isNew
        ? clients.pipeline.createPipeline(body)
        : clients.pipeline.updatePipeline({ ...body, id: id! });
    },
    onSuccess: (p) => {
      toast.success(isNew ? 'Pipeline created' : 'Pipeline saved');
      qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
      if (isNew) navigate({ to: '/pipelines/$id', params: { id: p.id } });
    },
    onError: (e) => {
      const err = toApiError(e);
      toast.error(err.message || 'Save failed');
    },
  });

  const hasErrors = diagnostics.length > 0;

  return (
    <div className='flex h-[calc(100vh-7rem)] gap-0'>
      {/* Left pane */}
      <div className='w-[380px] shrink-0 border-r border-zinc-800 overflow-y-auto p-6 space-y-5'>
        <div className='space-y-1'>
          <label className='text-xs font-medium text-zinc-400'>Name</label>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={pipeline?.source === 'git'}
            className='w-full rounded-md border border-zinc-700 bg-zinc-900 px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-indigo-500'
            placeholder='my-pipeline'
          />
        </div>

        <div className='space-y-2'>
          <label className='text-xs font-medium text-zinc-400'>Matchers</label>
          {matchers.map((m, i) => (
            <div key={i} className='flex items-center gap-2'>
              <span className='flex-1 font-mono text-xs bg-zinc-800 px-2 py-1 rounded'>{m}</span>
              <button
                onClick={() => setMatchers((ms) => ms.filter((_, j) => j !== i))}
                disabled={pipeline?.source === 'git'}
                className='text-zinc-500 hover:text-red-400 text-xs'
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
              className='flex-1 font-mono text-xs rounded border border-zinc-700 bg-zinc-900 px-2 py-1 focus:outline-none focus:ring-1 focus:ring-indigo-500'
              placeholder='cluster="prod"  (Enter to add)'
              disabled={pipeline?.source === 'git'}
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
              className='flex items-center gap-1 text-xs font-medium text-zinc-400 hover:text-zinc-200'
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
                    className='rounded border border-zinc-800 bg-zinc-900/40 p-2 text-xs space-y-1'
                  >
                    <div className='flex items-center justify-between'>
                      <span className='font-medium text-zinc-300'>#{r.revision}</span>
                      <span className='text-zinc-600'>
                        {r.changedAt ? timestampDate(r.changedAt).toLocaleDateString() : ''}
                      </span>
                    </div>
                    <div className='text-zinc-500'>{r.changedBy}</div>
                    {r.changeNote && <div className='text-zinc-400 italic'>{r.changeNote}</div>}
                    <button
                      onClick={() => toast.info('Revision contents are not exposed by the API yet')}
                      className='text-indigo-400 hover:text-indigo-300 text-xs'
                      data-testid='restore-btn'
                    >
                      Restore this revision
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        {pipeline && (
          <div className='text-xs text-zinc-500 space-y-1'>
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
        {/* Toolbar */}
        <div className='h-11 border-b border-zinc-800 px-3 flex items-center justify-between shrink-0'>
          <div className='flex items-center gap-2 text-xs' aria-live='polite'>
            {validating ? (
              <span className='text-zinc-400'>Validating…</span>
            ) : hasErrors ? (
              <span className='flex items-center gap-1 text-red-400'>
                <XCircle size={14} /> {diagnostics.length} problem
                {diagnostics.length > 1 ? 's' : ''}
              </span>
            ) : (
              <span className='flex items-center gap-1 text-emerald-500'>
                <CheckCircle2 size={14} /> No problems
              </span>
            )}
          </div>
          {pipeline?.source !== 'git' && (
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
            readOnly={pipeline?.source === 'git'}
            diagnostics={diagnostics}
            height='100%'
          />
        </div>

        {/* Problems panel */}
        {hasErrors && (
          <div className='max-h-40 overflow-y-auto border-t border-zinc-800 bg-zinc-950 p-2 space-y-1'>
            {diagnostics.map((d, i) => (
              <p key={i} className='font-mono text-xs text-red-400'>
                {d.line}:{d.col} &nbsp; {d.message}
              </p>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
