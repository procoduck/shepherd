/**
 * Pure value logic for a single attribute's widget (F6 / task item 1): what
 * widget an attribute type gets, how a raw form value is coerced into the
 * correctly-typed JS value `props` must hold (D2 — "a value must be stored
 * with its correct JSON type"), and the inverse formatting for display. Kept
 * free of React so it is unit-testable without a DOM (the project has no
 * jsdom/@testing-library/react dependency yet — adding one is a call for the
 * task that owns package.json, not this one).
 */
import type { AttrLike } from './schemaShapes';

export type Widget =
  | 'bool'
  | 'number'
  | 'duration'
  | 'enum'
  | 'secret'
  | 'capsule'
  | 'list'
  | 'map'
  | 'string';

/** Which widget an attribute renders as. Mirrors the type table in the task:
 *  string / number / bool (checkbox) / duration / list / map / enum (select,
 *  only when the schema actually carries `values`) / secret. */
export function widgetFor(attr: AttrLike): Widget {
  switch (attr.type) {
    case 'secret':
      return 'secret';
    case 'capsule':
      return 'capsule';
    case 'bool':
      return 'bool';
    case 'number':
      return 'number';
    case 'duration':
      return 'duration';
    case 'list':
      return 'list';
    case 'map':
      return 'map';
    case 'string':
      return attr.values && attr.values.length > 0 ? 'enum' : 'string';
    default:
      return 'string';
  }
}

export const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

/** Whether a props value counts as "set" — kept identical to l1.ts's
 *  `hasValue` (minus the `$expr` escape, which secrets/bindings handle
 *  separately here) so the inspector's blank/filled state agrees with what
 *  the validator considers satisfied. */
export function isSet(value: unknown): boolean {
  if (value === undefined || value === null) return false;
  if (typeof value === 'string') return value.trim() !== '';
  if (Array.isArray(value)) return value.length > 0;
  if (isPlainObject(value)) return Object.keys(value).length > 0;
  return true;
}

/**
 * Coerce a raw form value into the JS type the attribute's schema type
 * declares, for scalar (non-list/map) widgets. `raw` is always what an
 * `<input>`/`<select>` hands back: a string, or a boolean for a checkbox.
 * Returns `undefined` to mean "clear this key" — an empty box is not a
 * value (F8's "prefill" default then shows through as a placeholder rather
 * than a stored empty string).
 */
export function coerceScalar(type: string, raw: string | boolean): unknown {
  if (type === 'bool') return typeof raw === 'boolean' ? raw : raw === 'true';
  const text = typeof raw === 'string' ? raw : String(raw);
  if (text === '') return undefined;
  if (type === 'number') {
    const trimmed = text.trim();
    const n = Number(trimmed);
    // An unparsable number is kept as the raw text rather than silently
    // dropped or coerced to 0 — the renderer's own `serializeTyped` reports
    // `prop_type_mismatch` for a non-numeric string, which is the honest
    // diagnostic; hiding the user's keystrokes here would be worse.
    return trimmed !== '' && Number.isFinite(n) ? n : text;
  }
  return text;
}

/** Format a stored value back into what a text/number/duration/enum
 *  `<input>`/`<select>` should show. */
export function formatScalar(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (typeof value === 'number') return String(value);
  if (typeof value === 'string') return value;
  if (typeof value === 'boolean') return String(value);
  return '';
}

/** Human-readable rendering of a schema `default`, for the "not set — this
 *  component defaults to …" hint (F8's prefill, done as an honest hint
 *  rather than a silently-written value — see AttributeField's doc comment
 *  for why). */
export function formatDefault(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (Array.isArray(value)) return `[${value.map((v) => formatDefault(v)).join(', ')}]`;
  if (typeof value === 'string') return value === '' ? '""' : value;
  if (isPlainObject(value))
    return `{${Object.entries(value)
      .map(([k, v]) => `${k}: ${formatDefault(v)}`)
      .join(', ')}}`;
  return String(value);
}

// --- list widget ---

/** Normalizes a props value into the string chips the list widget shows.
 *  Non-string elements (a number in a schema default, say) are rendered as
 *  their string form; the round trip back through `addListItem` re-adds them
 *  as plain strings, which is the same coarsening the rest of the visual
 *  builder already accepts for list-typed attributes. */
export function listValue(value: unknown): string[] {
  return Array.isArray(value) ? value.map((v) => (typeof v === 'string' ? v : String(v))) : [];
}

export function addListItem(value: unknown, item: string): string[] {
  const trimmed = item.trim();
  const list = listValue(value);
  return trimmed === '' ? list : [...list, trimmed];
}

export function removeListItem(value: unknown, index: number): string[] {
  return listValue(value).filter((_, i) => i !== index);
}

export function replaceListItem(value: unknown, index: number, item: string): string[] {
  const list = listValue(value);
  return list.map((v, i) => (i === index ? item : v));
}

// --- map widget ---
//
// The widget displays and edits a *row array* (`MapRow[]`) — order-stable and
// safe to hold a blank/duplicate key mid-edit — and only collapses it to the
// `Record<string,string>` D2 wants in `props` at commit time (`mapFromRows`).
// All the row ops below are pure `MapRow[] -> MapRow[]` so they're testable
// without going anywhere near `props`.

export interface MapRow {
  key: string;
  value: string;
}

/** Normalizes a props value into the key/value rows the map widget shows. */
export function mapValue(value: unknown): MapRow[] {
  if (!isPlainObject(value)) return [];
  return Object.entries(value).map(([key, v]) => ({
    key,
    value: typeof v === 'string' ? v : String(v),
  }));
}

/** Converts the row list into the object `props` stores (D2's map shape).
 *  Blank keys are dropped rather than emitted, since an unnamed map entry can
 *  never round-trip through Alloy; a later row's key wins over an earlier
 *  duplicate, matching plain-object literal semantics. */
export function mapFromRows(rows: MapRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const { key, value } of rows) if (key.trim() !== '') out[key.trim()] = value;
  return out;
}

export function setMapRow(rows: MapRow[], index: number, patch: Partial<MapRow>): MapRow[] {
  return rows.map((row, i) => (i === index ? { ...row, ...patch } : row));
}

export function addMapRow(rows: MapRow[]): MapRow[] {
  return [...rows, { key: '', value: '' }];
}

export function removeMapRow(rows: MapRow[], index: number): MapRow[] {
  return rows.filter((_, i) => i !== index);
}
