/**
 * bindingPath.ts — nested-block binding paths (#426), the TypeScript twin of
 * `internal/visual/bindingpath.go`.
 *
 * `GraphBinding.prop` names where a binding's expression goes. A prop with
 * neither `.` nor `[` is a top-level attribute and keeps the meaning it always
 * had: both renderers write `prop = expr` after the node's body. Anything else
 * is a path into the component's nested blocks:
 *
 *     path    = segment *( "." segment )
 *     segment = identifier [ "[" index "]" ]
 *
 * The last segment is the attribute and carries no index. The ones before it
 * name blocks; a block name may contain dots (`stage.tenant`), so the schema
 * decides which segments group into one name (longest first), and only a block
 * name's last segment may carry the index. The index picks the block instance
 * (0-based); omitted, it is instance 0. The canonical form gives every
 * repeatable block an index and no other block one —
 * `endpoint[0].basic_auth.password`, `client.tls.ca_pem`,
 * `stage.tenant[0].value` — which is exactly the inspector's instance path
 * (`["endpoint", "0", "basic_auth", "password"]`) with each index folded onto
 * its block name.
 *
 * A nested binding renders exactly as `{"$expr": expr}` written at the same
 * place in `props` would; `overlayBindings` is that one rule, shared by
 * `renderTS.ts` and `l1.ts`.
 */
import type { GraphBinding } from './types';

const EXPR_KEY = '$expr';
const IDENT_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
const INDEX_RE = /^(0|[1-9][0-9]*)$/;
/** Mirrors maxBindingIndex in bindingpath.go. */
const MAX_INDEX = 9999;
/** Mirror bindingpath.go's MaxBindingPathSegments / MaxBindingPathBytes
 *  (#438 review): a path is refused before any work is done on it. */
export const MAX_BINDING_PATH_SEGMENTS = 32;
export const MAX_BINDING_PATH_BYTES = 256;

export interface BindingPathToken {
  name: string;
  index: number;
  indexed: boolean;
}
export interface BindingBlockStep {
  name: string;
  index: number;
  repeatable: boolean;
}

interface AttrShape {
  name: string;
  type?: string;
}
interface BlockShape {
  name: string;
  repeatable?: boolean;
  attributes?: AttrShape[];
  blocks?: BlockShape[];
}
export interface ComponentShape {
  attributes?: AttrShape[];
  blocks?: BlockShape[];
}

type Result<T> = { ok: true; value: T } | { ok: false; error: string };
const fail = (error: string): { ok: false; error: string } => ({ ok: false, error });

/** Whether `prop` is a path into a nested block rather than a top-level name. */
export function isNestedBindingProp(prop: string): boolean {
  return prop.includes('.') || prop.includes('[');
}

/** Splits a binding path into its segments, without the schema. */
export function parseBindingPath(prop: string): Result<BindingPathToken[]> {
  if (prop === '') return fail('empty path');
  // UTF-16 length is a lower bound on the UTF-8 bytes Go counts; the encoded
  // length is only taken when the cheap bound does not already refuse.
  const bytes =
    prop.length > MAX_BINDING_PATH_BYTES ? prop.length : new TextEncoder().encode(prop).length;
  if (bytes > MAX_BINDING_PATH_BYTES)
    return fail(`the path is ${bytes} bytes; the limit is ${MAX_BINDING_PATH_BYTES} bytes`);
  let segments = 1;
  for (const ch of prop) if (ch === '.') segments++;
  if (segments > MAX_BINDING_PATH_SEGMENTS)
    return fail(
      `the path has ${segments} segments; the limit is ${MAX_BINDING_PATH_SEGMENTS} segments`,
    );
  const parts = prop.split('.');
  const out: BindingPathToken[] = [];
  for (const [i, part] of parts.entries()) {
    let tok: BindingPathToken = { name: part, index: 0, indexed: false };
    const open = part.indexOf('[');
    if (open >= 0) {
      if (!part.endsWith(']')) return fail(`segment "${part}": unterminated index`);
      const digits = part.slice(open + 1, -1);
      if (!INDEX_RE.test(digits))
        return fail(`segment "${part}": the index must be a non-negative integer`);
      const n = Number(digits);
      if (n > MAX_INDEX) return fail(`segment "${part}": index out of range`);
      tok = { name: part.slice(0, open), index: n, indexed: true };
    }
    if (!IDENT_RE.test(tok.name)) return fail(`segment "${part}" is not an identifier`);
    if (tok.indexed && i === parts.length - 1)
      return fail('the attribute (last segment) cannot carry an index');
    out.push(tok);
  }
  return { ok: true, value: out };
}

