import { ChevronDown, ChevronUp } from 'lucide-react';
import { useState } from 'react';
import {
  type LineTrace,
  renderVisual,
  simulateLogs,
  simulateRelabel,
  type TargetTrace,
} from '../../api/client';
import { useMe } from '../../hooks/useMe';
import { renderTS } from '../renderTS';
import { useVisualStore } from '../store';
export function BottomDrawer() {
  const [open, setOpen] = useState(true);
  const [tab, setTab] = useState<'problems' | 'code' | 'simulate'>('problems');
  const diagnostics = useVisualStore((s) => s.diagnostics);
  const doc = useVisualStore((s) => s.doc);
  const schema = useVisualStore((s) => s.schema);
  const selected = useVisualStore((s) => s.selected);
  const setSelected = useVisualStore((s) => s.setSelected);
  const { data: me } = useMe();
  const [simulateTab, setSimulateTab] = useState<'relabel' | 'logs'>('relabel');
  const [relabelResult, setRelabelResult] = useState<{ traces: TargetTrace[] }>();
  const [logsResult, setLogsResult] = useState<{ traces: LineTrace[] }>();
  const [serverMismatch, setServerMismatch] = useState(false);
  const rendered = schema ? renderTS(doc, schema) : null;
  const verify = async () => {
    const org = (window as unknown as { __initialMe?: { orgs?: Array<{ id: string }> } })
      .__initialMe?.orgs?.[0]?.id;
    if (!org || !rendered) return;
    const server = await renderVisual(org, doc);
    setServerMismatch(server.content !== rendered.content);
  };
  const selectedNode =
    selected.length === 1 ? doc.nodes.find((n) => n.id === selected[0]) : undefined;
  const runRelabel = async () => {
    if (!me?.orgs[0]?.id) return;
    const rules =
      selectedNode && Array.isArray(selectedNode.props.rules) ? selectedNode.props.rules : [];
    setRelabelResult(
      await simulateRelabel(me.orgs[0].id, { rules, sample_targets: [] }),
    );
  };
  const runLogs = async () => {
    if (!me?.orgs[0]?.id) return;
    const stages =
      selectedNode && Array.isArray(selectedNode.props.stage)
        ? selectedNode.props.stage
        : selectedNode && Array.isArray(selectedNode.props.stages)
          ? selectedNode.props.stages
          : [];
    setLogsResult(await simulateLogs(me.orgs[0].id, { stages, sample_lines: [] }));
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
        <span className='ml-3 text-xs text-muted-foreground'>Uses built-in k8s fixture targets</span>
        {!selectedNode && (
          <p className='text-xs text-muted-foreground'>Select a relabel node to trace</p>
        )}
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
      <p className='text-xs text-muted-foreground'>Select a relabel node to trace</p>
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
        <span className='ml-3 text-xs text-muted-foreground'>Uses built-in log fixtures</span>
        {!selectedNode && (
          <p className='text-xs text-muted-foreground'>Select a loki.process node to trace</p>
        )}
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
      <p className='text-xs text-muted-foreground'>Select a loki.process node to trace</p>
    );
  return (
    <div
      className={`border-t flex flex-col shrink-0 ${open ? 'h-64' : 'h-9'}`}
      data-testid='bottom-drawer'
    >
      <div className='flex items-center border-b h-9 px-2 gap-1'>
        <button data-testid='drawer-tab-problems' onClick={() => setTab('problems')}>
          Problems{diagnostics.length ? ` (${diagnostics.length})` : ''}
        </button>
        <button data-testid='drawer-tab-code' onClick={() => setTab('code')}>
          Code
        </button>
        <button data-testid='drawer-tab-simulate' onClick={() => setTab('simulate')}>
          Simulate
        </button>
        <button
          className='ml-auto'
          aria-label={open ? 'Collapse drawer' : 'Expand drawer'}
          onClick={() => setOpen((x) => !x)}
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
              <p className='text-xs text-muted-foreground'>No problems.</p>
            )
          ) : tab === 'code' ? (
            <div data-testid='code-tab-content'>
              <button className='border rounded px-2 py-1 text-xs mb-2' onClick={verify}>
                Verify render
              </button>
              {serverMismatch && (
                <div className='text-xs text-red-500'>Server and client render differ.</div>
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
