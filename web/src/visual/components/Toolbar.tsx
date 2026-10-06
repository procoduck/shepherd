import type { JsonObject } from '@bufbuild/protobuf';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { CheckCircle2, Copy, XCircle } from 'lucide-react';
import { useLayoutEffect, useRef, useState } from 'react';
import { toast } from 'sonner';
import { renderVisual } from '../../api/client';
import { clients } from '../../api/transport';
import { MatcherSuggestions } from '../../components/MatcherSuggestions';
import { FormError } from '../../components/ui/FormError';
import { useCanWrite, useOrgId } from '../../hooks/useOrg';
import { formError } from '../../lib/formError';
import { matcherError as matcherProblem } from '../../lib/matcher';
import { clearDraft } from '../draft';
import { useVisualStore } from '../store';
import { useElementWidth } from '../useElementWidth';
import { OverflowMenu } from './OverflowMenu';
import { RevisionCompare } from './RevisionCompare';
import { type SandboxRunHandle, SandboxRunPanel } from './SandboxRunPanel';

/** Thrown when the server-side render (VisualService.Render) reports L1
 * diagnostics — the graph itself failed to render, so there is nothing
 * useful to save. Its message is the first diagnostic, shown under the
 * toolbar like any other save refusal. */
class RenderFailedError extends Error {}

export const READ_ONLY_REASON = "Viewers can't change pipelines — ask an org editor or admin";

/**
 * How much of the toolbar fits in one row (#251), by the toolbar's own width
 * — not the window's, so the app nav counts (the builder shows it as a 56px
 * rail; expanding it narrows the row):
 *  - full (≥ 1500px): every action inline.
 *  - medium: Flow check and History move into the "More" menu, leaving the
 *    matchers real room. A 1440px window (1384px row) and a 1280px one
 *    (1224px) land here; inline, the row wrapped at 1440px.
 *  - narrow (< 1000px, e.g. a 1024px window): Simulate moves into "More" too,
 *    and the name and matcher inputs shrink.
 */
export const TOOLBAR_FULL_FROM = 1500;
export const TOOLBAR_NARROW_BELOW = 1000;
type ToolbarLayout = 'full' | 'medium' | 'narrow';

