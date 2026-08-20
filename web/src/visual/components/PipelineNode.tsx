import {
  Handle,
  type Node,
  type NodeProps,
  Position,
  useConnection,
  useUpdateNodeInternals,
} from '@xyflow/react';
import { memo, useCallback, useEffect, useMemo, useState } from 'react';
import { getCategoryColor, getWireColor, portHandleId } from '../schemaAdapter';
import { type SimHealthEntry, selectConnectionState, useVisualStore } from '../store';
import type { ComponentDef, GraphNode, L1Diagnostic, PortDef } from '../types';

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

// --- D1: port ROLE, independent of the schema's input/output classification ---
//
// docs/reviews/README.md D1 (product owner's ruling, not to be relitigated): the
// canvas always presents left-to-right dataflow. Alloy has two reference
// patterns — a DATA export that the *consumer* references (discovery.Target,
// e.g. `prometheus.scrape.targets`) and a RECEIVER export that the *producer*
// references (storage.Appendable / loki.LogsReceiver / otelcol.Consumer /
// pyroscope.Appendable, e.g. `prometheus.remote_write.receiver`, referenced via
// `forward_to`). Because of that inversion, whether a port renders on the
// node's LEFT ("accepts" — data enters here) or RIGHT ("produces" — data
// leaves here) depends on the port's wire-type KIND, not on whether the schema
// calls it an "input" (argument) or "output" (export):
//   - a receiver-kind EXPORT (e.g. remote_write.receiver) is where data
//     conceptually enters the node -> "accepts" (left), even though the
//     schema calls it an output.
//   - a receiver-kind ARGUMENT (e.g. scrape.forward_to) is where data leaves
//     the node -> "produces" (right), even though the schema calls it an
//     input.
//   - a data-kind port behaves the ordinary way: an export produces (right),
//     an argument accepts (left).
// This file only decides layout (which side, which row) from the role; it
// does NOT change which ports are React Flow `source` vs `target` handles —
// that's the existing input/output classification, and CanvasPane's
// connection/edge-direction logic (out of this file's ownership) depends on
// it staying put. Every wire type here is a receiver-kind Go interface except
// `targets` (discovery.Target, the one DATA-kind wire type in the schema).
const RECEIVER_WIRE_TYPES = new Set<string>([
  'prom.metrics',
  'loki.logs',
  'otel.traces',
  'otel.metrics',
  'otel.logs',
  'otel.any',
  'pyroscope.profiles',
]);

export type PortRole = 'accepts' | 'produces';

/** Resolves a port's D1 role from its wire type and whether it's an argument
 * (schema `inputs`) or an export (schema `outputs`). Pure and exported so it's
 * directly unit-testable, same pattern as `selectConnectionState` in store.ts. */
export function portRole(wireType: string, kind: 'input' | 'output'): PortRole {
  const isReceiverKind = RECEIVER_WIRE_TYPES.has(wireType);
  if (kind === 'output') return isReceiverKind ? 'accepts' : 'produces';
  return isReceiverKind ? 'produces' : 'accepts';
}

export interface PortLayout {
  handleId: string;
  label: string;
  wireType: string;
  /** React Flow handle type — unchanged from the schema's input/output
   * classification; see the role comment above. */
  rfType: 'source' | 'target';
  role: PortRole;
  /** Vertical center of this port's row, in px from the ports container's
   * padding edge — see PORT_ROW_* below. Every port in the same column gets a
   * distinct value, which is the fix for the CRITICAL defect this task
   * addresses: React Flow's default `top: 50%` put every handle on a node at
   * the identical point (measured: prometheus.scrape's `targets` and
   * `forward_to` both at (736,512), docs/reviews/canvas-ux-and-forms.md F3). */
  top: number;
}

// Row geometry for the port list. Kept as named constants — rather than left
// to the browser to derive from the Tailwind classes below — so a port's
// pixel position is computed once, deterministically, in a plain function
// PipelineNode.test.ts can assert on without mounting a real layout engine.
// The row markup (`h-4 flex items-center`, `gap-1`, container `py-1`) must
// keep matching these numbers.
const PORT_ROW_HEIGHT_PX = 16; // h-4
const PORT_ROW_GAP_PX = 4; // gap-1
const PORT_LIST_PADDING_TOP_PX = 4; // ports container's py-1

function portRowCenterPx(index: number): number {
  return (
    PORT_LIST_PADDING_TOP_PX +
    index * (PORT_ROW_HEIGHT_PX + PORT_ROW_GAP_PX) +
    PORT_ROW_HEIGHT_PX / 2
  );
}

/** Groups a component's ports by D1 role (not by the schema's input/output
 * split) and assigns each port in a column its own row, so no two handles in
 * the same column ever share a coordinate. */
