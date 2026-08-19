import type { ReactNode } from 'react';
import type { ResolvedPort } from '../../l1';
import type { GraphBinding } from '../../types';
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
function WiredRow({ attr, wireCount }: { attr: AttrLike; wireCount: number }) {
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
    </div>
  );
}

/** Secrets never store a literal (task item 3): the field is disabled with an
 *  explanation, and shows the binding it is already bound to, when one
 *  exists — there is no picker in the canvas yet to create one, so this is
 *  honest about the current capability rather than pointing at a control
 *  that isn't there (unlike the placeholder text this replaces). A literal
 *  already present in `props` (e.g. from hand-authored graph JSON) is
 *  surfaced with a one-click way to clear it, rather than silently kept. */
function SecretField({
  attr,
  schemaPath,
  value,
  onChange,
  binding,
}: {
  attr: AttrLike;
  schemaPath: string[];
  value: unknown;
  onChange: (next: unknown) => void;
  binding?: GraphBinding;
}) {
  const literal = typeof value === 'string' && value.trim() !== '';
  return (
    <div>
      <FieldLabel attr={attr} htmlFor={fieldId(schemaPath)} />
      {binding ? (
        <div
          id={fieldId(schemaPath)}
          data-testid={`attr-secret-${attr.name}`}
          className='w-full border rounded px-2 py-1 border-border-strong text-muted'
        >
          Bound: <span className='font-mono'>{binding.ref.expr}</span>
        </div>
      ) : (
        <div
          id={fieldId(schemaPath)}
          data-testid={`attr-secret-${attr.name}`}
          className='w-full border rounded px-2 py-1 border-border-strong text-muted italic'
        >
          Secret — needs a config-node binding (no binding picker yet; set it in the text editor)
        </div>
      )}
      {literal && (
        <div className='mt-0.5 flex items-center gap-2'>
          <ErrorText
            name={attr.name}
            message='A literal value is stored here — secrets are never sent as a literal.'
          />
          <button
            type='button'
            data-testid={`attr-secret-clear-${attr.name}`}
            className='text-[11px] underline text-muted shrink-0'
            onClick={() => onChange(undefined)}
          >
            Clear
          </button>
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
  const set = isSet(value);
  return (
    <div>
      <label className='flex items-center gap-2 text-xs' htmlFor={fieldId(schemaPath)}>
        <input
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
  return (
    <div>
      <FieldLabel attr={attr} htmlFor={fieldId(schemaPath)} />
      <select
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
              contentEditable
              suppressContentEditableWarning
              className='outline-none min-w-[1ch]'
              onBlur={(e) => onChange(replaceListItem(items, i, e.currentTarget.textContent ?? ''))}
            >
              {item}
            </span>
            <button
              type='button'
              aria-label={`remove ${item}`}
              className='text-muted-2 hover:text-red-400'
              onClick={() => onChange(removeListItem(items, i))}
            >
              ×
            </button>
          </span>
        ))}
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
              value={row.key}
              onChange={(e) => onChange(mapFromRows(setMapRow(rows, i, { key: e.target.value })))}
            />
            <input
              className='flex-1 min-w-0 bg-panel border border-border rounded px-1 py-0.5 text-xs'
              placeholder='value'
              data-testid={`attr-map-value-${attr.name}-${i}`}
              value={row.value}
              onChange={(e) => onChange(mapFromRows(setMapRow(rows, i, { value: e.target.value })))}
            />
            <button
              type='button'
              aria-label={`remove ${row.key || 'row'}`}
              className='text-muted-2 hover:text-red-400 shrink-0'
              onClick={() => onChange(mapFromRows(removeMapRow(rows, i)))}
            >
              ×
            </button>
          </div>
        ))}
        <button
          type='button'
          data-testid={`attr-map-add-${attr.name}`}
          className='text-[11px] underline text-muted'
          onClick={() => onChange(mapFromRows(addMapRow(rows)))}
        >
          + add entry
        </button>
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
  error,
}: AttributeFieldProps) {
  if (port && wireCount > 0) return <WiredRow attr={attr} wireCount={wireCount} />;

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
