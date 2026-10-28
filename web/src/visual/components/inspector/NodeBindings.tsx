import { useVisualStore } from '../../store';
import type { GraphBinding } from '../../types';
import { useInspectorReadOnly } from './readOnly';

/**
 * The inspector's Bindings section (design §2.4, #426): every
 * `doc.bindings[]` entry this node owns, by its binding path —
 * `endpoint[1].basic_auth.password`, `stage.tenant[0].value` — with the
 * expression it binds and an Unbind. It lists a binding even where the field
 * itself is not on screen (a block instance the props do not hold yet, which
 * the binding creates when it renders), so no binding is ever invisible.
 */
export function NodeBindings({ nodeId, bindings }: { nodeId: string; bindings: GraphBinding[] }) {
  const unbindProp = useVisualStore((s) => s.unbindProp);
  const readOnly = useInspectorReadOnly();
  const mine = bindings.filter((b) => b.node === nodeId);
  if (mine.length === 0) return null;
  return (
    <div className='space-y-2 pt-1' data-testid='inspector-node-bindings'>
      <h4 className='text-[11px] uppercase tracking-wide text-muted-2'>Bindings</h4>
      <ul className='space-y-1'>
        {mine.map((b) => (
          <li
            key={b.prop}
            data-testid={`node-binding-${b.prop}`}
            className='flex items-start justify-between gap-2 text-xs'
          >
            <span className='min-w-0'>
              <span className='font-mono'>{b.prop}</span>
              <span className='text-muted'> = </span>
              <span className='font-mono text-muted break-all'>{b.ref.expr}</span>
            </span>
            {!readOnly && (
              <button
                type='button'
                className='shrink-0 text-[11px] underline text-muted'
                aria-label={`Unbind ${b.prop}`}
                onClick={() => unbindProp(nodeId, b.prop)}
              >
                Unbind
              </button>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