/** Resolves `prop` against a component's schema into the blocks it descends
 *  through and the attribute it names. */
export function resolveBindingPath(
  component: ComponentShape,
  prop: string,
  /** Counts the block-name candidates tried: at each level only as many
   *  segments are joined as the longest block name there has parts, so the
   *  work is linear in the path (#438 review). */
  work: { tried: number } = { tried: 0 },
): Result<{ steps: BindingBlockStep[]; attr: AttrShape }> {
  const parsed = parseBindingPath(prop);
  if (!parsed.ok) return parsed;
  const toks = parsed.value;
  let attrs = component.attributes ?? [];
  let blocks = component.blocks ?? [];
  const steps: BindingBlockStep[] = [];
  let i = 0;
  while (i < toks.length - 1) {
    let matched = false;
    const byName = new Map(blocks.map((b) => [b.name, b]));
    const maxParts = Math.max(0, ...blocks.map((b) => b.name.split('.').length));
    // Longest block name first (`stage.tenant` before `stage`), but never
    // longer than the longest name this level declares.
    for (let j = Math.min(toks.length - 2, i + maxParts - 1); j >= i && !matched; j--) {
      work.tried++;
      if (toks.slice(i, j).some((t) => t.indexed)) continue;
      const name = toks
        .slice(i, j + 1)
        .map((t) => t.name)
        .join('.');
      const block = byName.get(name);
      if (!block) continue;
      if (!block.repeatable && toks[j].index > 0)
        return fail(`block "${name}" appears once; index ${toks[j].index} does not exist`);
      steps.push({ name, index: toks[j].index, repeatable: block.repeatable === true });
      attrs = block.attributes ?? [];
      blocks = block.blocks ?? [];
      i = j + 1;
      matched = true;
    }
    if (!matched)
      return fail(
        `no block at "${toks
          .slice(i, -1)
          .map((t) => t.name)
          .join('.')}"`,
      );
  }
  const leaf = toks[toks.length - 1].name;
  const attr = attrs.find((a) => a.name === leaf);
  if (attr) return { ok: true, value: { steps, attr } };
  if (blocks.some((b) => b.name === leaf)) return fail(`"${leaf}" is a block, not an attribute`);
  return fail(`no attribute "${leaf}" there`);
}

/** A resolved path in its canonical form. */
export function formatBindingPath(steps: BindingBlockStep[], attr: string): string {
  return [...steps.map((s) => (s.repeatable ? `${s.name}[${s.index}]` : s.name)), attr].join('.');
}

/** The canonical binding prop for an inspector instance path — a numeric
 *  segment folds onto the block name before it:
 *  `["endpoint", "0", "basic_auth", "password"]` → `endpoint[0].basic_auth.password`. */
export function bindingPropOf(instancePath: string[]): string {
  const out: string[] = [];
  for (const seg of instancePath) {
    if (INDEX_RE.test(seg) && out.length > 0) out[out.length - 1] += `[${seg}]`;
    else out.push(seg);
  }
  return out.join('.');
}

/** Whether a binding's prop addresses the attribute at `instancePath`: its
 *  canonical form, or the same path with every index left out when each index
 *  is 0 (an omitted index means instance 0). */
export function bindingMatchesPath(prop: string, instancePath: string[]): boolean {
  if (prop === bindingPropOf(instancePath)) return true;
  const indices = instancePath.filter((s) => INDEX_RE.test(s));
  return (
    indices.every((s) => s === '0') &&
    prop === instancePath.filter((s) => !INDEX_RE.test(s)).join('.')
  );
}

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

function isRawExpr(v: Record<string, unknown>): boolean {
  const keys = Object.keys(v);
  return (
    keys.length === 1 &&
    keys[0] === EXPR_KEY &&
    typeof v[EXPR_KEY] === 'string' &&
    (v[EXPR_KEY] as string).trim() !== ''
  );
}

/** The index-th block instance a props value holds, and how many it holds —
 *  in constant time (bindingpath.go's instanceAt). `ok` is false when the
 *  value is not block-shaped there. */
function instanceAt(
  value: unknown,
  index: number,
): { inst: Record<string, unknown> | null; count: number; ok: boolean } {
  if (value === null || value === undefined) return { inst: null, count: 0, ok: true };
  if (Array.isArray(value)) {
    if (index >= value.length) return { inst: null, count: value.length, ok: true };
    const el = value[index];
    return isPlainObject(el)
      ? { inst: el, count: value.length, ok: true }
      : { inst: null, count: value.length, ok: false };
  }
  if (isPlainObject(value)) {
    if (isRawExpr(value)) return { inst: null, count: 0, ok: false };
    return { inst: index === 0 ? value : null, count: 1, ok: true };
  }
  return { inst: null, count: 0, ok: false };
}

