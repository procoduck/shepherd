import type { ReactNode } from 'react';
import type { L1DiagnosticEx, ResolvedPort } from '../../l1';
import type { GraphBinding } from '../../types';
import { AttributeField } from './AttributeField';
import {
  appendItem,
  type BlockInstance,
  blockInstances,
  commitInstances,
  removeAt,
  replaceAt,
  withAttr,
} from './blockOps';
import type { AttrLike, BlockLike } from './schemaShapes';

export interface BlockGroupProps {
  block: BlockLike;
  value: unknown;
  onChange: (next: unknown) => void;
  /** Dotted path, no block-instance indices — already ending in this block's
   *  own name. What the schema (`portByPath`) and bindings key on. */
  schemaPath: string[];
  /** Same, but with a `String(index)` segment for each repeatable ancestor —
   *  already ending in this block's own name. Matches `L1DiagnosticEx.path`
   *  exactly (see l1.ts's `walk`), so this is what diagnostic lookup keys on. */
  instancePath: string[];
  nodeId: string;
  diagnostics: L1DiagnosticEx[];
  bindings: GraphBinding[];
  portByPath: Map<string, ResolvedPort>;
  wireCounts: Map<string, number>;
  depth: number;
}

const diagAt = (diags: L1DiagnosticEx[], path: string[]): string | undefined =>
  diags.find((d) => d.path && d.path.join('.') === path.join('.'))?.message;

const bindingAt = (bindings: GraphBinding[], nodeId: string, schemaPath: string[]) =>
  bindings.find((b) => b.node === nodeId && b.prop === schemaPath.join('.'));

const groupBorder = 'border border-border-strong rounded';
const headerClass = 'flex items-center justify-between gap-2 px-2 py-1 bg-panel text-xs';
const bodyClass = 'p-2 space-y-3';

/**
 * One instance's fields: the block's own attributes (each an `AttributeField`,
 * task item 1) followed by its nested blocks (each a recursive `BlockGroup`,
 * task item 2 — this is what gives the inspector arbitrary nesting depth, not
 * just the two levels the task asks for a minimum of).
 */
function InstanceFields({
  attrs,
  blocks,
  instance,
  update,
  schemaPath,
  instancePath,
  nodeId,
  diagnostics,
  bindings,
  portByPath,
  wireCounts,
  depth,
}: {
  attrs: AttrLike[];
  blocks: BlockLike[];
  instance: BlockInstance;
  update: (updater: (inst: BlockInstance) => BlockInstance) => void;
  schemaPath: string[];
  instancePath: string[];
  nodeId: string;
  diagnostics: L1DiagnosticEx[];
  bindings: GraphBinding[];
  portByPath: Map<string, ResolvedPort>;
  wireCounts: Map<string, number>;
  depth: number;
}) {
  return (
    <div className={bodyClass}>
      {attrs.map((attr) => {
        const attrSchemaPath = [...schemaPath, attr.name];
        const port = portByPath.get(attrSchemaPath.join('.'));
        return (
          <AttributeField
            key={attr.name}
            attr={attr}
            schemaPath={attrSchemaPath}
            value={instance[attr.name]}
            onChange={(v) => update((inst) => withAttr(inst, attr.name, v))}
            port={port}
            wireCount={port ? (wireCounts.get(port.id) ?? 0) : 0}
            binding={bindingAt(bindings, nodeId, attrSchemaPath)}
            error={diagAt(diagnostics, [...instancePath, attr.name])}
          />
        );
      })}
      {blocks.map((block) => (
        <BlockGroup
          key={block.name}
          block={block}
          value={instance[block.name]}
          onChange={(v) => update((inst) => withAttr(inst, block.name, v))}
          schemaPath={[...schemaPath, block.name]}
          instancePath={[...instancePath, block.name]}
          nodeId={nodeId}
          diagnostics={diagnostics}
          bindings={bindings}
          portByPath={portByPath}
          wireCounts={wireCounts}
          depth={depth + 1}
        />
      ))}
    </div>
  );
}

