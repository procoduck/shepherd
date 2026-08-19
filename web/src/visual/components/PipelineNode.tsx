import { Handle, type Node, type NodeProps, Position, useConnection } from '@xyflow/react';
import { memo, useCallback, useMemo, useState } from 'react';
import { getCategoryColor, getWireColor, portHandleId } from '../schemaAdapter';
import { selectConnectionState, useVisualStore } from '../store';
import type { ComponentDef, GraphNode, L1Diagnostic } from '../types';

// Small 13px stroke icons, one per component category — drawn inline rather
// than pulled from an icon library since the set is fixed and tiny.
type Category = 'sources' | 'transform' | 'destinations' | 'config' | 'advanced';
const CATEGORY_ICON_PATHS: Record<Category, string> = {
  // Arrow flowing down into a tray — data originating here.
  sources: 'M6.5 1.5v6.2M4 5.4l2.5 2.5 2.5-2.5M2 11h9',
  // Funnel — narrowing/reshaping the stream.
  transform: 'M2 2h9L7.6 6.6v4l-2.2 1.1v-5.1L2 2z',
  // Arrow leaving a box — data shipped onward.
  destinations: 'M3.2 9.3L9.3 3.2M5.3 3.2h4v4M3 9.8h.01',
  // Gear-ish tick marks around a hub.
  config:
    'M6.5 1.7v1.4M6.5 9.4v1.4M1.7 6.5h1.4M9.4 6.5h1.4M3.3 3.3l1 1M8.7 3.3l-1 1M3.3 9.7l1-1M8.7 9.7l-1-1',
  // Equalizer sliders — everything else.
  advanced: 'M2 3.6h9M2 6.5h9M2 9.4h9M5 3.6v0M8 6.5v0M4 9.4v0',
};
function CategoryIcon({ category, className }: { category: string; className?: string }) {
  const key = (category in CATEGORY_ICON_PATHS ? category : 'advanced') as Category;
  return (
    <svg
      width={13}
      height={13}
      viewBox='0 0 13 13'
      fill='none'
      stroke='currentColor'
      strokeWidth={1.2}
      strokeLinecap='round'
      strokeLinejoin='round'
      aria-hidden='true'
      className={className}
      data-testid='node-category-icon'
    >
      <path d={CATEGORY_ICON_PATHS[key]} />
      {key === 'config' && <circle cx={6.5} cy={6.5} r={1.6} />}
      {key === 'advanced' && (
        <>
          <circle cx={5} cy={3.6} r={0.9} fill='currentColor' stroke='none' />
          <circle cx={8} cy={6.5} r={0.9} fill='currentColor' stroke='none' />
          <circle cx={4} cy={9.4} r={0.9} fill='currentColor' stroke='none' />
        </>
      )}
    </svg>
  );
}

export interface PipelineNodeData extends GraphNode {
  [key: string]: unknown;
  schema?: ComponentDef;
  diagnostics?: L1Diagnostic[];
  readOnly?: boolean;
}
type PipelineFlowNode = Node<PipelineNodeData, 'pipeline'>;

// The four connection-drag affordance states (A2), exposed as data-drop-state
// on the node root for styling AND for Playwright to assert on directly
// instead of scraping arbitrary classes.
type DropState = 'idle' | 'valid' | 'snapped' | 'dimmed';

// Tailwind's scanner needs literal class strings to find these — NOT built via
// template interpolation of a shared color constant (`${GREEN}`), which it
// can't statically discover. Keep this hex in sync with the box-shadow/text
// literals below and with `#22c55e` in the design mock ("Connection drag states").
const GREEN_TEXT_CLASS = 'text-[#22c55e]';
const GREEN_HANDLE_CLASS = '!w-4 !h-4 !bg-[#22c55e] ring-[#22c55e]';
const GREEN_BORDER_CLASS = 'border-t-[#22c55e] border-r-[#22c55e] border-b-[#22c55e]';

