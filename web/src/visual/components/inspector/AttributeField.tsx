import type { ReactNode } from 'react';
import { useState } from 'react';
import {
  buildBindingExpr,
  exprOf,
  SECRET_EXPORT_FIELD,
  type SecretSourceNode,
  secretSourceNodes,
} from '../../bindings';
import type { ResolvedPort } from '../../l1';
import { useVisualStore } from '../../store';
import type { GraphBinding, GraphEdge } from '../../types';
import {
  addListItem,
  addMapRow,
  coerceScalar,
  formatDefault,
  formatScalar,
  isSet,
  listValue,
  mapFromRows,
  mapValue,
  removeListItem,
  removeMapRow,
  replaceListItem,
  setMapRow,
  widgetFor,
} from './attributeOps';
import { useInspectorReadOnly } from './readOnly';
import type { AttrLike } from './schemaShapes';

export interface AttributeFieldProps {
  attr: AttrLike;
  /** dotted path, no block-instance indices — what the schema/bindings key on. */
  schemaPath: string[];
  value: unknown;
  onChange: (next: unknown) => void;
  /** The port this attribute also addresses (D1), if any — only argument-origin
   *  ports reach here, since an export can never be an attribute. */
  port?: ResolvedPort;
  wireCount: number;
  binding?: GraphBinding;
  /** This node's id and this attribute's own instance path (a numeric segment
   *  per repeatable-block ancestor — `L1DiagnosticEx.path`'s shape), needed
   *  only by the secret binding picker (W5-01's `setBinding`/`removeBinding`
   *  key on it). Every other widget ignores both. */
  nodeId?: string;
  instancePath?: string[];
  /** This port's own incoming edges, in their current fan-in order (W5-08) —
   *  `WiredRow`'s minimal reorder control. Only meaningful (and only ever
   *  >1) for a `cardinality: list` port; a scalar or cardinality-less one
   *  never renders the control regardless (nothing to reorder). */
  wireEdges?: GraphEdge[];
  onMoveEdge?: (edgeId: string, direction: 'up' | 'down') => void;
  /** Validation message from the node's L1 diagnostics whose path matches this
   *  field exactly (task item 4 — inline feedback tied to diagnostics). */
  error?: string;
}

const fieldId = (path: string[]) => `attr-${path.join('-')}`;

const labelClass = 'flex items-baseline gap-1 text-xs text-muted mb-0.5';
const inputClass =
  'w-full border rounded px-2 py-1 bg-transparent border-border-strong disabled:opacity-60 disabled:cursor-not-allowed';
const errorInputClass = 'border-red-500';
const hintClass = 'mt-0.5 text-[11px] text-muted-2';
const errorClass = 'mt-0.5 text-[11px] text-red-500';

/** One attribute's label row: name, required marker, declared type. Shared by
 *  every widget branch below so the affordance (task item 4) never drifts
 *  between them. */
function FieldLabel({ attr, htmlFor }: { attr: AttrLike; htmlFor: string }) {
  return (
    <label className={labelClass} htmlFor={htmlFor}>
      <span>{attr.name}</span>
      {attr.required && (
        <span className='text-red-500' title='Required' aria-hidden='true'>
          *
        </span>
      )}
      <span className='text-muted-2'>· {attr.type ?? 'string'}</span>
    </label>
  );
}

function Hint({ children }: { children: ReactNode }) {
  return <p className={hintClass}>{children}</p>;
}

function ErrorText({ message, name }: { message?: string; name: string }) {
  if (!message) return null;
  return (
    <p className={errorClass} data-testid={`attr-error-${name}`}>
      {message}
    </p>
  );
}

/**
 * Renders a wired-port field as a read-only status row instead of an editable
 * box (F7's follow-on in the UI): the value is supplied by the canvas wire,
 * and letting the user also type a literal here is exactly the
 * `prop_wire_conflict` / "attribute may only be provided once" trap the
 * review walked into. `renderTS.ts` already prefers the wire over a stray
 * literal, so this is a UX guardrail, not a correctness requirement.
 */
