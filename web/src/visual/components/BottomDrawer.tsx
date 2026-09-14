import { ChevronDown, ChevronUp } from 'lucide-react';
import { useEffect, useState } from 'react';
import {
  type LineTrace,
  renderVisual,
  simulateLogs,
  simulateRelabel,
  type TargetTrace,
} from '../../api/client';
import { useOrgId } from '../../hooks/useOrg';
import { renderTS } from '../renderTS';
import { useVisualStore } from '../store';
import { useDebouncedValue } from '../useDebouncedValue';
export function BottomDrawer() {
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<'problems' | 'code' | 'simulate'>('problems');
  const diagnostics = useVisualStore((s) => s.diagnostics);
  const doc = useVisualStore((s) => s.doc);
  const schema = useVisualStore((s) => s.schema);
  const selected = useVisualStore((s) => s.selected);
  const setSelected = useVisualStore((s) => s.setSelected);
  const orgId = useOrgId();
  const [simulateTab, setSimulateTab] = useState<'relabel' | 'logs'>('relabel');
  const [relabelResult, setRelabelResult] = useState<{ traces: TargetTrace[] }>();
  const [logsResult, setLogsResult] = useState<{ traces: LineTrace[] }>();
  // Tri-state: null before the first Verify click, then whichever the last
  // server render reported (F3: this used to read a window-global the test
  // harness injects but production never sets, and rendered nothing on a
  // match — silently dead outside the mocked test suite).
  const [serverMismatch, setServerMismatch] = useState<null | 'match' | 'mismatch'>(null);
  // The outcome is a claim about the doc AS OF THE LAST VERIFY CLICK. Once
  // the user edits the graph it's stale — most visibly when it's a green
  // "Server render matches" that no longer matches anything — so clear it
  // on every doc change and require a fresh Verify click to re-earn it.
  useEffect(() => {
    setServerMismatch(null);
  }, [doc]);
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
  const selectTab = (t: 'problems' | 'code' | 'simulate') => {
    setTab(t);
    setOpen(true);
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
    const server = await renderVisual(orgId, doc);
    setServerMismatch(server.content === rendered.content ? 'match' : 'mismatch');
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
          className='border rounded px-2 py-1 text-xs'
          data-testid='simulate-relabel-run'
          onClick={runRelabel}
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
          className='border rounded px-2 py-1 text-xs'
          data-testid='simulate-logs-run'
          onClick={runLogs}
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
      <div className='flex items-center border-b border-border h-8 px-2 gap-3 text-xs shrink-0'>
        <button
          data-testid='drawer-tab-problems'
          onClick={() => selectTab('problems')}
          className={`font-medium ${errorCount ? 'text-red-400' : 'text-emerald-400'}`}
        >
          Problems {errorCount}
          {warningCount > 0 && ` · ${warningCount} warning${warningCount !== 1 ? 's' : ''}`}
        </button>
        <button
          data-testid='drawer-tab-code'
          onClick={() => selectTab('code')}
          className='text-muted hover:text-muted-2'
        >
          Generated config
        </button>
        <button
          data-testid='drawer-tab-simulate'
          onClick={() => selectTab('simulate')}
          className='text-muted hover:text-muted-2'
        >
          Simulation
        </button>
        <kbd
          className='ml-auto text-[10px] font-mono text-muted-2 border border-border-strong rounded px-1 py-0.5'
          title='Toggle drawer'
        >
          ⌃`
        </kbd>
        <button
          data-testid='drawer-toggle'
          aria-label={open ? 'Collapse drawer' : 'Expand drawer'}
          onClick={() => setOpen((x) => !x)}
          className='text-muted-2 hover:text-muted'
        >
          {open ? <ChevronDown size={14} /> : <ChevronUp size={14} />}
        </button>
      </div>
      {open && (
        <div className='flex-1 overflow-y-auto p-2'>
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
              <button className='border rounded px-2 py-1 text-xs mb-2' onClick={verify}>
                Verify render
              </button>
              {serverMismatch && (
                <div
                  data-testid='verify-render-result'
                  className={`text-xs ${serverMismatch === 'mismatch' ? 'text-red-500' : 'text-emerald-500'}`}
                >
                  {serverMismatch === 'mismatch'
                    ? 'Server and client render differ'
                    : 'Server render matches'}
                </div>
              )}
              <pre className='font-mono text-xs whitespace-pre-wrap'>{rendered?.content ?? ''}</pre>
            </div>
          ) : (
            <div data-testid='simulate-panel'>
              <div className='flex gap-1 mb-2'>
                <button
                  data-testid='simulate-relabel-tab'
                  onClick={() => setSimulateTab('relabel')}
                >
                  Relabel trace
                </button>
                <button data-testid='simulate-logs-tab' onClick={() => setSimulateTab('logs')}>
                  Log stage trace
                </button>
              </div>
              <div
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