function toolbarLayout(width: number): ToolbarLayout {
  // 0 = not measured yet (no layout pass in this environment): keep the
  // historical all-inline row.
  if (width === 0 || width >= TOOLBAR_FULL_FROM) return 'full';
  return width >= TOOLBAR_NARROW_BELOW ? 'medium' : 'narrow';
}

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
  const [comparing, setComparing] = useState(false);
  const toolbarRef = useRef<HTMLDivElement>(null);
  const sandboxRef = useRef<SandboxRunHandle>(null);
  const width = useElementWidth(toolbarRef);
  const layout = toolbarLayout(width);
  const compact = layout === 'narrow';
  const secondaryInMenu = layout !== 'full';
  const simulateInMenu = layout === 'narrow';
  const chipsRef = useRef<HTMLDivElement>(null);
  const chipsWidth = useElementWidth(chipsRef);
  const [chipsOverflow, setChipsOverflow] = useState(false);
  // Whether the chip strip holds more than it shows — re-measured whenever
  // the chips or the room for them change.
  useLayoutEffect(() => {
    const el = chipsRef.current;
    setChipsOverflow(!!el && el.scrollWidth > el.clientWidth + 1);
  }, [matchers, chipsWidth, width]);
  // Steps the strip one view-width along, wrapping back to the start.
  const scrollChips = () => {
    const el = chipsRef.current;
    if (!el) return;
    const atEnd = el.scrollLeft + el.clientWidth >= el.scrollWidth - 1;
    el.scrollTo({ left: atEnd ? 0 : el.scrollLeft + el.clientWidth, behavior: 'smooth' });
  };

  const orgId = useOrgId();
  // Read-only for viewers, like the text editor (PipelineEditorPage): every
  // action here is org-editor on the server, so offering it only produced a
  // 403 toast (#206).
  const readOnly = !useCanWrite();
  const navigate = useNavigate();
  const qc = useQueryClient();

  const commitMatcher = () => {
    const value = matcherInput.trim();
    if (!value) return;
    // The same rules the server's matcher parser applies (lib/matcher.ts).
    const problem = matcherProblem(value);
    if (problem) {
      setMatcherError(problem);
      return;
    }
    addMatcher(value);
    setMatcherInput('');
    setMatcherError(null);
  };

  const copyId = async () => {
    try {
      await navigator.clipboard.writeText(pipelineId);
      toast.success('Pipeline id copied');
    } catch {
      toast.error('Copy failed — the full id is in the tooltip');
    }
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
        // uses for node props. The JSON round-trip drops any explicitly-
        // undefined keys a store patch may have planted: protobuf's Struct
        // conversion rejects undefined ("google.protobuf.Value must have a
        // value"), and wizard_state is persisted as JSON anyway, so this is
        // semantically exact.
        wizardState: JSON.parse(JSON.stringify(doc)) as JsonObject,
      };
      return pipelineId === 'new'
        ? clients.pipeline.createPipeline(body)
        : clients.pipeline.updatePipeline({ ...body, id: pipelineId });
    },
    onSuccess: (p) => {
      toast.success(pipelineId === 'new' ? 'Pipeline created' : 'Pipeline saved');
      useVisualStore.getState().markSaved();
      qc.invalidateQueries({ queryKey: ['pipelines', orgId] });
      // The graph just saved is now durable on the server — the local draft
      // (keyed by the id this save was made under, 'new' for a create) no
      // longer has anything to protect against losing.
      void clearDraft(pipelineId);
      // Save lands on the pipeline's page, which is the text editor; `from`
      // makes it say so and offer the way back into the builder (#251).
      navigate({ to: '/pipelines/$id', params: { id: p.id }, search: { from: 'visual' } });
    },
  });
  // A refused save shows in a strip under the toolbar (#249), not a toast.
  const saveError = formError(saveMutation.error, 'Save failed');

  const matchersRequired = matchers.length === 0;
  // L1 diagnostics of severity 'error' are blocking (see l1.ts) — a
  // 'warning' diagnostic must not prevent save. Matchers are checked first
  // so the existing "add a matcher" tooltip still wins when both are true;
  // an unwired/misconfigured graph is reported separately once that's fixed.
  const hasBlockingErrors = errors > 0;
  const canSave = !readOnly && !matchersRequired && !hasBlockingErrors && !saveMutation.isPending;
  const saveDisabledReason = readOnly
    ? READ_ONLY_REASON
    : matchersRequired
      ? 'Add at least one matcher before saving — format: key="value" or key=~"regex"'
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

  const flowResult = flowCheckActive
    ? errors === 0
      ? `Flow OK · ${doc.nodes.filter((n) => !n.disabled).length} nodes, ${doc.edges.length} wires`
      : `Flow broken · ${errors} problem${errors !== 1 ? 's' : ''}`
    : null;
  const sandboxDisabledReason = readOnly ? READ_ONLY_REASON : undefined;

  return (
    <>
      <div
        ref={toolbarRef}
        className={`h-11 border-b flex items-center px-4 shrink-0 ${compact ? 'gap-2' : 'gap-3'}`}
        data-testid='toolbar'
        data-layout={layout}
      >
        {flowCheckActive && <span data-testid='flow-check-active' className='sr-only' />}
        <input
          data-testid='toolbar-name'
          aria-label='Pipeline name'
          value={pipelineName}
          onChange={(e) => setPipelineName(e.target.value)}
          disabled={readOnly}
          placeholder='Pipeline name...'
          className={`shrink-0 border rounded px-2 py-1 text-sm bg-background disabled:opacity-60 disabled:cursor-not-allowed ${
            compact ? 'w-32' : 'w-48'
          }`}
        />
        {readOnly && (
          <span
            data-testid='toolbar-read-only'
            title={READ_ONLY_REASON}
            className='shrink-0 whitespace-nowrap text-xs px-2 py-0.5 rounded border border-border text-muted'
          >
            Read only
          </span>
        )}
        {pipelineId === 'new' ? (
          <span className='shrink-0 whitespace-nowrap text-xs text-muted'>New pipeline</span>
        ) : (
          // #251: a UUID wrapped to three lines at 1024px. One truncated line;
          // the full id is in the tooltip and one click from the clipboard.
          <span className='shrink-0 flex items-center gap-0.5'>
            <span
              data-testid='toolbar-pipeline-id'
              title={`Pipeline id ${pipelineId}`}
              className={`block truncate whitespace-nowrap font-mono text-xs text-muted ${
                compact ? 'max-w-[6rem]' : 'max-w-[8rem]'
              }`}
            >
              {pipelineId}
            </span>
            <button
              type='button'
              data-testid='toolbar-id-copy'
              aria-label='Copy pipeline id'
              title='Copy pipeline id'
              onClick={copyId}
              className='p-1 rounded text-muted hover:text-zinc-200'
            >
              <Copy size={12} />
            </button>
          </span>
        )}

        {/* The matchers take whatever the row has left: the chips scroll
          sideways inside their own strip rather than being clipped by the
          fixed-width controls around them (#251). */}
        <div
          className='relative flex flex-1 min-w-0 items-center gap-1'
          data-testid='toolbar-matchers'
        >
          <div
            ref={chipsRef}
            data-testid='toolbar-matcher-chips'
            title={matchers.length ? matchers.join('\n') : undefined}
            className='flex min-w-0 shrink items-center gap-1 overflow-x-auto'
          >
            {matchers.map((m, i) => (
              <span
                key={`${m}-${i}`}
                data-testid='matcher-chip'
                title={m}
                className='flex items-center gap-1 shrink-0 whitespace-nowrap text-xs font-mono bg-card border rounded px-2 py-0.5'
              >
                {/* A very long matcher is cut short so one chip always fits the strip. */}
                <span className='block max-w-[11rem] truncate'>{m}</span>
                <button
                  type='button'
                  data-testid={`matcher-remove-${i}`}
                  aria-label={`Remove matcher ${m}`}
                  onClick={() => removeMatcher(i)}
                  disabled={readOnly}
                  className='text-muted hover:text-red-400 disabled:opacity-50 disabled:cursor-not-allowed'
                >
                  ×
                </button>
              </span>
            ))}
          </div>
          {chipsOverflow && (
            // The strip scrolls; this says there is more in it than shows, and
            // steps through it.
            <button
              type='button'
              data-testid='toolbar-matchers-more'
              title={`${matchers.length} matchers:\n${matchers.join('\n')}`}
              aria-label={`Show more matchers (${matchers.length} in all)`}
              onClick={scrollChips}
              className='shrink-0 whitespace-nowrap rounded border border-border px-1.5 py-0.5 text-xs text-muted hover:text-zinc-200'
            >
              {matchers.length} ›
            </button>
          )}
          <input
            data-testid='matcher-input'
            aria-label='Add matcher'
            aria-invalid={!!matcherError}
            list='visual-matcher-suggestions'
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
            disabled={readOnly}
            className={`shrink-0 border rounded px-2 py-1 text-xs font-mono bg-background disabled:opacity-60 disabled:cursor-not-allowed ${
              compact ? 'w-28' : 'w-40'
            }`}
          />
          <MatcherSuggestions id='visual-matcher-suggestions' orgId={orgId} />
          {matcherError && (
            // Below the row, not in it: the sentence is wider than the strip.
            <span
              data-testid='matcher-error'
              role='alert'
              className='absolute left-0 top-full mt-1 z-20 whitespace-nowrap rounded border border-border bg-card px-2 py-1 text-xs text-red-500 shadow-md'
            >
              {matcherError}
            </span>
          )}
        </div>

        <button
          type='button'
          data-testid='toolbar-validity'
          onClick={showProblems}
          className={`flex items-center gap-1 whitespace-nowrap text-xs px-2 py-1 rounded shrink-0 ${
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

        {!secondaryInMenu && (
          <button
            type='button'
            data-testid='flow-check-toggle'
            aria-pressed={flowCheckActive}
            onClick={toggleFlowCheck}
            className='text-sm px-3 py-1 rounded border shrink-0 whitespace-nowrap'
          >
            Flow check
          </button>
        )}
        {flowResult && (
          // F15: toggling flow check used to change only the canvas's edge
          // animation (CanvasPane.tsx), with no textual outcome anywhere.
          // Below the full layout only the verdict shows; the counts are in the tooltip.
          <span
            data-testid='flow-check-result'
            title={flowResult}
            className={`block truncate whitespace-nowrap text-xs shrink-0 max-w-[14rem] ${
              errors === 0 ? 'text-emerald-400' : 'text-red-400'
            }`}
          >
            {secondaryInMenu ? (errors === 0 ? 'Flow OK' : 'Flow broken') : flowResult}
          </span>
        )}
        {/* History opens the graph revision diff (#118). Only a saved pipeline
          has revisions to compare, so it's hidden for a brand-new one. */}
        {!secondaryInMenu && pipelineId !== 'new' && (
          <button
            type='button'
            data-testid='toolbar-history'
            onClick={() => setComparing(true)}
            className='text-sm px-3 py-1 rounded border shrink-0 whitespace-nowrap'
          >
            History
          </button>
        )}
        {comparing && orgId && pipelineId !== 'new' && (
          <RevisionCompare
            pipelineId={pipelineId}
            orgId={orgId}
            onClose={() => setComparing(false)}
          />
        )}
        {/* Mounted in every layout — it owns the run dialog. On a narrow row its
          own trigger is hidden and the overflow menu starts the run. */}
        <SandboxRunPanel
          ref={sandboxRef}
          orgId={orgId}
          disabledReason={sandboxDisabledReason}
          hideTrigger={simulateInMenu}
        />
        {secondaryInMenu && (
          <OverflowMenu
            testId='toolbar-more'
            label='More actions'
            items={[
              {
                kind: 'checkbox',
                testId: 'flow-check-toggle',
                label: 'Flow check',
                checked: flowCheckActive,
                onSelect: toggleFlowCheck,
              },
              ...(pipelineId !== 'new'
                ? [
                    {
                      kind: 'item' as const,
                      testId: 'toolbar-history',
                      label: 'History',
                      onSelect: () => setComparing(true),
                    },
                  ]
                : []),
              ...(simulateInMenu
                ? [
                    {
                      kind: 'item' as const,
                      testId: 'simulate-menu-sandbox-run',
                      label: 'Sandbox run (30s)…',
                      disabledReason: orgId ? sandboxDisabledReason : 'No organization selected',
                      onSelect: () => sandboxRef.current?.start(),
                    },
                  ]
                : []),
            ]}
          />
        )}
        <button
          type='button'
          data-testid='toolbar-save'
          onClick={() => saveMutation.mutate()}
          disabled={!canSave}
          title={saveDisabledReason}
          className='text-sm px-3 py-1 rounded border shrink-0 whitespace-nowrap disabled:opacity-50 disabled:cursor-not-allowed'
        >
          {saveMutation.isPending ? 'Saving…' : 'Save'}
        </button>
      </div>
      {saveError && (
        <div className='border-b px-4 py-2 shrink-0'>
          <FormError message={saveError} />
        </div>
      )}
    </>
  );
}