export const PipelineNode = memo(function PipelineNode({
  data,
  selected,
}: NodeProps<PipelineFlowNode>) {
  const node = data;
  const setLabel = useVisualStore((s) => s.setLabel);
  const schema = useVisualStore((s) => s.schema);
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(node.label);
  const def = node.schema;

  // A3: connectingFrom lives in the store, not in node data, so starting/ending
  // a drag doesn't touch rfNodes — every PipelineNode's props stay referentially
  // stable across the gesture. We subscribe to the raw value (changes exactly
  // twice per drag: start and end — same cheap pattern CanvasPane uses for its
  // connectionLineStyle) and derive this node's own booleans/ids in a plain
  // `useMemo`, rather than doing the derivation inside the zustand selector
  // itself: a zustand selector that allocates a fresh object per call (the
  // "valid" branch of `selectConnectionState` does — it can't return a shared
  // constant since `validPortIds` varies per node) fed through `useShallow`
  // triggered React's "getSnapshot should be cached" infinite-loop guard, so we
  // do the actual derivation on the React side instead. `selectConnectionState`
  // stays exported as a pure function so it's directly unit-testable (A3 vitest)
  // independent of this wiring choice.
  const connectingFrom = useVisualStore((s) => s.connectingFrom);
  const connState = useMemo(
    () => selectConnectionState(connectingFrom, node.id, def),
    [connectingFrom, node.id, def],
  );
  // The "snapped" state (hovering inside connectionRadius of a valid handle)
  // isn't in our store — React Flow tracks it live during the gesture. Its
  // `useConnection` hook is the reliable, typed way to read it (React Flow
  // v12); we pass a selector so this only re-renders THIS node when the
  // hovered handle id changes to/from one of its own, not on every pointer
  // move. (The alternative — scraping the `.connectingto` CSS class React
  // Flow toggles on handle DOM nodes — isn't selector-friendly and isn't
  // typed, so we didn't use it.)
  const snappedHandleId = useConnection((c) =>
    c.inProgress && c.toNode?.id === node.id && c.isValid === true
      ? (c.toHandle?.id ?? null)
      : null,
  );

  const dropState: DropState = !connState.dragActive
    ? 'idle'
    : snappedHandleId != null
      ? 'snapped'
      : connState.isValidTarget
        ? 'valid'
        : 'dimmed';

  const commit = useCallback(() => {
    setEditing(false);
    if (value.trim()) setLabel(node.id, value.trim());
  }, [node.id, setLabel, value]);
  const errors = (node.diagnostics ?? []).filter(
    (d) => d.node_id === node.id && d.severity === 'error',
  ).length;
  const warnings = (node.diagnostics ?? []).filter(
    (d) => d.node_id === node.id && d.severity === 'warning',
  ).length;

  const categoryColor = getCategoryColor(schema, def?.category ?? 'advanced');

  const borderColorClass =
    dropState === 'valid' || dropState === 'snapped'
      ? GREEN_BORDER_CLASS
      : selected
        ? 'border-t-accent border-r-accent border-b-accent'
        : 'border-t-border border-r-border border-b-border';
  const dropShadowClass =
    dropState === 'snapped'
      ? 'shadow-[0_0_0_3px_rgba(34,197,94,0.45)]'
      : dropState === 'valid'
        ? 'shadow-[0_0_0_2px_rgba(34,197,94,0.3)]'
        : selected
          ? 'shadow-[0_0_0_3px_rgba(99,102,241,0.25)]'
          : '';

  return (
    <div
      className={[
        'bg-card rounded-lg w-60 text-xs select-none shadow-md',
        'border-t border-r border-b border-l-[3px]',
        borderColorClass,
        errors ? 'border-l-red-500' : '',
        node.disabled ? 'opacity-50 border-dashed' : '',
        dropState === 'dimmed' ? 'opacity-40' : '',
        dropState === 'snapped' ? 'bg-[#131f17]' : '',
        dropShadowClass,
      ].join(' ')}
      style={errors ? undefined : { borderLeftColor: categoryColor }}
      data-node-id={node.id}
      data-testid='pipeline-node'
      data-drop-state={dropState}
    >
      <div className='px-2 py-1.5 border-b border-border flex items-center gap-1.5 font-mono truncate'>
        <CategoryIcon category={def?.category ?? 'advanced'} className='shrink-0 text-muted-2' />
        <span className='truncate'>{node.component}</span>
      </div>
      <div className='px-2 py-0.5 text-muted'>
        {editing ? (
          <input
            autoFocus
            data-testid='node-label-input'
            className='w-full border border-border-strong rounded px-1 bg-panel'
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onBlur={commit}
            onKeyDown={(e) => e.key === 'Enter' && commit()}
          />
        ) : (
          <span
            data-testid='node-label'
            onDoubleClick={
              node.readOnly
                ? undefined
                : () => {
                    setEditing(true);
                    setValue(node.label);
                  }
            }
            className='cursor-text'
          >
            &ldquo;{node.label}&rdquo;
          </span>
        )}
      </div>
      <div className='relative flex justify-between px-2 py-1 min-h-[24px]'>
        <div className='flex flex-col gap-1'>
          {(def?.inputs ?? []).map((p, i) => {
            const handleId = portHandleId(p, i);
            const compatible = connState.validPortIds.includes(handleId);
            const isSnappedPort = compatible && handleId === snappedHandleId;
            const highlight = compatible || isSnappedPort;
            return (
              <div key={handleId}>
                <Handle
                  type='target'
                  position={Position.Left}
                  id={handleId}
                  style={highlight ? undefined : { backgroundColor: getWireColor(schema, p.type) }}
                  className={[
                    '!w-3 !h-3 !rounded-full border-0 ring-2 ring-background',
                    highlight ? GREEN_HANDLE_CLASS : '',
                    connState.dragActive && !highlight ? 'opacity-30' : '',
                  ].join(' ')}
                />
                <span className={highlight ? GREEN_TEXT_CLASS : undefined}>
                  {p.prop}
                  {isSnappedPort ? ' ✓' : ''}
                </span>
              </div>
            );
          })}
        </div>
        <div className='flex flex-col gap-1 items-end'>
          {(def?.outputs ?? []).map((p, i) => {
            const handleId = portHandleId(p, i);
            const compatible = connState.validPortIds.includes(handleId);
            const isSnappedPort = compatible && handleId === snappedHandleId;
            const highlight = compatible || isSnappedPort;
            return (
              <div key={handleId}>
                <span className={highlight ? GREEN_TEXT_CLASS : undefined}>
                  {isSnappedPort ? '✓ ' : ''}
                  {p.export}
                </span>
                <Handle
                  type='source'
                  position={Position.Right}
                  id={handleId}
                  style={highlight ? undefined : { backgroundColor: getWireColor(schema, p.type) }}
                  className={[
                    '!w-3 !h-3 !rounded-full border-0 ring-2 ring-background',
                    highlight ? GREEN_HANDLE_CLASS : '',
                    connState.dragActive && !highlight ? 'opacity-30' : '',
                  ].join(' ')}
                />
              </div>
            );
          })}
        </div>
      </div>
      {(errors || warnings) > 0 && (
        <div className='px-2 py-0.5 border-t border-border'>
          {errors > 0 && (
            <span className='text-red-500' data-testid='node-error-count'>
              ⚠ {errors}
            </span>
          )}
          {warnings > 0 && <span className='text-yellow-500 ml-2'>◊ {warnings}</span>}
        </div>
      )}
    </div>
  );
});