export function layoutPorts(def: ComponentDef | undefined): {
  left: PortLayout[];
  right: PortLayout[];
} {
  const left: PortLayout[] = [];
  const right: PortLayout[] = [];
  const place = (
    port: PortDef,
    i: number,
    kind: 'input' | 'output',
    rfType: 'source' | 'target',
  ) => {
    const entry: PortLayout = {
      handleId: portHandleId(port, i),
      label: (kind === 'input' ? port.prop : port.export) ?? '',
      wireType: port.type,
      rfType,
      role: portRole(port.type, kind),
      top: 0, // assigned below, once each column's final membership is known
    };
    (entry.role === 'accepts' ? left : right).push(entry);
  };
  (def?.inputs ?? []).forEach((p, i) => place(p, i, 'input', 'target'));
  (def?.outputs ?? []).forEach((p, i) => place(p, i, 'output', 'source'));
  left.forEach((entry, i) => {
    entry.top = portRowCenterPx(i);
  });
  right.forEach((entry, i) => {
    entry.top = portRowCenterPx(i);
  });
  return { left, right };
}

export interface PipelineNodeData extends GraphNode {
  [key: string]: unknown;
  schema?: ComponentDef;
  diagnostics?: L1Diagnostic[];
  readOnly?: boolean;
  /** Design doc §6.4 step 4: per-node health from the sandbox run currently
   * being viewed, projected onto the node the same way `diagnostics` is —
   * see store.ts's `simHealthByNode`. */
  health?: SimHealthEntry;
}
type PipelineFlowNode = Node<PipelineNodeData, 'pipeline'>;

// health_state values mirror Alloy's own /api/v0/web/components verbatim
// (VB-1 §6.4) — this is the closed set that endpoint actually emits.
const HEALTH_BADGE_COLOR: Record<string, string> = {
  healthy: '#22c55e',
  unhealthy: '#ef4444',
  exited: '#f59e0b',
  unknown: '#71717a',
};

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
  // D1: ports grouped by role (accepts/left, produces/right) with each port
  // in a column assigned its own row — see layoutPorts above.
  const { left: acceptPorts, right: producePorts } = useMemo(() => layoutPorts(def), [def]);

  // React Flow caches each handle's bounds when it measures the node. Because
  // layoutPorts gives every port its own `top`, the handles move after that
  // measurement — and stale bounds mean a drop lands nowhere, so the wire is
  // silently discarded even though the gesture and validation were correct.
  // Telling RF to re-measure whenever the port layout changes is what makes
  // multi-port nodes connectable at all.
  const updateNodeInternals = useUpdateNodeInternals();
  useEffect(() => {
    updateNodeInternals(node.id);
  }, [node.id, acceptPorts, producePorts, updateNodeInternals]);
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
        'relative bg-card rounded-lg w-60 text-xs select-none shadow-md',
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
      {node.health && (
        <div
          data-testid='node-health-badge'
          data-health-state={node.health.health_state}
          title={`${node.health.health_state}${node.health.message ? `: ${node.health.message}` : ''}`}
          className='absolute -top-1.5 -right-1.5 w-3 h-3 rounded-full ring-2 ring-background z-10'
          style={{
            backgroundColor:
              HEALTH_BADGE_COLOR[node.health.health_state] ?? HEALTH_BADGE_COLOR.unknown,
          }}
        />
      )}
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
          {acceptPorts.map((p) => {
            const compatible = connState.validPortIds.includes(p.handleId);
            const isSnappedPort = compatible && p.handleId === snappedHandleId;
            const highlight = compatible || isSnappedPort;
            return (
              <div key={p.handleId} className='h-4 flex items-center'>
                <Handle
                  type={p.rfType}
                  position={Position.Left}
                  id={p.handleId}
                  style={{
                    top: `${p.top}px`,
                    ...(highlight
                      ? undefined
                      : { backgroundColor: getWireColor(schema, p.wireType) }),
                  }}
                  className={[
                    '!w-3 !h-3 !rounded-full border-0 ring-2 ring-background',
                    highlight ? GREEN_HANDLE_CLASS : '',
                    connState.dragActive && !highlight ? 'opacity-30' : '',
                  ].join(' ')}
                />
                <span className={highlight ? GREEN_TEXT_CLASS : undefined}>
                  {p.label}
                  {isSnappedPort ? ' ✓' : ''}
                </span>
              </div>
            );
          })}
        </div>
        <div className='flex flex-col gap-1 items-end'>
          {producePorts.map((p) => {
            const compatible = connState.validPortIds.includes(p.handleId);
            const isSnappedPort = compatible && p.handleId === snappedHandleId;
            const highlight = compatible || isSnappedPort;
            return (
              <div key={p.handleId} className='h-4 flex items-center'>
                <span className={highlight ? GREEN_TEXT_CLASS : undefined}>
                  {isSnappedPort ? '✓ ' : ''}
                  {p.label}
                </span>
                <Handle
                  type={p.rfType}
                  position={Position.Right}
                  id={p.handleId}
                  style={{
                    top: `${p.top}px`,
                    ...(highlight
                      ? undefined
                      : { backgroundColor: getWireColor(schema, p.wireType) }),
                  }}
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
