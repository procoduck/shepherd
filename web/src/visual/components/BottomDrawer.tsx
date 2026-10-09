import { ChevronDown, ChevronUp } from 'lucide-react';
import { type KeyboardEvent as ReactKeyboardEvent, useEffect, useState } from 'react';
import {
  type LineTrace,
  renderVisual,
  simulateLogs,
  simulateRelabel,
  type TargetTrace,
} from '../../api/client';
import { useCanWrite, useOrgId } from '../../hooks/useOrg';
import { renderTS } from '../renderTS';
import { useVisualStore } from '../store';
import { useDebouncedValue } from '../useDebouncedValue';
import { useReadOnlyReason } from './Toolbar';

const DRAWER_TABS = ['problems', 'code', 'simulate'] as const;
type DrawerTab = (typeof DRAWER_TABS)[number];

/** The selected tab carries an accent underline on a raised background and
 *  full-strength text; the others stay muted until hovered. `text` replaces
 *  the default text colour (the Problems tab colours itself by its count). */
function tabClass(selected: boolean, text?: string): string {
  const base = selected ? 'border-accent bg-card' : 'border-transparent';
  const colour = text ?? (selected ? 'text-zinc-100' : 'text-muted hover:text-zinc-200');
  return `${base} ${colour}`;
}

