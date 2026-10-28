import { useVisualStore } from '../../store';
import type { GraphBinding } from '../../types';
import { useInspectorReadOnly } from './readOnly';
import type { AttrLike } from './schemaShapes';

/**
 * A non-secret attribute that a `doc.bindings[]` entry supplies — at the top
 * level or at a nested path (`client.endpoint`, `stage.tenant[0].value`,
 * #426). The binding wins over any literal at the same place when rendering,
 * so the field shows the bound expression instead of an input that would do
 * nothing, and offers Unbind to get the input back. Secret-typed attributes
 * keep their own picker (`SecretField`).
 */
export function BoundField({
  attr,
  fieldId,
  binding,
  nodeId,
  instancePath,
}: {
  attr: AttrLike;
  fieldId: string;
  binding: GraphBinding;
  nodeId?: string;
  instancePath?: string[];
}) {
  const removeBinding = useVisualStore((s) => s.removeBinding);
  const readOnly = useInspectorReadOnly();
  return (
    <div>
      <label htmlFor={fieldId} className='flex items-baseline gap-1 text-xs text-muted mb-0.5'>
        <span>{attr.name}</span>
        <span className='text-muted-2'>· {attr.type ?? 'string'}</span>
      </label>
      <div
        id={fieldId}
        data-testid={`attr-bound-${attr.name}`}
        className='w-full border rounded px-2 py-1 border-border-strong text-muted flex items-center gap-2'
      >
        <span>
          Bound: <span className='font-mono'>{binding.ref.expr}</span>
        </span>
        {nodeId && instancePath && !readOnly && (
          <button
            type='button'
            data-testid={`attr-binding-unbind-${attr.name}`}
            className='text-[11px] underline text-muted shrink-0'
            onClick={() => removeBinding(nodeId, instancePath)}
          >
            Unbind
          </button>
        )}
      </div>
    </div>
  );
}
