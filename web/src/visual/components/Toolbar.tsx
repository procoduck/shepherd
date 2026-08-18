import { toast } from 'sonner';
import { renderVisual } from '../../api/client';
import { useMe } from '../../hooks/useMe';
import { useVisualStore } from '../store';
export function Toolbar({ pipelineId }: { pipelineId: string }) {
  const errors = useVisualStore((s) => s.diagnostics.filter((d) => d.severity === 'error').length);
  const doc = useVisualStore((s) => s.doc);
  const flowCheckActive = useVisualStore((s) => s.flowCheckActive);
  const toggleFlowCheck = useVisualStore((s) => s.toggleFlowCheck);
  const { data: me } = useMe();
  const save = async () => {
    const org = me?.orgs[0]?.id;
    if (!org) return toast.error('No organization available');
    try {
      await renderVisual(org, doc);
      toast.success('Render verified');
    } catch {
      toast.error('Render failed');
    }
  };
  return (
    <div
      className='h-11 border-b flex items-center px-4 gap-4 shrink-0'
      data-testid='toolbar'
    >
      {flowCheckActive && <span data-testid='flow-check-active' className='sr-only' />}
      <input
        data-testid='toolbar-name'
        placeholder='Pipeline name...'
        className='border rounded px-2 py-1 text-sm w-48 bg-background'
      />
      <span className='text-xs text-muted-foreground'>
        {pipelineId === 'new' ? 'New pipeline' : pipelineId}
      </span>
      {errors > 0 && (
        <span data-testid='toolbar-errors' className='text-xs text-red-500 ml-auto'>
          {errors} error{errors !== 1 ? 's' : ''}
        </span>
      )}
      <button
        data-testid='flow-check-toggle'
        aria-pressed={flowCheckActive}
        onClick={toggleFlowCheck}
        className='text-sm px-3 py-1 rounded border'
      >
        Flow check
      </button>
      <button
        data-testid='toolbar-save'
        onClick={save}
        className='ml-auto text-sm px-3 py-1 rounded border'
      >
        Save
      </button>
    </div>
  );
}