function WiredRow({
  attr,
  wireCount,
  wireEdges,
  onMoveEdge,
}: {
  attr: AttrLike;
  wireCount: number;
  wireEdges?: GraphEdge[];
  onMoveEdge?: (edgeId: string, direction: 'up' | 'down') => void;
}) {
  // The minimal fan-in reorder control (W5-08): one row per incoming wire,
  // in its current order, each with up/down buttons that swap it with the
  // neighboring wire. Only shown once there is more than one wire to order.
  const readOnly = useInspectorReadOnly();
  const showReorder = onMoveEdge && wireEdges && wireEdges.length > 1;
  return (
    <div>
      <FieldLabel attr={attr} htmlFor={fieldId([attr.name])} />
      <div
        id={fieldId([attr.name])}
        data-testid={`attr-wired-${attr.name}`}
        className='w-full border rounded px-2 py-1 border-border-strong text-muted flex items-center gap-1.5'
      >
        <span aria-hidden='true'>↦</span>
        <span>
          Wired on the canvas ({wireCount} connection{wireCount === 1 ? '' : 's'})
        </span>
      </div>
      {showReorder && (
        <div className='mt-1 space-y-0.5' data-testid={`attr-wire-order-${attr.name}`}>
          {wireEdges.map((e, i) => (
            <div key={e.id} className='flex items-center gap-1 text-[11px] text-muted'>
              <span className='w-3 text-right'>{i + 1}.</span>
              <button
                type='button'
                aria-label={`move wire ${i + 1} up`}
                data-testid={`attr-wire-up-${attr.name}-${i}`}
                className='disabled:opacity-30'
                disabled={readOnly || i === 0}
                onClick={() => onMoveEdge(e.id, 'up')}
              >
                ▲
              </button>
              <button
                type='button'
                aria-label={`move wire ${i + 1} down`}
                data-testid={`attr-wire-down-${attr.name}-${i}`}
                className='disabled:opacity-30'
                disabled={readOnly || i === wireEdges.length - 1}
                onClick={() => onMoveEdge(e.id, 'down')}
              >
                ▼
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/** The picker itself (W5-02): existing secret-source nodes in the graph, plus
 *  an option to place a fresh `remote.kubernetes.secret` and bind straight to
 *  it. A map-shaped source (`data`) needs a key ("which entry"); a
 *  scalar-shaped one (`content`) binds as-is. Store-aware — unlike every
 *  other widget here — because it genuinely needs graph-wide knowledge (every
 *  secret-source node, not just this field's own value) and the ability to
 *  place a new node, not merely this field's `onChange`. */
function BindingPicker({
  attr,
  nodeId,
  instancePath,
}: {
  attr: AttrLike;
  nodeId: string;
  instancePath: string[];
}) {
  const doc = useVisualStore((s) => s.doc);
  const schema = useVisualStore((s) => s.schema);
  const addNode = useVisualStore((s) => s.addNode);
  const setBinding = useVisualStore((s) => s.setBinding);
  const getPlacement = useVisualStore((s) => s.getPlacement);
  const [sourceId, setSourceId] = useState('');
  const [key, setKey] = useState('');

  const sources = secretSourceNodes(doc, schema);
  const selected = sources.find((s) => s.id === sourceId);
  const needsKey = selected ? (SECRET_EXPORT_FIELD[selected.component]?.map ?? false) : false;

  const selectSource = (value: string) => {
    if (value === '__add__') {
      const pos = getPlacement ? getPlacement(doc.nodes.length) : { x: 0, y: 0 };
      addNode('remote.kubernetes.secret', pos);
      const created = useVisualStore.getState().doc.nodes.at(-1);
      setSourceId(created?.id ?? '');
    } else {
      setSourceId(value);
    }
    setKey('');
  };

  const bind = () => {
    if (!selected) return;
    const expr = buildBindingExpr(selected as SecretSourceNode, key);
    if (expr) setBinding(nodeId, instancePath, expr);
  };

  return (
    <div className='space-y-1'>
      <select
        data-testid={`attr-binding-source-${attr.name}`}
        className={inputClass}
        value={sourceId}
        onChange={(e) => selectSource(e.target.value)}
      >
        <option value=''>— select a binding source —</option>
        {sources.map((s) => (
          <option key={s.id} value={s.id}>
            {s.label} ({s.component})
          </option>
        ))}
        <option value='__add__'>Add remote.kubernetes.secret…</option>
      </select>
      {needsKey && (
        <input
          data-testid={`attr-binding-key-${attr.name}`}
          className={inputClass}
          placeholder='key, e.g. password'
          value={key}
          onChange={(e) => setKey(e.target.value)}
        />
      )}
      <button
        type='button'
        data-testid={`attr-binding-bind-${attr.name}`}
        className='text-[11px] underline text-muted disabled:opacity-50 disabled:no-underline'
        disabled={!selected || (needsKey && key.trim() === '')}
        onClick={bind}
      >
        Bind
      </button>
    </div>
  );
}

/**
 * Secrets never store a literal (task item 3): the field offers a picker
 * (W5-02) instead of a text box, and shows a unified "Bound: <expr>" display
 * whichever of the two binding channels the value actually came from — a
 * `{"$expr": ...}` written directly into this prop by `setBinding` (W5-01,
 * any depth), or a legacy top-level `doc.bindings[]` entry (see
 * `bindings.ts`'s module doc for why the two channels exist). Only the
 * former can be unbound here: it is the one this module's own
 * `removeBinding` actually manages. A literal already present in `props`
 * (e.g. from hand-authored graph JSON) is surfaced with a one-click way to
 * clear it, rather than silently kept.
 */
function SecretField({
  attr,
  schemaPath,
  value,
  onChange,
  binding,
  nodeId,
  instancePath,
}: {
  attr: AttrLike;
  schemaPath: string[];
  value: unknown;
  onChange: (next: unknown) => void;
  binding?: GraphBinding;
  nodeId?: string;
  instancePath?: string[];
}) {
  const removeBinding = useVisualStore((s) => s.removeBinding);
  const readOnly = useInspectorReadOnly();
  const literal = typeof value === 'string' && value.trim() !== '';
  const propExpr = exprOf(value);
  const boundExpr = propExpr ?? binding?.ref.expr;
  return (
    <div>
      <FieldLabel attr={attr} htmlFor={fieldId(schemaPath)} />
      {boundExpr ? (
        <div
          id={fieldId(schemaPath)}
          data-testid={`attr-secret-${attr.name}`}
          className='w-full border rounded px-2 py-1 border-border-strong text-muted flex items-center gap-2'
        >
          <span>
            Bound: <span className='font-mono'>{boundExpr}</span>
          </span>
          {propExpr && nodeId && instancePath && !readOnly && (
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
      ) : (
        <div id={fieldId(schemaPath)} data-testid={`attr-secret-${attr.name}`}>
          {readOnly ? (
            <div className='w-full border rounded px-2 py-1 border-border-strong text-muted italic'>
              Not bound
            </div>
          ) : nodeId && instancePath ? (
            <BindingPicker attr={attr} nodeId={nodeId} instancePath={instancePath} />
          ) : (
            <div className='w-full border rounded px-2 py-1 border-border-strong text-muted italic'>
              Secret — needs a config-node binding
            </div>
          )}
        </div>
      )}
      {literal && (
        <div className='mt-0.5 flex items-center gap-2'>
          <ErrorText
            name={attr.name}
            message='A literal value is stored here — secrets are never sent as a literal.'
          />
          {!readOnly && (
            <button
              type='button'
              data-testid={`attr-secret-clear-${attr.name}`}
              className='text-[11px] underline text-muted shrink-0'
              onClick={() => onChange(undefined)}
            >
              Clear
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function BoolField({
  attr,
  schemaPath,
  value,
  onChange,
}: {
  attr: AttrLike;
  schemaPath: string[];
  value: unknown;
  onChange: (next: unknown) => void;
}) {
  const readOnly = useInspectorReadOnly();
  const set = isSet(value);
  return (
    <div>
      <label className='flex items-center gap-2 text-xs' htmlFor={fieldId(schemaPath)}>
        <input
          disabled={readOnly}
          id={fieldId(schemaPath)}
          data-testid={`attr-bool-${attr.name}`}
          type='checkbox'
          checked={set ? Boolean(value) : Boolean(attr.default)}
          onChange={(e) => onChange(coerceScalar('bool', e.target.checked))}
        />
        <span>{attr.name}</span>
        {attr.required && (
          <span className='text-red-500' aria-hidden='true'>
            *
          </span>
        )}
      </label>
      {!set && attr.default !== undefined && (
        <Hint>not set — component default is {formatDefault(attr.default)}</Hint>
      )}
    </div>
  );
}

function EnumField({
  attr,
  schemaPath,
  value,
  onChange,
  hasError,
}: {
  attr: AttrLike;
  schemaPath: string[];
  value: unknown;
  onChange: (next: unknown) => void;
  hasError: boolean;
}) {
  const readOnly = useInspectorReadOnly();
  return (
    <div>
      <FieldLabel attr={attr} htmlFor={fieldId(schemaPath)} />
      <select
        disabled={readOnly}
        id={fieldId(schemaPath)}
        data-testid={`attr-select-${attr.name}`}
        className={`${inputClass} ${hasError ? errorInputClass : ''}`}
        value={formatScalar(value)}
        onChange={(e) => onChange(coerceScalar('string', e.target.value))}
      >
        <option value=''>
          {attr.default !== undefined ? `— (default: ${formatDefault(attr.default)})` : '—'}
        </option>
        {(attr.values ?? []).map((v) => (
          <option key={v} value={v}>
            {v}
          </option>
        ))}
      </select>
    </div>
  );
}

function TextLikeField({
  attr,
  schemaPath,
  value,
  onChange,
  hasError,
  kind,
}: {
  attr: AttrLike;
  schemaPath: string[];
  value: unknown;
  onChange: (next: unknown) => void;
  hasError: boolean;
  kind: 'string' | 'number' | 'duration' | 'capsule';
}) {
  const readOnly = useInspectorReadOnly();
  const placeholder =
    attr.default !== undefined
      ? formatDefault(attr.default)
      : kind === 'duration'
        ? 'e.g. 30s, 5m, 1h'
        : kind === 'capsule'
          ? 'component reference expression, e.g. otelcol.auth.basic.default.handler'
          : undefined;
  return (
    <div>
      <FieldLabel attr={attr} htmlFor={fieldId(schemaPath)} />
      <input
        id={fieldId(schemaPath)}
        data-testid={`attr-input-${attr.name}`}
        type={kind === 'number' ? 'number' : 'text'}
        readOnly={readOnly}
        className={`${inputClass} ${hasError ? errorInputClass : ''} ${kind === 'capsule' ? 'font-mono' : ''}`}
        value={formatScalar(value)}
        placeholder={placeholder}
        onChange={(e) =>
          onChange(coerceScalar(kind === 'capsule' ? 'string' : kind, e.target.value))
        }
      />
      {kind === 'capsule' && (
        <Hint>Emitted unquoted as an Alloy expression, not a string literal.</Hint>
      )}
      {kind !== 'capsule' && attr.default !== undefined && !isSet(value) && (
        <Hint>not set — component default is {formatDefault(attr.default)}</Hint>
      )}
    </div>
  );
}

function ListField({
  attr,
  schemaPath,
  value,
  onChange,
  hasError,
}: {
  attr: AttrLike;
  schemaPath: string[];
  value: unknown;
  onChange: (next: unknown) => void;
  hasError: boolean;
}) {
  const readOnly = useInspectorReadOnly();
  const items = listValue(value);
  return (
    <div>
      <FieldLabel attr={attr} htmlFor={fieldId(schemaPath)} />
      <div
        id={fieldId(schemaPath)}
        data-testid={`attr-list-${attr.name}`}
        className={`flex flex-wrap gap-1 border rounded px-1.5 py-1 border-border-strong ${hasError ? errorInputClass : ''}`}
      >
        {items.map((item, i) => (
          <span
            key={`${i}-${item}`}
            className='inline-flex items-center gap-1 bg-panel border border-border rounded px-1.5 text-xs'
          >
            <span
              contentEditable={!readOnly}
              suppressContentEditableWarning
              className='outline-none min-w-[1ch]'
              onBlur={(e) => onChange(replaceListItem(items, i, e.currentTarget.textContent ?? ''))}
            >
              {item}
            </span>
            {!readOnly && (
              <button
                type='button'
                aria-label={`remove ${item}`}
                className='text-muted-2 hover:text-red-400'
                onClick={() => onChange(removeListItem(items, i))}
              >
                ×
              </button>
            )}
          </span>
        ))}
        {!readOnly && (
          <input
            data-testid={`attr-list-add-${attr.name}`}
            className='flex-1 min-w-[6ch] bg-transparent text-xs outline-none'
            placeholder={items.length === 0 ? '+ add item, Enter' : '+'}
            onKeyDown={(e) => {
              if (e.key !== 'Enter') return;
              e.preventDefault();
              const input = e.currentTarget;
              onChange(addListItem(items, input.value));
              input.value = '';
            }}
          />
        )}
        {readOnly && items.length === 0 && <span className='text-xs text-muted-2'>—</span>}
      </div>
      {attr.default !== undefined && items.length === 0 && (
        <Hint>not set — component default is {formatDefault(attr.default)}</Hint>
      )}
    </div>
  );
}

function MapField({
  attr,
  schemaPath,
  value,
  onChange,
  hasError,
}: {
  attr: AttrLike;
  schemaPath: string[];
  value: unknown;
  onChange: (next: unknown) => void;
  hasError: boolean;
}) {
  const readOnly = useInspectorReadOnly();
  const rows = mapValue(value);
  return (
    <div>
      <FieldLabel attr={attr} htmlFor={fieldId(schemaPath)} />
      <div
        id={fieldId(schemaPath)}
        data-testid={`attr-map-${attr.name}`}
        className={`space-y-1 border rounded p-1.5 border-border-strong ${hasError ? errorInputClass : ''}`}
      >
        {rows.map((row, i) => (
          <div key={`${i}-${row.key}`} className='flex items-center gap-1'>
            <input
              className='w-1/3 min-w-0 bg-panel border border-border rounded px-1 py-0.5 text-xs'
              placeholder='key'
              data-testid={`attr-map-key-${attr.name}-${i}`}
              readOnly={readOnly}
              value={row.key}
              onChange={(e) => onChange(mapFromRows(setMapRow(rows, i, { key: e.target.value })))}
            />
            <input
              className='flex-1 min-w-0 bg-panel border border-border rounded px-1 py-0.5 text-xs'
              placeholder='value'
              data-testid={`attr-map-value-${attr.name}-${i}`}
              readOnly={readOnly}
              value={row.value}
              onChange={(e) => onChange(mapFromRows(setMapRow(rows, i, { value: e.target.value })))}
            />
            {!readOnly && (
              <button
                type='button'
                aria-label={`remove ${row.key || 'row'}`}
                className='text-muted-2 hover:text-red-400 shrink-0'
                onClick={() => onChange(mapFromRows(removeMapRow(rows, i)))}
              >
                ×
              </button>
            )}
          </div>
        ))}
        {readOnly ? (
          rows.length === 0 && <span className='text-xs text-muted-2'>—</span>
        ) : (
          <button
            type='button'
            data-testid={`attr-map-add-${attr.name}`}
            className='text-[11px] underline text-muted'
            onClick={() => onChange(mapFromRows(addMapRow(rows)))}
          >
            + add entry
          </button>
        )}
      </div>
      {attr.default !== undefined && rows.length === 0 && (
        <Hint>not set — component default is {formatDefault(attr.default)}</Hint>
      )}
    </div>
  );
}

/**
 * One attribute's widget, dispatched by schema type (task item 1). A value is
 * always written back through `onChange` with its correct JSON type — the
 * root cause of F6 was every widget here being a `<input type="text">`
 * writing `e.target.value`, a string, regardless of the schema's declared
 * type.
 */
export function AttributeField({
  attr,
  schemaPath,
  value,
  onChange,
  port,
  wireCount,
  binding,
  nodeId,
  instancePath,
  wireEdges,
  onMoveEdge,
  error,
}: AttributeFieldProps) {
  if (port && wireCount > 0)
    return (
      <WiredRow attr={attr} wireCount={wireCount} wireEdges={wireEdges} onMoveEdge={onMoveEdge} />
    );

  const widget = widgetFor(attr);
  const hasError = Boolean(error);

  if (widget === 'secret')
    return (
      <SecretField
        attr={attr}
        schemaPath={schemaPath}
        value={value}
        onChange={onChange}
        binding={binding}
        nodeId={nodeId}
        instancePath={instancePath}
      />
    );

  let body: ReactNode;
  if (widget === 'bool') {
    body = <BoolField attr={attr} schemaPath={schemaPath} value={value} onChange={onChange} />;
  } else if (widget === 'enum') {
    body = (
      <EnumField
        attr={attr}
        schemaPath={schemaPath}
        value={value}
        onChange={onChange}
        hasError={hasError}
      />
    );
  } else if (widget === 'list') {
    body = (
      <ListField
        attr={attr}
        schemaPath={schemaPath}
        value={value}
        onChange={onChange}
        hasError={hasError}
      />
    );
  } else if (widget === 'map') {
    body = (
      <MapField
        attr={attr}
        schemaPath={schemaPath}
        value={value}
        onChange={onChange}
        hasError={hasError}
      />
    );
  } else {
    body = (
      <>
        <TextLikeField
          attr={attr}
          schemaPath={schemaPath}
          value={value}
          onChange={onChange}
          hasError={hasError}
          kind={
            widget === 'number' || widget === 'duration' || widget === 'capsule' ? widget : 'string'
          }
        />
        {port && wireCount === 0 && <Hint>can also be wired on the canvas ({port.type})</Hint>}
      </>
    );
  }

  return (
    <div>
      {body}
      <ErrorText name={attr.name} message={error} />
    </div>
  );
}