export function BlockGroup({
  block,
  value,
  onChange,
  schemaPath,
  instancePath,
  nodeId,
  diagnostics,
  bindings,
  portByPath,
  wireCounts,
  depth,
}: BlockGroupProps) {
  const instances = blockInstances(value);
  const attrs = block.attributes ?? [];
  const blocks = block.blocks ?? [];
  const repeatable = Boolean(block.repeatable);
  const missing = diagAt(diagnostics, instancePath); // `required_block_missing`

  const header = (extra?: ReactNode) => (
    <div className={headerClass} data-testid={`block-header-${schemaPath.join('.')}`}>
      <span className='font-mono flex items-baseline gap-1'>
        <span>{block.name}</span>
        {block.required && (
          <span className='text-red-500' title='Required' aria-hidden='true'>
            *
          </span>
        )}
        <span className='text-muted-2'>{repeatable ? 'block, repeatable' : 'block'}</span>
      </span>
      {extra}
    </div>
  );

  // --- Repeatable: N instance panels, always an Add button. Nothing is
  // materialized eagerly — a repeatable block with zero instances just shows
  // the empty state, since (unlike a single required block) there's no one
  // slot to pre-open.
  if (repeatable) {
    const add = () => onChange(commitInstances(appendItem(instances, {}), true));
    return (
      <div className={groupBorder} style={{ marginLeft: depth > 0 ? 4 : 0 }}>
        {header()}
        <div className='p-2 space-y-2'>
          {instances.length === 0 && (
            <p className={`text-[11px] ${missing ? 'text-red-500' : 'text-muted-2'}`}>
              {missing ?? 'none configured'}
            </p>
          )}
          {instances.map((instance, i) => (
            <div key={`${schemaPath.join('.')}-${i}`} className={groupBorder}>
              <div className={headerClass}>
                <span className='text-muted-2'>#{i + 1}</span>
                <button
                  type='button'
                  data-testid={`block-remove-${schemaPath.join('.')}-${i}`}
                  className='text-muted-2 hover:text-red-400'
                  onClick={() => onChange(commitInstances(removeAt(instances, i), true))}
                >
                  Remove
                </button>
              </div>
              <InstanceFields
                attrs={attrs}
                blocks={blocks}
                instance={instance}
                update={(updater) =>
                  onChange(commitInstances(replaceAt(instances, i, updater(instance)), true))
                }
                schemaPath={schemaPath}
                instancePath={[...instancePath, String(i)]}
                nodeId={nodeId}
                diagnostics={diagnostics}
                bindings={bindings}
                portByPath={portByPath}
                wireCounts={wireCounts}
                depth={depth}
              />
            </div>
          ))}
          <button
            type='button'
            data-testid={`block-add-${schemaPath.join('.')}`}
            className='text-[11px] underline text-muted'
            onClick={add}
          >
            + add {block.name}
          </button>
        </div>
      </div>
    );
  }

  // --- Non-repeatable, present: show its fields with a Remove affordance
  // (required blocks show one too — Remove just clears back to the implicit
  // empty instance a required-but-absent block already renders below).
  if (instances.length > 0 || block.required) {
    const instance = instances[0] ?? {};
    return (
      <div className={groupBorder} style={{ marginLeft: depth > 0 ? 4 : 0 }}>
        {header(
          !block.required && (
            <button
              type='button'
              data-testid={`block-remove-${schemaPath.join('.')}`}
              className='text-muted-2 hover:text-red-400'
              onClick={() => onChange(commitInstances([], false))}
            >
              Remove
            </button>
          ),
        )}
        {missing && <p className='px-2 pt-1 text-[11px] text-red-500'>{missing}</p>}
        <InstanceFields
          attrs={attrs}
          blocks={blocks}
          instance={instance}
          update={(updater) => onChange(commitInstances([updater(instance)], false))}
          schemaPath={schemaPath}
          instancePath={instancePath}
          nodeId={nodeId}
          diagnostics={diagnostics}
          bindings={bindings}
          portByPath={portByPath}
          wireCounts={wireCounts}
          depth={depth}
        />
      </div>
    );
  }

  // --- Non-repeatable, optional, absent: a collapsed add affordance instead
  // of an always-open sub-form for a block most components never touch.
  return (
    <div className={groupBorder} style={{ marginLeft: depth > 0 ? 4 : 0 }}>
      {header(
        <button
          type='button'
          data-testid={`block-add-${schemaPath.join('.')}`}
          className='text-[11px] underline text-muted'
          onClick={() => onChange(commitInstances([{}], false))}
        >
          + configure
        </button>,
      )}
    </div>
  );
}
