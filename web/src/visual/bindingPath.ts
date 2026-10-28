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
    // Longest block name first: `stage.tenant` before `stage`.
    for (let j = toks.length - 2; j >= i && !matched; j--) {
      if (toks.slice(i, j).some((t) => t.indexed)) continue;
      const name = toks
        .slice(i, j + 1)
        .map((t) => t.name)
        .join('.');
      const block = blocks.find((b) => b.name === name);
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

/** The block-instance view of a props value (render.go's blockInstances). */
function instancesOf(value: unknown): Record<string, unknown>[] | null {
  if (value === null || value === undefined) return [];
  if (Array.isArray(value)) {
    const out: Record<string, unknown>[] = [];
    for (const el of value) {
      if (!isPlainObject(el)) return null;
      out.push(el);
    }
    return out;
  }
  if (isPlainObject(value)) return isRawExpr(value) ? null : [value];
  return null;
}

/** A copy of `props` with `value` at the resolved path — every container on
 *  the way copied, a missing block created only for instance 0. */
export function setAtBindingPath(
  props: Record<string, unknown>,
  steps: BindingBlockStep[],
  attr: string,
  value: unknown,
): Result<{ props: Record<string, unknown>; prior: boolean }> {
  const out: Record<string, unknown> = { ...props };
  if (steps.length === 0) {
    const prior = Object.hasOwn(props, attr);
    out[attr] = value;
    return { ok: true, value: { props: out, prior } };
  }
  const [step, ...rest] = steps;
  const current = props[step.name];
  const instances = instancesOf(current);
  if (instances === null) return fail(`"${step.name}": the props there are not a block`);
  let inst: Record<string, unknown>;
  if (step.index < instances.length) inst = instances[step.index];
  else if (step.index === 0 && instances.length === 0) inst = {};
  else
    return fail(
      `${step.name}[${step.index}]: no such block instance (the node has ${instances.length})`,
    );
  const child = setAtBindingPath(inst, rest, attr, value);
  if (!child.ok) return child;
  if (Array.isArray(current)) {
    const next = [...current];
    next[step.index] = child.value.props;
    out[step.name] = next;
  } else out[step.name] = child.value.props;
  return { ok: true, value: { props: out, prior: child.value.prior } };
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
      ? setAtBindingPath(out, resolved.value.steps, resolved.value.attr.name, {
          [EXPR_KEY]: expr,
        })
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
