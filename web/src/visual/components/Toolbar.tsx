import type { JsonObject } from '@bufbuild/protobuf';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { CheckCircle2, XCircle } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { renderVisual } from '../../api/client';
import { clients, toApiError } from '../../api/transport';
import { useOrgId } from '../../hooks/useOrg';
import { isValidMatcher } from '../matcher';
import { useVisualStore } from '../store';

/** Thrown when the server-side render (VisualService.Render) reports L1
 * diagnostics — the graph itself failed to render, so there is nothing
 * useful to save. Distinguished from a transport/ConnectError so the
 * mutation's error handler can show the diagnostic message verbatim instead
 * of running it through toApiError. */
class RenderFailedError extends Error {}

export function Toolbar({ pipelineId }: { pipelineId: string }) {
  const diagnostics = useVisualStore((s) => s.diagnostics);
  const errors = diagnostics.filter((d) => d.severity === 'error').length;
  const doc = useVisualStore((s) => s.doc);
  const flowCheckActive = useVisualStore((s) => s.flowCheckActive);
  const toggleFlowCheck = useVisualStore((s) => s.toggleFlowCheck);
  const pipelineName = useVisualStore((s) => s.pipelineName);
  const setPipelineName = useVisualStore((s) => s.setPipelineName);
  const matchers = useVisualStore((s) => s.matchers);
  const addMatcher = useVisualStore((s) => s.addMatcher);
  const removeMatcher = useVisualStore((s) => s.removeMatcher);

  const [matcherInput, setMatcherInput] = useState('');
  const [matcherError, setMatcherError] = useState<string | null>(null);

  const orgId = useOrgId();
  const navigate = useNavigate();
  const qc = useQueryClient();

  const commitMatcher = () => {
    const value = matcherInput.trim();
    if (!value) return;
    if (!isValidMatcher(value)) {
      setMatcherError('Matchers must look like key="value" or key=~"regex"');
      return;
    }
    addMatcher(value);
    setMatcherInput('');
    setMatcherError(null);
  };

  const saveMutation = useMutation({
    mutationFn: async () => {
      if (!orgId) throw new Error('No organization available');
      // Render server-side first — contents saved on the pipeline must be
      // the server's render of `doc`, not the client-side TS preview.
      const rendered = await renderVisual(orgId, doc);
      if (rendered.diagnostics.length > 0) {
        const first = rendered.diagnostics[0] as { message?: string } | undefined;
        throw new RenderFailedError(
          first?.message ?? 'Render failed — fix the problems below and try again',
        );
      }
      const body = {
        orgId,
        name: pipelineName.trim(),
        contents: rendered.content,
        matchers,
        source: 'visual',
        // wizard_state is the visual graph document itself (see
        // pipeline.proto), not the wire-shaped Render request — cast is the
        // same "local domain object as JsonObject" pattern api/client.ts
        // uses for node props.
        wizardState: doc as unknown as JsonObject,
      };
      return pipelineId === 'new'
        ? clients.pipeline.createPipeline(body)
        : clients.pipeline.updatePipeline({ ...body, id: pipelineId });
    },
    onSuccess: (p) => {
      toast.success(pipelineId === 'new' ? 'Pipeline created' : 'Pipeline saved');
      qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
      navigate({ to: '/pipelines/$id', params: { id: p.id } });
    },
    onError: (e) => {
      if (e instanceof RenderFailedError) {
        toast.error(e.message);
        return;
      }
      const err = toApiError(e);
      toast.error(err.message || 'Save failed');
    },
  });

  const matchersRequired = matchers.length === 0;
  // L1 diagnostics of severity 'error' are blocking (see l1.ts) — a
  // 'warning' diagnostic must not prevent save. Matchers are checked first
  // so the existing "add a matcher" tooltip still wins when both are true;
  // an unwired/misconfigured graph is reported separately once that's fixed.
  const hasBlockingErrors = errors > 0;
  const canSave = !matchersRequired && !hasBlockingErrors && !saveMutation.isPending;
  const saveDisabledReason = matchersRequired
    ? 'Add at least one matcher before saving — format: key="value" or key=~"regex" (quotes required)'
    : hasBlockingErrors
      ? `Fix ${errors} blocking problem${errors !== 1 ? 's' : ''} before saving`
      : undefined;

  // Clicking the validity chip reuses the bottom drawer's own Problems-tab
  // button rather than duplicating its open/tab state — that state is
  // local to BottomDrawer (owned by a different task); this is the
  // least-invasive way to link the two without either side reaching into
  // the other's internals beyond a stable data-testid.
  const showProblems = () => {
    document.querySelector<HTMLButtonElement>('[data-testid="drawer-tab-problems"]')?.click();
  };

  return (
    <div className='h-11 border-b flex items-center px-4 gap-3 shrink-0' data-testid='toolbar'>
      {flowCheckActive && <span data-testid='flow-check-active' className='sr-only' />}
      <input
        data-testid='toolbar-name'
        value={pipelineName}
        onChange={(e) => setPipelineName(e.target.value)}
        placeholder='Pipeline name...'
        className='border rounded px-2 py-1 text-sm w-48 bg-background'
      />
      <span className='text-xs text-muted'>
        {pipelineId === 'new' ? 'New pipeline' : pipelineId}
      </span>

      <div
        className='flex items-center gap-1 overflow-x-auto max-w-sm'
        data-testid='toolbar-matchers'
      >
        {matchers.map((m, i) => (
          <span
            key={`${m}-${i}`}
            data-testid='matcher-chip'
            className='flex items-center gap-1 shrink-0 text-xs font-mono bg-card border rounded px-2 py-0.5'
          >
            {m}
            <button
              type='button'
              data-testid={`matcher-remove-${i}`}
              aria-label={`Remove matcher ${m}`}
              onClick={() => removeMatcher(i)}
              className='text-muted hover:text-red-400'
            >
              ×
            </button>
          </span>
        ))}
        <input
          data-testid='matcher-input'
          value={matcherInput}
          onChange={(e) => {
            setMatcherInput(e.target.value);
            if (matcherError) setMatcherError(null);
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              commitMatcher();
            }
          }}
          placeholder='cluster="prod-eu-1"'
          className='shrink-0 border rounded px-2 py-1 text-xs w-40 font-mono bg-background'
        />
        {matcherError && (
          <span data-testid='matcher-error' className='shrink-0 text-xs text-red-500'>
            {matcherError}
          </span>
        )}
      </div>

      <button
        type='button'
        data-testid='toolbar-validity'
        onClick={showProblems}
        className={`ml-auto flex items-center gap-1 text-xs px-2 py-1 rounded shrink-0 ${
          errors > 0 ? 'text-red-400' : 'text-emerald-400'
        }`}
      >
        {errors > 0 ? (
          <>
            <XCircle size={13} />
            {/* Task item 10: this chip and the Save button's blocking-count
                tooltip must agree — both count only blocking `error`
                diagnostics, never `warning`s (F16, still unfixed before this
                change: the chip showed diagnostics.length, e.g. "3 problems"
                against a tooltip reading "Fix 2 blocking problems"). */}
            {errors} problem{errors !== 1 ? 's' : ''}
          </>
        ) : (
          <>
            <CheckCircle2 size={13} />
            Valid
          </>
        )}
      </button>

      <button
        data-testid='flow-check-toggle'
        aria-pressed={flowCheckActive}
        onClick={toggleFlowCheck}
        className='text-sm px-3 py-1 rounded border shrink-0'
      >
        Flow check
      </button>
      <button
        data-testid='toolbar-save'
        onClick={() => saveMutation.mutate()}
        disabled={!canSave}
        title={saveDisabledReason}
        className='text-sm px-3 py-1 rounded border shrink-0 disabled:opacity-50 disabled:cursor-not-allowed'
      >
        {saveMutation.isPending ? 'Saving…' : 'Save'}
      </button>
    </div>
  );
}
