import { Handle, type Node, type NodeProps, Position, useUpdateNodeInternals } from '@xyflow/react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { memo, useEffect } from 'react';
import type { GroupPort } from '../graphViewLayout';
import { portRowTop } from '../graphViewLayout';

/**
 * A module group on the read-only graph view: an Alloy `declare` that
 * GraphView expanded into its components (graphViewLayout.ts). Expanded, it is
 * a React Flow parent node whose children sit inside it; collapsed, a compact
 * box. Either way its ports — the module's arguments and exports that carry
 * data — sit on the side data crosses the module boundary on, at the offsets
 * ELK gave them (or in rows, for the server-grid fallback).
 */
export interface ModuleGroupData {
  [key: string]: unknown;
  id: string;
  label: string;
  childCount: number;
  collapsed: boolean;
  /** False on the server-grid fallback: children are not inside the box
   *  there, so there is nothing to expand or collapse. */
  collapsible: boolean;
  ports: GroupPort[];
  /** Port row offsets from ELK by GroupPort.key; rows are used when absent. */
  portTops?: Record<string, number>;
  onToggle?: (id: string) => void;
}

export type ModuleGroupFlowNode = Node<ModuleGroupData, 'module'>;

export const ModuleGroupNode = memo(function ModuleGroupNode({
  data,
  selected,
}: NodeProps<ModuleGroupFlowNode>) {
  const updateNodeInternals = useUpdateNodeInternals();
  // Handles move when ELK re-places the ports; React Flow caches their bounds.
  useEffect(() => {
    updateNodeInternals(data.id);
  }, [data.id, data.ports, data.portTops, data.collapsed, updateNodeInternals]);

  const rowIndex = { left: 0, right: 0 };
  const placed = data.ports.map((p) => {
    const top = data.portTops?.[p.key] ?? portRowTop(rowIndex[p.side]);
    rowIndex[p.side] += 1;
    return { ...p, top };
  });
  const name = `module ${data.label}`;

  return (
    <div
      data-testid='module-group'
      data-node-id={data.id}
      data-collapsed={data.collapsed ? 'true' : 'false'}
      role='group'
      aria-label={`Module ${data.label}`}
      className={[
        'relative h-full w-full rounded-lg border border-dashed text-xs',
        'border-border-strong bg-panel/60',
        selected ? 'shadow-[0_0_0_3px_rgba(99,102,241,0.25)]' : '',
      ].join(' ')}
    >
      <div className='flex h-9 items-center gap-1.5 border-b border-dashed border-border-strong px-2'>
        {data.collapsible ? (
          <button
            type='button'
            data-testid='module-group-toggle'
            aria-label={`${data.collapsed ? 'Expand' : 'Collapse'} ${name}`}
            aria-expanded={!data.collapsed}
            className='nodrag rounded p-0.5 text-muted hover:bg-accent/20'
            onClick={(e) => {
              e.stopPropagation();
              data.onToggle?.(data.id);
            }}
          >
            {data.collapsed ? <ChevronRight size={14} /> : <ChevronDown size={14} />}
          </button>
        ) : null}
        <span className='font-mono text-muted'>declare</span>
        <span className='truncate font-mono font-semibold'>{data.label}</span>
        <span className='ml-auto shrink-0 text-muted' data-testid='module-group-count'>
          {data.childCount} {data.childCount === 1 ? 'component' : 'components'}
        </span>
      </div>
      {placed.map((p) =>
        p.handles.map((h) => (
          <Handle
            key={`${h.rfType}:${h.handleId}`}
            id={h.handleId}
            type={h.rfType}
            position={p.side === 'left' ? Position.Left : Position.Right}
            isConnectable={false}
            style={{ top: p.top }}
            title={p.label}
            className='!h-2.5 !w-2.5 !border-2 !border-border-strong !bg-card'
          />
        )),
      )}
      {/* Labelled inside the compact box only: on an expanded group a label
          would sit over the children next to the port; the handle's title
          names it there. */}
      {(data.collapsed || !data.collapsible) &&
        placed.map((p) => (
          <span
            key={`label:${p.key}`}
            data-testid='module-group-port'
            className={[
              'pointer-events-none absolute -translate-y-1/2 font-mono italic text-muted',
              p.side === 'left' ? 'left-2' : 'right-2',
            ].join(' ')}
            style={{ top: p.top }}
          >
            {p.label}
          </span>
        ))}
    </div>
  );
});
