import { Handle, type Node, type NodeProps, Position } from '@xyflow/react';
import { memo, useCallback, useState } from 'react';
import { portsCompatible } from '../l1';
import { CATEGORY_BORDER, WIRE_COLOR } from '../schemaAdapter';
import { useVisualStore } from '../store';
import type { ComponentDef, GraphNode, L1Diagnostic } from '../types';
export interface PipelineNodeData extends GraphNode {
  [key: string]: unknown;
  schema?: ComponentDef;
  diagnostics?: L1Diagnostic[];
  readOnly?: boolean;
  connectingFrom?: {
    nodeId: string;
    handleId: string;
    handleType: 'source' | 'target';
    wireType: string | null;
  } | null;
}
type PipelineFlowNode = Node<PipelineNodeData, 'pipeline'>;
export const PipelineNode = memo(function PipelineNode({
  data,
  selected,
}: NodeProps<PipelineFlowNode>) {
  const node = data;
  const setLabel = useVisualStore((s) => s.setLabel);
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(node.label);
  const def = node.schema;
  const cf = node.connectingFrom;
  const isDragging = cf != null && cf.nodeId !== node.id;
  const hasAnyCompatible =
    cf && cf.nodeId !== node.id && cf.wireType != null
      ? cf.handleType === 'source'
        ? (def?.inputs ?? []).some((p) => portsCompatible(cf.wireType!, p.type))
        : (def?.outputs ?? []).some((p) => portsCompatible(p.type, cf.wireType!))
      : true;
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
  return (
    <div
      className={[
        'bg-card border rounded w-60 text-xs select-none border-l-4',
        errors ? 'border-l-red-500' : CATEGORY_BORDER[def?.category ?? 'advanced'],
        selected ? 'ring-2 ring-indigo-500' : '',
        node.disabled ? 'opacity-50 border-dashed' : '',
        cf && cf.nodeId !== node.id && !hasAnyCompatible ? 'opacity-40' : '',
      ].join(' ')}
      data-node-id={node.id}
      data-testid='pipeline-node'
    >
      <div className='px-2 py-1 border-b font-mono truncate'>{node.component}</div>
      <div className='px-2 py-0.5 text-muted-foreground'>
        {editing ? (
          <input
            autoFocus
            data-testid='node-label-input'
            className='w-full border rounded px-1'
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
          {(def?.inputs ?? []).map((p, i) => (
            <div key={p.prop ?? i}>
              <Handle
                type='target'
                position={Position.Left}
                id={p.prop ?? String(i)}
                className={[
                  '!w-2.5 !h-2.5 !rounded-full border-0',
                  WIRE_COLOR[p.type] ?? 'bg-slate-400',
                  isDragging &&
                  cf?.handleType === 'source' &&
                  cf.wireType != null &&
                  portsCompatible(cf.wireType, p.type)
                    ? 'ring-2 ring-green-400 !w-4 !h-4'
                    : '',
                  isDragging &&
                  cf?.handleType === 'source' &&
                  cf.wireType != null &&
                  !portsCompatible(cf.wireType, p.type)
                    ? 'opacity-30'
                    : '',
                ].join(' ')}
              />
              {p.prop}
            </div>
          ))}
        </div>
        <div className='flex flex-col gap-1 items-end'>
          {(def?.outputs ?? []).map((p, i) => (
            <div key={p.export ?? i}>
              {p.export}
              <Handle
                type='source'
                position={Position.Right}
                id={p.export ?? String(i)}
                className={[
                  '!w-2.5 !h-2.5 !rounded-full border-0',
                  WIRE_COLOR[p.type] ?? 'bg-slate-400',
                  isDragging &&
                  cf?.handleType === 'target' &&
                  cf.wireType != null &&
                  portsCompatible(p.type, cf.wireType)
                    ? 'ring-2 ring-green-400 !w-4 !h-4'
                    : '',
                  isDragging &&
                  cf?.handleType === 'target' &&
                  cf.wireType != null &&
                  !portsCompatible(p.type, cf.wireType)
                    ? 'opacity-30'
                    : '',
                ].join(' ')}
              />
            </div>
          ))}
        </div>
      </div>
      {(errors || warnings) > 0 && (
        <div className='px-2 py-0.5 border-t'>
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
