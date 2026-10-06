import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import type { Pipeline } from '@/gen/shepherd/mgmt/v1/pipeline_pb';

/**
 * Enable/Disable and Delete on the pipeline page (#252): the same RPCs and
 * the same `canWrite` gate the Pipelines list uses. A team member, whose
 * write reach comes only from owning a pipeline, sees neither — exactly as on
 * the list: /api/me cannot say what their team owns, and the server
 * (auth.AuthorizeOwnership) has the final say either way. Without write
 * access the enabled state is shown as text.
 *
 * `queryOrgId` is the org the page's queries are keyed on; `orgId` is the
 * pipeline's own org, sent with the RPCs.
 */
export function PipelineActions({
  pipeline,
  orgId,
  queryOrgId,
  canWrite,
}: {
  pipeline: Pipeline;
  orgId: string;
  queryOrgId: string;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const pipelineKey = ['pipeline', queryOrgId, pipeline.id];

  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      enabled
        ? clients.pipeline.disablePipeline({ orgId, id: pipeline.id })
        : clients.pipeline.enablePipeline({ orgId, id: pipeline.id }),
    onSuccess: (p) => {
      toast.success(p.enabled ? 'Pipeline enabled' : 'Pipeline disabled');
      qc.setQueryData(pipelineKey, p);
      qc.invalidateQueries({ queryKey: pipelineKey });
      qc.invalidateQueries({ queryKey: ['pipelines', queryOrgId] });
    },
    onError: (e) => toast.error(toApiError(e).message || 'Could not change the enabled state'),
  });

  const remove = useMutation({
    mutationFn: () => clients.pipeline.deletePipeline({ orgId, id: pipeline.id }),
    onSuccess: () => {
      toast.success('Pipeline deleted');
      setConfirmingDelete(false);
      qc.removeQueries({ queryKey: pipelineKey });
      qc.invalidateQueries({ queryKey: ['pipelines', queryOrgId] });
      navigate({ to: '/pipelines' });
    },
    onError: (e) => {
      setConfirmingDelete(false);
      toast.error(toApiError(e).message || 'Delete failed');
    },
  });

  const state = pipeline.enabled ? 'Enabled' : 'Disabled';

  return (
    <div className='flex items-center justify-between gap-3'>
      {canWrite ? (
        <label className='flex items-center gap-2 text-xs text-muted'>
          <button
            type='button'
            role='switch'
            aria-checked={pipeline.enabled}
            aria-label={`Enabled: ${pipeline.name}`}
            disabled={toggle.isPending}
            onClick={() => toggle.mutate(pipeline.enabled)}
            className={`w-9 h-5 rounded-full relative shrink-0 transition-colors disabled:opacity-50 ${pipeline.enabled ? 'bg-emerald-600' : 'bg-border-strong'}`}
          >
            <span
              className={`absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-white shadow transition-transform ${pipeline.enabled ? 'translate-x-4' : 'translate-x-0'}`}
            />
          </button>
          {state}
        </label>
      ) : (
        <span className='text-xs text-muted' data-testid='pipeline-enabled-status'>
          {state}
        </span>
      )}
      {/* A Git pipeline's source of truth is the repository: the server
          refuses to delete one (rpc_pipeline.go), so it is not offered. */}
      {canWrite && pipeline.source !== 'git' && (
        <button
          type='button'
          onClick={() => setConfirmingDelete(true)}
          className='flex items-center gap-1 text-xs text-muted hover:text-red-400'
          data-testid='pipeline-delete-btn'
        >
          <Trash2 size={12} /> Delete
        </button>
      )}
      {confirmingDelete && (
        <AdminConfirmDialog
          title='Delete pipeline'
          body={`Delete "${pipeline.name}"? It stops being served to matching collectors, and its revision history is deleted with it.`}
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={remove.isPending}
          onCancel={() => setConfirmingDelete(false)}
          onConfirm={() => remove.mutate()}
        />
      )}
    </div>
  );
}