export function BottomDrawer() {
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<DrawerTab>('problems');
  const diagnostics = useVisualStore((s) => s.diagnostics);
  const doc = useVisualStore((s) => s.doc);
  const schema = useVisualStore((s) => s.schema);
  const selected = useVisualStore((s) => s.selected);
  const setSelected = useVisualStore((s) => s.setSelected);
  const orgId = useOrgId();
  // Server render (Verify) and the simulators are org-editor RPCs; a viewer
  // sees the tabs but cannot run them (#206).
  const readOnly = !useCanWrite();
  const readOnlyReason = useReadOnlyReason();
  const [simulateTab, setSimulateTab] = useState<'relabel' | 'logs'>('relabel');
  const [relabelResult, setRelabelResult] = useState<{ traces: TargetTrace[] }>();
  const [logsResult, setLogsResult] = useState<{ traces: LineTrace[] }>();
  // Tri-state: null before the first Verify click, then whichever the last
  // server render reported (F3: this used to read a window-global the test
  // harness injects but production never sets, and rendered nothing on a
  // match — silently dead outside the mocked test suite).
  //
  // The outcome is a claim about a SPECIFIC client render, captured here as
  // `content` alongside it. Staleness is derived by comparing that snapshot
  // to the current `rendered.content` at display time (below) rather than by
  // keying an effect on `doc` identity: `doc` also changes on view-only
  // mutations renderTS never reads (viewport pan/zoom via updateViewport,
  // node-drag position via updateNode), which used to blank the banner on a
  // canvas pan even though the outcome was still accurate. Snapshotting the
  // content also closes the async race where an edit made while `verify`'s
  // request is in flight would otherwise let a late response resurrect a
  // stale outcome for a doc the user has since changed.
  const [verified, setVerified] = useState<null | {
    outcome: 'match' | 'mismatch';
    content: string;
  }>(null);
  // W5-10: `renderTS` re-walks the whole graph, and `doc` changes on every
  // store mutation — including one per keystroke anywhere in the inspector,
  // whether or not the Code tab is even the one showing. Rendering from a
  // 300ms-trailing-debounced view of `doc` instead keeps that off the hot
  // path while the user is still typing, without changing what eventually
  // renders (nor `verify`, below, which deliberately renders the LIVE `doc`
  // — it's a one-off click, not a per-keystroke recompute).
  const debouncedDoc = useDebouncedValue(doc, 300);
  const rendered = schema ? renderTS(debouncedDoc, schema) : null;
  // The collapsed bar's three labels double as tab affordances: clicking one
  // both selects that tab and expands the drawer to show it.
  const selectTab = (t: DrawerTab) => {
    setTab(t);
    setOpen(true);
  };
  // Arrow keys / Home / End move between the tabs (the WAI-ARIA tabs
  // pattern, automatic activation): selection follows focus.
  const onTabKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const i = DRAWER_TABS.indexOf(tab);
    const next =
      e.key === 'ArrowRight'
        ? DRAWER_TABS[(i + 1) % DRAWER_TABS.length]
        : e.key === 'ArrowLeft'
          ? DRAWER_TABS[(i - 1 + DRAWER_TABS.length) % DRAWER_TABS.length]
          : e.key === 'Home'
            ? DRAWER_TABS[0]
            : e.key === 'End'
              ? DRAWER_TABS[DRAWER_TABS.length - 1]
              : undefined;
    if (!next) return;
    e.preventDefault();
    selectTab(next);
    document.getElementById(`drawer-tab-${next}`)?.focus();
  };
  // ⌃` toggles the drawer from anywhere in the builder, matching the hint shown
  // on the status bar.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.ctrlKey && e.key === '`') {
        e.preventDefault();
        setOpen((x) => !x);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);
  const verify = async () => {
    if (!orgId || !rendered) return;
    const content = rendered.content;
    const server = await renderVisual(orgId, doc);
    setVerified({ outcome: server.content === content ? 'match' : 'mismatch', content });
  };
  const selectedNode =
    selected.length === 1 ? doc.nodes.find((n) => n.id === selected[0]) : undefined;
  const runRelabel = async () => {
    if (!orgId) return;
    const rules =
      selectedNode && Array.isArray(selectedNode.props.rules) ? selectedNode.props.rules : [];
    setRelabelResult(await simulateRelabel(orgId, { rules, sample_targets: [] }));
  };
  const runLogs = async () => {
    if (!orgId) return;
    const stages =
      selectedNode && Array.isArray(selectedNode.props.stage)
        ? selectedNode.props.stage
        : selectedNode && Array.isArray(selectedNode.props.stages)
          ? selectedNode.props.stages
          : [];
    setLogsResult(await simulateLogs(orgId, { stages, sample_lines: [] }));
  };
  const labels = (value: Record<string, string> | undefined) => JSON.stringify(value ?? {});
  const relabelPanel =
    selectedNode?.component === 'prometheus.relabel' || !selectedNode ? (
      <>
        <button
          className='border rounded px-2 py-1 text-xs disabled:opacity-50 disabled:cursor-not-allowed'
          data-testid='simulate-relabel-run'
          onClick={runRelabel}
          disabled={readOnly}
          title={readOnly ? readOnlyReason : undefined}
        >
          Run
        </button>
        <span className='ml-3 text-xs text-muted'>Uses built-in k8s fixture targets</span>
        {!selectedNode && <p className='text-xs text-muted'>Select a relabel node to trace</p>}
        <div className='mt-2 space-y-2'>
          {relabelResult?.traces.map((trace, ti) => (
            <div key={ti} className='flex gap-2 items-start flex-wrap'>
              <div className='border rounded p-2 text-xs'>Input: {labels(trace.input)}</div>
              {trace.steps.map((step, i) => (
                <button
                  key={i}
                  data-testid={`step-card-${i}`}
                  className='border rounded p-2 text-xs text-left'
                  onClick={() => selectedNode && setSelected([selectedNode.id])}
                >
                  Rule {step.rule_index}: {step.action}
                  <br />
                  {labels(step.before)} → {labels(step.after)}{' '}
                </button>
              ))}
              <div className='border rounded p-2 text-xs'>
                {trace.kept ? (
                  `Output: ${labels(trace.output)}`
                ) : (
                  <span data-testid='dropped-badge'>DROPPED</span>
                )}
              </div>
            </div>
          ))}
        </div>
      </>
    ) : (
      <p className='text-xs text-muted'>Select a relabel node to trace</p>
    );
  const logsPanel =
    selectedNode?.component === 'loki.process' || !selectedNode ? (
      <>
        <button
          className='border rounded px-2 py-1 text-xs disabled:opacity-50 disabled:cursor-not-allowed'
          data-testid='simulate-logs-run'
          onClick={runLogs}
          disabled={readOnly}
          title={readOnly ? readOnlyReason : undefined}
        >
          Run
        </button>
        <span className='ml-3 text-xs text-muted'>Uses built-in log fixtures</span>
        {!selectedNode && <p className='text-xs text-muted'>Select a loki.process node to trace</p>}
        <div className='mt-2 space-y-2'>
          {logsResult?.traces.map((trace, ti) => (
            <div key={ti} className='flex gap-2 items-start flex-wrap'>
              <div className='border rounded p-2 text-xs'>Input: {trace.input}</div>
              {trace.steps.map((step, i) => (
                <button
                  key={i}
                  data-testid={`step-card-${i}`}
                  className='border rounded p-2 text-xs text-left'
                  onClick={() => selectedNode && setSelected([selectedNode.id])}
                >
                  Stage {step.stage_index}: {step.stage_type}
                  <br />
                  {step.line_before} → {step.line_after}
                  {step.note && (
                    <>
                      <br />
                      {step.note}
                    </>
                  )}
                </button>
              ))}
              <div className='border rounded p-2 text-xs'>
                {trace.dropped ? (
                  <span data-testid='dropped-badge'>DROPPED</span>
                ) : (
                  `Output: ${trace.output ?? ''}`
                )}
              </div>
            </div>
          ))}
        </div>
      </>
    ) : (
      <p className='text-xs text-muted'>Select a loki.process node to trace</p>
    );
  // F10: this used to show diagnostics.length (errors + warnings) while the
  // toolbar's chip counted only blocking `error`s — the two disagreed
  // whenever a graph had any warning. Both now count the same thing.
  const errorCount = diagnostics.filter((d) => d.severity === 'error').length;
  const warningCount = diagnostics.length - errorCount;
  return (
    <div
      className={`bg-panel border-t border-border flex flex-col shrink-0 ${open ? 'h-64' : 'h-8'}`}
      data-testid='bottom-drawer'
    >
      <div className='flex items-center border-b border-border h-8 px-2 gap-1 text-xs shrink-0'>
        {/* #251: a real tablist — the three labels used to be plain buttons
            with no selected state, so nothing said which panel was showing. */}
        <div
          role='tablist'
          aria-label='Builder drawer'
          className='flex items-stretch h-full gap-1'
          onKeyDown={onTabKeyDown}
        >
          {DRAWER_TABS.map((t) => {
            const selectedTab = tab === t;
            return (
              <button
                key={t}
                type='button'
                role='tab'
                id={`drawer-tab-${t}`}
                data-testid={`drawer-tab-${t}`}
                aria-selected={selectedTab}
                aria-controls={open && selectedTab ? 'drawer-tabpanel' : undefined}
                tabIndex={selectedTab ? 0 : -1}
                onClick={() => selectTab(t)}
                className={`px-2 -mb-px border-b-2 ${
                  t === 'problems'
                    ? `font-medium ${tabClass(selectedTab, errorCount ? 'text-red-400' : 'text-emerald-400')}`
                    : tabClass(selectedTab)
                }`}
              >
                {t === 'problems' ? (
                  <>
                    Problems {errorCount}
                    {warningCount > 0 &&
                      ` · ${warningCount} warning${warningCount !== 1 ? 's' : ''}`}
                  </>
                ) : t === 'code' ? (
                  'Generated config'
                ) : (
                  'Simulation'
                )}
              </button>
            );
          })}
        </div>
        <kbd
          className='ml-auto text-[10px] font-mono text-muted-2 border border-border-strong rounded px-1 py-0.5'
          title='Toggle drawer'
        >
          ⌃`
        </kbd>
        <button
          type='button'
          data-testid='drawer-toggle'
          aria-expanded={open}
          aria-label={open ? 'Collapse drawer' : 'Expand drawer'}
          onClick={() => setOpen((x) => !x)}
          className='text-muted-2 hover:text-muted'
        >
          {open ? <ChevronDown size={14} /> : <ChevronUp size={14} />}
        </button>
      </div>
      {open && (
        <div
          role='tabpanel'
          id='drawer-tabpanel'
          aria-labelledby={`drawer-tab-${tab}`}
          className='flex-1 overflow-y-auto p-2'
        >
          {tab === 'problems' ? (
            diagnostics.length ? (
              diagnostics.map((d, i) => (
                <div key={i} data-testid='problem-row' className='text-xs'>
                  {d.layer} {d.code}: {d.message}
                </div>
              ))
            ) : (
              <p className='text-xs text-muted'>No problems.</p>
            )
          ) : tab === 'code' ? (
            <div data-testid='code-tab-content'>
              <button
                className='border rounded px-2 py-1 text-xs mb-2 disabled:opacity-50 disabled:cursor-not-allowed'
                onClick={verify}
                disabled={readOnly}
                title={readOnly ? readOnlyReason : undefined}
              >
                Verify render
              </button>
              {verified && rendered?.content === verified.content && (
                <div
                  data-testid='verify-render-result'
                  className={`text-xs ${verified.outcome === 'mismatch' ? 'text-red-500' : 'text-emerald-500'}`}
                >
                  {verified.outcome === 'mismatch'
                    ? 'Server and client render differ'
                    : 'Server render matches'}
                </div>
              )}
              <pre className='font-mono text-xs whitespace-pre-wrap'>{rendered?.content ?? ''}</pre>
            </div>
          ) : (
            <div data-testid='simulate-panel'>
              <div
                role='tablist'
                aria-label='Simulation'
                className='flex gap-1 mb-2 border-b border-border text-xs'
              >
                {(['relabel', 'logs'] as const).map((t) => (
                  <button
                    key={t}
                    type='button'
                    role='tab'
                    id={`simulate-${t}-tab`}
                    data-testid={`simulate-${t}-tab`}
                    aria-selected={simulateTab === t}
                    aria-controls='simulate-tabpanel'
                    onClick={() => setSimulateTab(t)}
                    className={`px-2 py-1 -mb-px border-b-2 ${tabClass(simulateTab === t)}`}
                  >
                    {t === 'relabel' ? 'Relabel trace' : 'Log stage trace'}
                  </button>
                ))}
              </div>
              <div
                role='tabpanel'
                id='simulate-tabpanel'
                aria-labelledby={`simulate-${simulateTab}-tab`}
                data-testid={
                  simulateTab === 'relabel' ? 'simulate-relabel-panel' : 'simulate-logs-panel'
                }
              >
                {simulateTab === 'relabel' ? relabelPanel : logsPanel}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
