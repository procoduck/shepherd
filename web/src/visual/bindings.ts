/**
 * bindings.ts — W5-01: the authored-binding model.
 *
 * A binding has two channels, and both render at the prop's own place, any
 * depth:
 *
 * - `doc.bindings[]` (`GraphBinding`): what the inspector's picker writes now
 *   (`store.ts`'s `setBinding` with the source's `ref`). Its `prop` is a top-
 *   level attribute name or, since #426, a nested binding path —
 *   `endpoint[0].basic_auth.password`, `client.tls.ca_pem`,
 *   `stage.tenant[0].value` (`bindingPath.ts`). The entry records the source
 *   node, so L1 can report it dangling when the source goes.
 * - `{"$expr": expr}` written into `props` at the prop's instance path —
 *   W5-01's original channel, still written by `setBinding` without a `ref`,
 *   and the shape the read-only parse produces for every expression. The
 *   `bindings-secret` corpus fixture's n4 (`endpoint[0].basic_auth.password`)
 *   pins it.
 *
 * Both renderers place a nested `doc.bindings[]` entry exactly where the same
 * `$expr` in props would go (`overlayBindings`), so the two are
 * interchangeable on output; a top-level entry keeps its historic place after
 * the node's body.
 */
import type { ComponentDef, GraphDocument, GraphNode, SchemaPayload } from './types';

export const EXPR_KEY = '$expr';

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** Whether `value` is the `{"$expr": "..."}` escape — the one shape `props`
 *  uses, at any depth, to mean "this is a raw Alloy expression, not a
 *  literal". Mirrors l1.ts's private `rawExpr` predicate (kept separate
 *  rather than imported, so this module — used from presentational UI code —
 *  never pulls in l1.ts's validation surface). */
export function isExprValue(value: unknown): value is { [EXPR_KEY]: string } {
  if (!isPlainObject(value)) return false;
  const keys = Object.keys(value);
  return keys.length === 1 && keys[0] === EXPR_KEY && typeof value[EXPR_KEY] === 'string';
}

/** The raw expression string, or undefined if `value` isn't a `$expr` value. */
export function exprOf(value: unknown): string | undefined {
  return isExprValue(value) ? value[EXPR_KEY] : undefined;
}

// --- Nested props path helpers, shared by store.ts's setBinding/removeBinding. ---
//
// A path segment matching /^\d+$/ addresses an array index (a repeatable
// block instance); every other segment addresses an object key. This is
// exactly `L1DiagnosticEx.path`'s shape (l1.ts's `walk`, e.g.
// `["endpoint", "0", "basic_auth", "password"]`), so a diagnostic's path can
// be handed to these functions unmodified.

/** Sets `value` at `path` inside `props`, creating any intermediate
 *  block-instance containers (objects or arrays) the path implies. Returns a
 *  NEW props object — every container ON the path is shallow-copied; sibling
 *  values keep their reference. A no-op (`path` empty) returns `props`
 *  itself. */
export function setAtPath(
  props: Record<string, unknown>,
  path: string[],
  value: unknown,
): Record<string, unknown> {
  if (path.length === 0) return props;
  const [head, ...rest] = path;
  const next = { ...props };
  if (rest.length === 0) {
    next[head] = value;
    return next;
  }
  const child = next[head];
  if (/^\d+$/.test(rest[0])) {
    const idx = Number(rest[0]);
    const arr = Array.isArray(child) ? [...child] : [];
    while (arr.length <= idx) arr.push({});
    arr[idx] = setAtPath(isPlainObject(arr[idx]) ? arr[idx] : {}, rest.slice(1), value);
    next[head] = arr;
  } else {
    next[head] = setAtPath(isPlainObject(child) ? child : {}, rest, value);
  }
  return next;
}

/** The inverse of `setAtPath`: deletes the value at `path`, returning a NEW
 *  props object. A path that does not resolve (missing key, out-of-range
 *  index, or a container of the wrong shape) is a no-op that still returns a
 *  value equal to `props`. */
export function deleteAtPath(
  props: Record<string, unknown>,
  path: string[],
): Record<string, unknown> {
  if (path.length === 0) return props;
  const [head, ...rest] = path;
  if (!Object.hasOwn(props, head)) return props;
  const next = { ...props };
  if (rest.length === 0) {
    delete next[head];
    return next;
  }
  const child = next[head];
  if (/^\d+$/.test(rest[0])) {
    if (!Array.isArray(child)) return props;
    const idx = Number(rest[0]);
    if (idx < 0 || idx >= child.length) return props;
    const arr = [...child];
    arr[idx] = deleteAtPath(isPlainObject(arr[idx]) ? arr[idx] : {}, rest.slice(1));
    next[head] = arr;
  } else {
    if (!isPlainObject(child)) return props;
    next[head] = deleteAtPath(child, rest);
  }
  return next;
}

// --- Secret-source discovery (W5-01 / W5-02's BindingPicker). ---

/** A node in the graph whose component is a secret/config source — the
 *  overlay's `sim_secret_source` — and so a valid BindingPicker candidate. */
export interface SecretSourceNode {
  id: string;
  label: string;
  component: string;
  mode: string;
}

/** Every enabled node in `doc` whose component declares `sim_secret_source`
 *  in the schema. `[]` when the schema hasn't loaded yet. */
export function secretSourceNodes(
  doc: Pick<GraphDocument, 'nodes'>,
  schema: SchemaPayload | null | undefined,
): SecretSourceNode[] {
  if (!schema) return [];
  const out: SecretSourceNode[] = [];
  for (const n of doc.nodes as GraphNode[]) {
    if (n.disabled) continue;
    const def = schema.components[n.component] as
      | (ComponentDef & { sim_secret_source?: { mode: string } })
      | undefined;
    if (def?.sim_secret_source)
      out.push({
        id: n.id,
        label: n.label,
        component: n.component,
        mode: def.sim_secret_source.mode,
      });
  }
  return out;
}

/**
 * The Alloy export field a secret-source component's value comes through, by
 * component name. Not present in the schema payload — these six components
 * declare no `outputs` (a Config-category node is referenced by a hand-written
 * expression, never wired), so which field to read is fixed knowledge of the
 * six components the overlay marks `sim_secret_source`, matched against the
 * `bindings-secret` corpus fixture's n4
 * (`remote.kubernetes.secret.creds.data["password"]`) and Alloy's own docs.
 * `map: true` means the export is a `map[string]string` a key must index;
 * `map: false` means the export is the whole (string) value.
 */
export const SECRET_EXPORT_FIELD: Record<string, { field: string; map: boolean }> = {
  'local.file': { field: 'content', map: false },
  'remote.http': { field: 'content', map: false },
  'remote.s3': { field: 'content', map: false },
  'remote.vault': { field: 'data', map: true },
  'remote.kubernetes.secret': { field: 'data', map: true },
  'remote.kubernetes.configmap': { field: 'data', map: true },
};

/** Builds the `$expr` string a binding to `source` resolves to — indexed by
 *  `key` when the source's export is map-shaped. Returns null when the
 *  component isn't a known secret source, or a map-shaped source is given a
 *  blank key. */
export function buildBindingExpr(
  source: Pick<SecretSourceNode, 'component' | 'label'>,
  key?: string,
): string | null {
  const spec = SECRET_EXPORT_FIELD[source.component];
  if (!spec) return null;
  const base = `${source.component}.${source.label}.${spec.field}`;
  if (!spec.map) return base;
  const trimmed = (key ?? '').trim();
  return trimmed === '' ? null : `${base}[${JSON.stringify(trimmed)}]`;
}