/** The containers one node's overlay has copied, so each is copied once
 *  however many bindings pass through it (#438 review). */
export type CopyOnWrite = WeakSet<object>;

function ownObject(cow: CopyOnWrite, o: Record<string, unknown> | null): Record<string, unknown> {
  if (o && cow.has(o)) return o;
  const out = { ...(o ?? {}) };
  cow.add(out);
  return out;
}

function ownArray(cow: CopyOnWrite, a: unknown[]): unknown[] {
  if (cow.has(a)) return a;
  const out = [...a];
  cow.add(out);
  return out;
}

/** `props` with `value` at the resolved path, containers on the way copied
 *  once through `cow`, a missing block created only for instance 0. Nothing is
 *  written when the path cannot be placed; the input is never modified. */
export function setAtBindingPath(
  props: Record<string, unknown>,
  steps: BindingBlockStep[],
  attr: string,
  value: unknown,
  cow: CopyOnWrite = new WeakSet(),
): Result<{ props: Record<string, unknown>; prior: boolean }> {
  let scope: Record<string, unknown> | null = props;
  for (const step of steps) {
    const at = instanceAt(scope?.[step.name], step.index);
    if (!at.ok) return fail(`"${step.name}": the props there are not a block`);
    if (at.inst === null && !(at.count === 0 && step.index === 0))
      return fail(`${step.name}[${step.index}]: no such block instance (the node has ${at.count})`);
    scope = at.inst;
  }
  const out = ownObject(cow, props);
  let here = out;
  for (const step of steps) {
    const current = here[step.name];
    let child: Record<string, unknown>;
    if (Array.isArray(current)) {
      const list = ownArray(cow, current);
      child = ownObject(cow, list[step.index] as Record<string, unknown>);
      list[step.index] = child;
      here[step.name] = list;
    } else {
      child = ownObject(cow, isPlainObject(current) ? current : null);
      here[step.name] = child;
    }
    here = child;
  }
  const prior = Object.hasOwn(here, attr);
  here[attr] = value;
  return { ok: true, value: { props: out, prior } };
}

export interface OverlayIssue {
  code: 'empty_binding_expr' | 'binding_path_invalid' | 'binding_overrides_prop';
  severity: 'error' | 'warning';
  message: string;
}

const MAX_FIELD = 64;
const clip = (s: string, clipper?: (s: string) => string) =>
  clipper ? clipper(s) : s.length > MAX_FIELD ? `${s.slice(0, MAX_FIELD)}…` : s;

/**
 * Overlays a node's nested bindings onto a copy of its props as
 * `{"$expr": ...}` values (render.go's applyNestedBindings). Returns the props
 * to render or check, the bindings that stay top-level, and one issue per
 * binding that could not be placed (it is then left out).
 */
export function overlayBindings(
  component: ComponentShape | undefined,
  props: Record<string, unknown>,
  bindings: GraphBinding[],
  clipper?: (s: string) => string,
): { props: Record<string, unknown>; top: GraphBinding[]; issues: OverlayIssue[] } {
  const top: GraphBinding[] = [];
  const issues: OverlayIssue[] = [];
  let out = props;
  const cow: CopyOnWrite = new WeakSet();
  for (const binding of bindings) {
    if (!isNestedBindingProp(binding.prop)) {
      top.push(binding);
      continue;
    }
    const name = clip(binding.prop, clipper);
    const expr = binding.ref.expr.trim();
    if (expr === '') {
      issues.push({
        code: 'empty_binding_expr',
        severity: 'error',
        message: `binding for "${name}" has an empty expression`,
      });
      continue;
    }
    if (!component) {
      issues.push({
        code: 'binding_path_invalid',
        severity: 'error',
        message: `binding path "${name}": the component is not in the schema, so its blocks are unknown`,
      });
      continue;
    }
    const resolved = resolveBindingPath(component, binding.prop);
    const placed = resolved.ok
      ? setAtBindingPath(
          out,
          resolved.value.steps,
          resolved.value.attr.name,
          { [EXPR_KEY]: expr },
          cow,
        )
      : resolved;
    if (!placed.ok) {
      issues.push({
        code: 'binding_path_invalid',
        severity: 'error',
        message: `binding path "${name}": ${clip(placed.error, clipper)}`,
      });
      continue;
    }
    if (placed.value.prior)
      issues.push({
        code: 'binding_overrides_prop',
        severity: 'warning',
        message: `"${name}" is both bound and set as a value; the binding was used`,
      });
    out = placed.value.props;
  }
  return { props: out, top, issues };
}
