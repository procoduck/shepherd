import { overlayBindings } from './bindingPath';
import type { GraphDocument, GraphEdge, GraphNode, SchemaPayload } from './types';

/**
 * Re-sequences a component's block schemas to the author's recorded order.
 * Named blocks come first in that order; anything the author never arranged
 * follows in schema-declaration order, so the result is always a permutation —
 * never adds or drops a block.
 *
 * An empty order returns the schema's own sequence unchanged, which is the path
 * every graph saved before `block_order` existed takes. Mirrors orderBlocks in
 * internal/visual/render.go; the two renderers are drift-checked against the
 * shared corpus and must agree.
 */
function orderBlocks<T extends { name: string }>(blocks: T[], order?: string[]): T[] {
  if (!order || order.length === 0 || blocks.length === 0) return blocks;
  const byName = new Map(blocks.map((b) => [b.name, b]));
  const out: T[] = [];
  const placed = new Set<string>();
  for (const name of order) {
    const b = byName.get(name);
    // An undeclared name is ignored rather than diagnosed: block_order is a
    // rendering hint, and an unknown name already surfaces as an unknown prop.
    if (!b || placed.has(name)) continue;
    out.push(b);
    placed.add(name);
  }
  for (const b of blocks) if (!placed.has(b.name)) out.push(b);
  return out;
}

/**
 * The preview's output bounds (#400), the browser copy of #393's in
 * internal/visual/render.go. Every size is UTF-8 bytes, as Go's len() counts
 * them, never UTF-16 code units.
 *
 * The SIZE limits are looser than the server's — 4× its MaxRenderBytes (1 MiB),
 * MaxDiagnostics (200) and MaxDiagnosticBytes (64 KiB). A preview of a graph
 * the server would refuse for size is still worth showing; what these stop is
 * a graph that would hang the tab. The diagnostic-byte cap was not set by the
 * owner's decision: it is 4 × 64 KiB so it scales with the other two.
 *
 * The NAME limits are the server's own MaxLabelBytes / MaxComponentBytes: the
 * server refuses a longer name, so previewing one would mislead.
 *
 * Below every limit the two renderers agree byte for byte (the shared corpus
 * proves it); a graph between the server's caps and these renders here and is
 * refused there.
 */
export const MAX_RENDER_BYTES = 4 << 20;
export const MAX_LABEL_BYTES = 256;
export const MAX_COMPONENT_BYTES = 256;
export const MAX_DIAGNOSTICS = 800;
export const MAX_DIAGNOSTIC_BYTES = 256 << 10;
/** How many of a cycle's components its diagnostic names (render.go maxCycleNames). */
const MAX_CYCLE_NAMES = 10;
/** How much of one client-supplied string a message quotes (render.go maxMessageField). */
const MAX_MESSAGE_FIELD = 64;
/** A render refused because its text would pass MAX_RENDER_BYTES; content is empty. */
export const CODE_OUTPUT_TOO_LARGE = 'output_too_large';
/** The count of diagnostics past MAX_DIAGNOSTICS / MAX_DIAGNOSTIC_BYTES. */
export const CODE_TOO_MANY_DIAGNOSTICS = 'too_many_diagnostics';

/** The UTF-8 length of `s` — Go's len(s). A lone surrogate counts as U+FFFD
 *  (3 bytes), which is what Go's JSON decoder turns one into. */
function utf8Length(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      const d = s.charCodeAt(i + 1);
      if (d >= 0xdc00 && d <= 0xdfff) {
        n += 4;
        i++;
      } else n += 3;
    } else n += 3;
  }
  return n;
}

/** Shorten a client-supplied string for a diagnostic message: at most `n`
 *  UTF-8 bytes, ending in an ellipsis when cut, never inside a code point
 *  (render.go clipTo). */
function clipTo(s: string, n: number): string {
  if (s.length * 3 <= n || utf8Length(s) <= n) return s;
  const room = n - utf8Length('…');
  let bytes = 0;
  let end = 0;
  for (const ch of s) {
    const size = utf8Length(ch);
    if (bytes + size > room) break;
    bytes += size;
    end += ch.length;
  }
  return `${s.slice(0, end)}…`;
}
const clip = (s: string) => clipTo(s, MAX_MESSAGE_FIELD);

export interface RenderDiagnostic {
  layer: 'L1';
  severity?: 'error' | 'warning';
  code: string;
  node_id?: string;
  node_id2?: string;
  message: string;
}
export interface RenderTSResult {
  content: string;
  nodeMap: Record<string, { startLine: number; endLine: number }>;
  diagnostics: RenderDiagnostic[];
}

/**
 * The schema shapes this renderer reads. They are structurally the payload the
 * server serves (`types.ts`) plus the fields the D1 port model added — `role`
 * and `path` on ports, `repeatable` on blocks — which `types.ts` does not
 * declare yet. Reading them through these local shapes keeps the renderer
 * honest about what it depends on without editing the shared types.
 */
interface AttrLike {
  name: string;
  type?: string;
}
interface PortLike {
  prop?: string;
  export?: string;
  type?: string;
  cardinality?: string;
  role?: string;
  path?: string[];
}
interface BlockLike {
  name: string;
  repeatable?: boolean;
  required?: boolean;
  attributes?: AttrLike[];
  blocks?: BlockLike[];
}
interface ComponentLike {
  category?: string;
  attributes?: AttrLike[];
  blocks?: BlockLike[];
  inputs?: PortLike[];
  outputs?: PortLike[];
}

const ROLE_PRODUCES = 'produces';
const ROLE_ACCEPTS = 'accepts';
/**
 * `{"$expr": "..."}` marks a props value as a raw Alloy expression rather than
 * a literal — the only way to reach a secret nested inside a block, and the
 * shape ParseAlloy uses to round-trip non-literal expressions.
 */
const EXPR_KEY = '$expr';
/** An opaque node's source text, kept for the read-only graph view (ParseAlloy,
 *  render.go's rawKey). Display-only: never emitted, never reported. */
const RAW_KEY = '$raw';

const categoryOrder: Record<string, number> = {
  config: 0,
  sources: 1,
  transform: 2,
  destinations: 3,
  advanced: 4,
};

/** Sanitize a label to an Alloy identifier. The `u` flag makes this operate on
 *  code points, so an astral character costs one `_` here and in Go. */
const sanitize = (label: string) => {
  const value = label
    .replace(/[^a-z0-9_]/giu, '_')
    .replace(/^[^a-zA-Z_]/, '_$&')
    .toLowerCase();
  return value || '_';
};

/** Compare by code point — the same order Go's byte-wise `<` produces. */
function compareBytes(a: string, b: string): number {
  const left = Array.from(a);
  const right = Array.from(b);
  const n = Math.min(left.length, right.length);
  for (let i = 0; i < n; i++) {
    const x = left[i].codePointAt(0) as number;
    const y = right[i].codePointAt(0) as number;
    if (x !== y) return x < y ? -1 : 1;
  }
  return left.length - right.length;
}

/** Render an Alloy string literal, matching internal/visual/render.go's quote. */
function quote(value: string): string {
  let out = '"';
  for (const ch of value) {
    const code = ch.codePointAt(0) as number;
    if (ch === '"') out += '\\"';
    else if (ch === '\\') out += '\\\\';
    else if (ch === '\n') out += '\\n';
    else if (ch === '\r') out += '\\r';
    else if (ch === '\t') out += '\\t';
    else if (code < 0x20 || code === 0x7f) out += `\\u${code.toString(16).padStart(4, '0')}`;
    else out += ch;
  }
  return `${out}"`;
}

/** Plain-decimal number formatting, matching Go's strconv.FormatFloat(f,'f',-1,64). */
function formatNumber(value: number): string {
  if (value === 0) return '0';
  const s = String(value);
  const m = /^(-?)(\d+)(?:\.(\d+))?[eE]([+-]?\d+)$/.exec(s);
  if (!m) return s;
  const [, sign, intPart, fracPart = '', expPart] = m;
  const exp = Number.parseInt(expPart, 10);
  const digits = intPart + fracPart;
  const point = intPart.length + exp;
  let body: string;
  if (point <= 0) body = `0.${'0'.repeat(-point)}${digits}`;
  else if (point >= digits.length) body = digits + '0'.repeat(point - digits.length);
  else body = `${digits.slice(0, point)}.${digits.slice(point)}`;
  return sign + body;
}

const DURATION_RE = /^[+-]?(0|([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$/;
const IDENT_RE = /^[a-zA-Z_][a-zA-Z0-9_]*$/;

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

/** The `{"$expr": "..."}` escape, or null. */
function rawExpr(value: unknown): string | null {
  if (!isPlainObject(value)) return null;
  const keys = Object.keys(value);
  if (keys.length !== 1 || keys[0] !== EXPR_KEY) return null;
  const expr = value[EXPR_KEY];
  if (typeof expr !== 'string' || expr.trim() === '') return null;
  return expr.trim();
}

function jsonKind(value: unknown): string {
  if (value === null || value === undefined) return 'null';
  if (typeof value === 'string') return 'a string';
  if (typeof value === 'boolean') return 'a bool';
  if (typeof value === 'number') return 'a number';
  if (Array.isArray(value)) return 'a list';
  if (typeof value === 'object') return 'an object';
  return 'unsupported';
}

/** Text accumulated piece by piece that stops — `over` — at the first piece
 *  that would take it past `room` UTF-8 bytes; nothing after that is kept.
 *  render.go's boundedBuilder. */
class BoundedBuilder {
  private parts: string[] = [];
  bytes = 0;
  over = false;
  constructor(readonly room: number) {}

  put(s: string) {
    if (this.over) return;
    const n = utf8Length(s);
    if (this.bytes + n > this.room) {
      this.over = true;
      return;
    }
    this.parts.push(s);
    this.bytes += n;
  }

  text(): string {
    return this.parts.join('');
  }
}

/** Serialize by JSON shape, for list elements, map values and untyped
 *  attributes, piece by piece so a bounded builder stops early (render.go
 *  writeNatural). */
function writeNatural(b: BoundedBuilder, value: unknown) {
  if (b.over) return;
  if (value === null || value === undefined) b.put('null');
  else if (typeof value === 'string') b.put(quote(value));
  else if (typeof value === 'boolean') b.put(String(value));
  else if (typeof value === 'number') b.put(formatNumber(value));
  else if (Array.isArray(value)) {
    b.put('[');
    for (let i = 0; i < value.length; i++) {
      if (b.over) return;
      if (i > 0) b.put(', ');
      writeNatural(b, value[i]);
    }
    b.put(']');
  } else if (isPlainObject(value)) {
    const expr = rawExpr(value);
    if (expr !== null) {
      b.put(expr);
      return;
    }
    const keys = Object.keys(value).sort(compareBytes);
    b.put('{');
    for (let i = 0; i < keys.length; i++) {
      if (b.over) return;
      if (i > 0) b.put(', ');
      b.put(IDENT_RE.test(keys[i]) ? keys[i] : quote(keys[i]));
      b.put(' = ');
      writeNatural(b, value[keys[i]]);
    }
    b.put('}');
  } else b.put(quote(String(value)));
}

/** writeNatural's text, or null as soon as it passes `room` bytes — a value is
 *  never built whole only to be refused (render.go serializeNaturalWithin). */
function serializeNaturalWithin(value: unknown, room: number): string | null {
  const b = new BoundedBuilder(room);
  writeNatural(b, value);
  return b.over ? null : b.text();
}

/** The rendered text, with a running line count, that stops writing at the
 *  first write that would take it past `limit` bytes and reports that in
 *  `over` (render.go lineWriter). Kept as chunks and joined once, so neither
 *  the text nor a line count is rebuilt per node. */
class LineWriter {
  private chunks: string[] = [];
  bytes = 0;
  lines = 0;
  over = false;
  constructor(readonly limit: number) {}

  write(s: string) {
    if (this.over) return;
    const n = utf8Length(s);
    if (this.bytes + n > this.limit) {
      this.over = true;
      return;
    }
    this.chunks.push(s);
    this.bytes += n;
    for (let i = s.indexOf('\n'); i !== -1; i = s.indexOf('\n', i + 1)) this.lines++;
  }

  currentLine(): number {
    return this.lines + 1;
  }

  /** A value serialized by JSON shape within the room left; one that cannot
   *  fit marks the writer over and is not emitted. */
  natural(value: unknown): string | null {
    const text = serializeNaturalWithin(value, this.limit - this.bytes);
    if (text === null) this.over = true;
    return text;
  }

  text(): string {
    return this.chunks.join('');
  }
}

const diagSize = (d: RenderDiagnostic) =>
  utf8Length(d.layer) +
  utf8Length(d.severity ?? '') +
  utf8Length(d.code) +
  utf8Length(d.node_id ?? '') +
  utf8Length(d.node_id2 ?? '') +
  utf8Length(d.message);

/** Diagnostics up to MAX_DIAGNOSTICS / MAX_DIAGNOSTIC_BYTES; the rest are
 *  counted into one too_many_diagnostics entry, an error when any dropped one
 *  was, so a refusal is never lost to the cap (render.go diagSink). */
class DiagSink {
  private out: RenderDiagnostic[] = [];
  private bytes = 0;
  private dropped = 0;
  private droppedErr = false;

  add(d: RenderDiagnostic) {
    const size = diagSize(d);
    if (
      this.dropped > 0 ||
      this.out.length >= MAX_DIAGNOSTICS ||
      this.bytes + size > MAX_DIAGNOSTIC_BYTES
    ) {
      this.dropped++;
      this.droppedErr ||= d.severity === 'error';
      return;
    }
    this.out.push(d);
    this.bytes += size;
  }

  list(): RenderDiagnostic[] {
    if (this.dropped === 0) return this.out;
    return [
      ...this.out,
      {
        layer: 'L1',
        severity: this.droppedErr ? 'error' : 'warning',
        code: CODE_TOO_MANY_DIAGNOSTICS,
        message: `${this.dropped} more diagnostics were not reported (at most ${MAX_DIAGNOSTICS}, ${MAX_DIAGNOSTIC_BYTES} bytes, are)`,
      },
    ];
  }
}

/** A min-heap over `compare` — the topological sort's ready set. `compare`
 *  is a strict total order over the active nodes (two with the same component
 *  and label are a collision, refused first), so popping the least yields the
 *  same sequence as the re-sorted queue it replaced, without re-sorting after
 *  every push (render.go readyQueue). */
class MinHeap {
  private items: string[] = [];
  constructor(private compare: (a: string, b: string) => number) {}

  get size(): number {
    return this.items.length;
  }

  push(id: string) {
    const h = this.items;
    h.push(id);
    let i = h.length - 1;
    while (i > 0) {
      const parent = (i - 1) >> 1;
      if (this.compare(h[i], h[parent]) >= 0) break;
      [h[i], h[parent]] = [h[parent], h[i]];
      i = parent;
    }
  }

  pop(): string {
    const h = this.items;
    const top = h[0];
    const last = h.pop() as string;
    if (h.length > 0) {
      h[0] = last;
      let i = 0;
      for (;;) {
        const l = 2 * i + 1;
        const r = l + 1;
        let least = i;
        if (l < h.length && this.compare(h[l], h[least]) < 0) least = l;
        if (r < h.length && this.compare(h[r], h[least]) < 0) least = r;
        if (least === i) break;
        [h[i], h[least]] = [h[least], h[i]];
        i = least;
      }
    }
    return top;
  }
}

interface LevelRef {
  path: string[];
  value: string;
}

class Renderer {
  sink = new DiagSink();

  add(d: RenderDiagnostic) {
    this.sink.add(d);
  }

  diag(code: string, severity: 'error' | 'warning', nodeId: string, message: string) {
    this.add({ layer: 'L1', severity, code, node_id: nodeId, message });
  }

  /**
   * Serialize one value according to its declared schema type. Returns null
   * when nothing should be emitted; the reason is reported as a diagnostic.
   * A list, map or untyped value is serialized within the room `lw` has
   * left, and one that cannot fit marks it over.
   */
  serializeTyped(
    lw: LineWriter,
    nodeId: string,
    name: string,
    value: unknown,
    type: string,
  ): string | null {
    if (value === null || value === undefined) return null;
    const expr = rawExpr(value);
    if (expr !== null) return expr;
    const mismatch = (want: string): null => {
      this.diag(
        'prop_type_mismatch',
        'error',
        nodeId,
        `"${clip(name)}" is declared ${want} in the schema but the value is ${jsonKind(value)}; it was not emitted`,
      );
      return null;
    };
    switch (type) {
      case 'secret':
        this.diag(
          'secret_by_value',
          'error',
          nodeId,
          `"${clip(name)}" is a secret: supply it as an expression (a binding or {"$expr": ...}), never as a literal`,
        );
        return null;
      case 'duration': {
        let s: string;
        if (typeof value === 'string') s = value;
        else if (typeof value === 'number') s = formatNumber(value);
        else return mismatch('a duration');
        if (!DURATION_RE.test(s))
          this.diag(
            'invalid_duration',
            'error',
            nodeId,
            `"${clip(name)}" = "${clip(s)}" is not a duration (expected a unit such as 30s, 5m, 1h)`,
          );
        return quote(s);
      }
      case 'string':
        if (typeof value === 'string') return quote(value);
        if (typeof value === 'boolean') return quote(String(value));
        if (typeof value === 'number') return quote(formatNumber(value));
        return mismatch('a string');
      case 'number':
        if (typeof value === 'number')
          return Number.isFinite(value) ? formatNumber(value) : mismatch('a number');
        if (typeof value === 'string') {
          const parsed = Number(value.trim());
          if (value.trim() === '' || !Number.isFinite(parsed)) return mismatch('a number');
          return formatNumber(parsed);
        }
        return mismatch('a number');
      case 'bool':
        if (typeof value === 'boolean') return String(value);
        if (value === 'true' || value === 'false') return value;
        return mismatch('a bool');
      case 'list':
        if (Array.isArray(value)) return lw.natural(value);
        return mismatch('a list');
      case 'map':
        if (isPlainObject(value)) return lw.natural(value);
        return mismatch('a map');
      case 'capsule':
        // A capsule can only come from another component's export, so a string
        // is an expression, not a literal.
        if (typeof value === 'string') return value.trim() === '' ? null : value.trim();
        return mismatch('an expression');
      default:
        return lw.natural(value);
    }
  }

  /**
   * Write one block body: schema-ordered attributes (with wired references
   * taking the place of the attribute they satisfy), then any remaining
   * top-level references, then nested blocks, then a diagnostic for every prop
   * the schema does not know.
   */
  body(
    lw: LineWriter,
    indent: string,
    nodeId: string,
    attrs: AttrLike[],
    blocks: BlockLike[],
    props: Record<string, unknown>,
    refs: LevelRef[],
  ) {
    const consumed = new Set<string>();
    const here = new Map<string, string>();
    const nested = new Map<string, LevelRef[]>();
    for (const ref of refs) {
      if (ref.path.length === 1) here.set(ref.path[0], ref.value);
      else if (ref.path.length > 1) {
        const head = ref.path[0];
        nested.set(head, [
          ...(nested.get(head) ?? []),
          { path: ref.path.slice(1), value: ref.value },
        ]);
      }
    }
    const conflict = (name: string) =>
      this.diag(
        'prop_wire_conflict',
        'error',
        nodeId,
        `"${name}" is both wired and set as a value; the wire was used`,
      );

    for (const attr of attrs) {
      const wired = here.get(attr.name);
      if (wired !== undefined) {
        if (Object.hasOwn(props, attr.name)) conflict(attr.name);
        lw.write(`${indent}${attr.name} = ${wired}\n`);
        consumed.add(attr.name);
        here.delete(attr.name);
        continue;
      }
      if (!Object.hasOwn(props, attr.name)) continue;
      consumed.add(attr.name);
      const text = this.serializeTyped(lw, nodeId, attr.name, props[attr.name], attr.type ?? '');
      if (text !== null) lw.write(`${indent}${attr.name} = ${text}\n`);
    }
    for (const [name, value] of here) {
      if (Object.hasOwn(props, name)) {
        conflict(name);
        consumed.add(name);
      }
      lw.write(`${indent}${name} = ${value}\n`);
    }

    for (const block of blocks) {
      let instances = blockInstances(props[block.name]);
      if (Object.hasOwn(props, block.name)) {
        consumed.add(block.name);
        if (instances === null) {
          this.diag(
            'prop_type_mismatch',
            'error',
            nodeId,
            `"${block.name}" is a block: expected an object or a list of objects`,
          );
          instances = [];
        }
        if (instances.length > 1 && !block.repeatable)
          this.diag(
            'block_not_repeatable',
            'error',
            nodeId,
            `block "${block.name}" may only appear once`,
          );
      }
      const childRefs = nested.get(block.name) ?? [];
      if ((instances === null || instances.length === 0) && childRefs.length === 0) continue;
      if (instances === null || instances.length === 0) instances = [{}];
      for (const [i, instance] of instances.entries()) {
        lw.write(`${indent}${block.name} {\n`);
        this.body(
          lw,
          `${indent}  `,
          nodeId,
          block.attributes ?? [],
          block.blocks ?? [],
          instance,
          i === 0 ? childRefs : [],
        );
        lw.write(`${indent}}\n`);
      }
    }

    for (const key of Object.keys(props)
      .filter((k) => !consumed.has(k) && k !== RAW_KEY)
      .sort(compareBytes))
      this.diag(
        'unknown_prop',
        'warning',
        nodeId,
        `"${clip(key)}" is not an attribute or block of this component in this schema; it was not emitted`,
      );
  }

  /** Props for a component the schema does not describe: written by JSON shape
   *  so nothing is silently dropped. */
  untypedProps(lw: LineWriter, indent: string, nodeId: string, props: Record<string, unknown>) {
    for (const key of Object.keys(props).sort(compareBytes)) {
      if (key === RAW_KEY || props[key] === null || props[key] === undefined) continue;
      const text = this.serializeTyped(lw, nodeId, key, props[key], '');
      if (text !== null) lw.write(`${indent}${key} = ${text}\n`);
    }
  }
}

/** Normalize a props value into block instances (D2). */
function blockInstances(value: unknown): Record<string, unknown>[] | null {
  if (value === null || value === undefined) return [];
  if (Array.isArray(value)) {
    const out: Record<string, unknown>[] = [];
    for (const el of value) {
      if (!isPlainObject(el)) return null;
      out.push(el);
    }
    return out;
  }
  if (isPlainObject(value)) return rawExpr(value) === null ? [value] : null;
  return null;
}

const portPath = (port: PortLike): string[] =>
  port.path && port.path.length > 0 ? port.path : [port.prop ?? port.export ?? ''];
const inputRole = (port: PortLike) => port.role || ROLE_ACCEPTS;
const outputRole = (port: PortLike) => port.role || ROLE_PRODUCES;
const findInput = (c: ComponentLike | undefined, name: string) =>
  (c?.inputs ?? []).find((p) => (p.prop ?? '') === name);
const findOutput = (c: ComponentLike | undefined, name: string) =>
  (c?.outputs ?? []).find((p) => (p.export ?? '') === name);

interface Wire {
  text: string;
  order: number;
  seq: number;
}

/**
 * Render a graph to Alloy text, exactly as internal/visual/render.go does. Its
 * output is bounded (#400): a label or component name over 256 bytes is
 * refused, a graph whose text would pass MAX_RENDER_BYTES is refused
 * (output_too_large, empty content) as soon as the text does, and the
 * diagnostic list is capped. Every step is n log n in the graph's size.
 */
export function renderTS(doc: GraphDocument, schema: SchemaPayload): RenderTSResult {
  const rd = new Renderer();
  const components = schema.components as unknown as Record<string, ComponentLike | undefined>;
  const labels = new Map<string, string>();
  const seen = new Map<string, string>();
  let refuse = false;
  for (const node of doc.nodes) {
    const componentBytes = utf8Length(node.component);
    if (componentBytes > MAX_COMPONENT_BYTES) {
      refuse = true;
      rd.diag(
        'component_too_long',
        'error',
        node.id,
        `the component name is ${componentBytes} bytes; the limit is ${MAX_COMPONENT_BYTES}`,
      );
      continue;
    }
    // Checked on the raw label: a disabled node's marker comment writes it
    // unsanitized, and every wire writes the sanitized one.
    const labelBytes = utf8Length(node.label);
    if (labelBytes > MAX_LABEL_BYTES) {
      refuse = true;
      rd.diag(
        'label_too_long',
        'error',
        node.id,
        `the label is ${labelBytes} bytes; the limit is ${MAX_LABEL_BYTES}`,
      );
      continue;
    }
    const label = sanitize(node.label);
    labels.set(node.id, label);
    if (node.disabled) continue;
    const key = `${node.component}\0${label}`;
    const old = seen.get(key);
    if (old) {
      refuse = true;
      rd.add({
        layer: 'L1',
        severity: 'error',
        code: 'label_collision',
        node_id: old,
        node_id2: node.id,
        message: `nodes ${clip(old)} and ${clip(node.id)} have the same label`,
      });
      continue;
    }
    seen.set(key, node.id);
  }
  if (refuse) {
    // A collision makes every reference ambiguous, so no content is safe to
    // emit; every colliding pair is reported, not just the first. An
    // over-long name is refused the same way.
    return { content: '', nodeMap: {}, diagnostics: rd.sink.list() };
  }
  const tooLarge = (): RenderTSResult => {
    rd.add({
      layer: 'L1',
      severity: 'error',
      code: CODE_OUTPUT_TOO_LARGE,
      message: `the rendered pipeline would exceed ${MAX_RENDER_BYTES} bytes (4 MiB), the most the preview renders`,
    });
    return { content: '', nodeMap: {}, diagnostics: rd.sink.list() };
  };

  const active = new Map<string, GraphNode>();
  for (const node of doc.nodes) if (!node.disabled) active.set(node.id, node);
  const indegree = new Map<string, number>();
  for (const id of active.keys()) indegree.set(id, 0);
  const adjacency = new Map<string, string[]>();
  for (const edge of doc.edges) {
    if (!active.has(edge.from.node) || !active.has(edge.to.node)) continue;
    const next = adjacency.get(edge.from.node);
    if (next) next.push(edge.to.node);
    else adjacency.set(edge.from.node, [edge.to.node]);
    indegree.set(edge.to.node, (indegree.get(edge.to.node) ?? 0) + 1);
  }
  const compare = (a: string, b: string) => {
    const na = active.get(a) as GraphNode;
    const nb = active.get(b) as GraphNode;
    return (
      (categoryOrder[components[na.component]?.category ?? 'advanced'] ?? 4) -
        (categoryOrder[components[nb.component]?.category ?? 'advanced'] ?? 4) ||
      compareBytes(na.component, nb.component) ||
      compareBytes(labels.get(a) as string, labels.get(b) as string)
    );
  };
  const ready = new MinHeap(compare);
  for (const id of active.keys()) if (indegree.get(id) === 0) ready.push(id);
  const order: string[] = [];
  while (ready.size > 0) {
    const id = ready.pop();
    order.push(id);
    for (const to of adjacency.get(id) ?? []) {
      indegree.set(to, (indegree.get(to) as number) - 1);
      if (indegree.get(to) === 0) ready.push(to);
    }
  }
  if (order.length < active.size) {
    const placed = new Set(order);
    const rest = [...active.keys()].filter((id) => !placed.has(id)).sort(compare);
    // At most MAX_CYCLE_NAMES are named: the message must not grow with the
    // graph.
    const names = rest
      .slice(0, MAX_CYCLE_NAMES)
      .map((id) => `${(active.get(id) as GraphNode).component} ${quote(labels.get(id) as string)}`);
    if (rest.length > MAX_CYCLE_NAMES) names.push(`${rest.length - MAX_CYCLE_NAMES} more`);
    rd.add({
      layer: 'L1',
      severity: 'error',
      code: 'cycle',
      node_id: rest[0],
      message: `graph has a cycle through ${names.join(', ')}; the emission order of those components is arbitrary`,
    });
    order.push(...rest);
  }

  const wires = resolveEdges(rd, doc, active, labels, components);
  if (wires === null) return tooLarge();
  // Each node's bindings in document order, indexed once rather than
  // filtered once per node.
  const bindings = new Map<string, GraphDocument['bindings']>();
  for (const binding of doc.bindings) {
    if (!active.has(binding.node)) continue;
    const list = bindings.get(binding.node);
    if (list) list.push(binding);
    else bindings.set(binding.node, [binding]);
  }

  const version = schema._meta.alloy_version || doc.schema_version;
  const lw = new LineWriter(MAX_RENDER_BYTES);
  // The graph's node count, disabled nodes included — render.go writes the
  // same line (it read "graph revision N" until #308).
  const nodes = doc.nodes.length === 1 ? '1 node' : `${doc.nodes.length} nodes`;
  lw.write(
    `// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); ${nodes}, schema ${version}\n`,
  );
  for (const node of doc.nodes)
    if (node.disabled) lw.write(`// node ${quote(node.label)} disabled\n`);
  const nodeMap: RenderTSResult['nodeMap'] = {};
  for (const id of order) {
    if (lw.over) break;
    const node = active.get(id) as GraphNode;
    const component = components[node.component];
    if (node.component !== '' && component === undefined)
      rd.add({
        layer: 'L1',
        severity: 'error',
        code: 'unknown_component',
        node_id: id,
        message: `component "${clip(node.component)}" is not in the schema for this graph`,
      });
    lw.write('\n');
    const startLine = lw.currentLine();
    lw.write(`${node.component} ${quote(labels.get(id) as string)} {\n`);
    // A binding at a nested path (#426, bindingPath.ts) renders inside its
    // block, as the same {"$expr": ...} in props would; a top-level one keeps
    // its place after the body.
    const overlay = overlayBindings(component, node.props ?? {}, bindings.get(id) ?? [], clip);
    for (const issue of overlay.issues) rd.diag(issue.code, issue.severity, id, issue.message);
    const props = overlay.props;
    if (component)
      rd.body(
        lw,
        '  ',
        id,
        component.attributes ?? [],
        orderBlocks(component.blocks ?? [], node.block_order),
        props,
        nodeRefs(component, wires.get(id)),
      );
    else rd.untypedProps(lw, '  ', id, props);
    for (const binding of overlay.top) {
      const expr = binding.ref.expr.trim();
      if (expr === '') {
        rd.diag(
          'empty_binding_expr',
          'error',
          id,
          `binding for "${clip(binding.prop)}" has an empty expression`,
        );
        continue;
      }
      if (binding.prop === '') {
        rd.diag('empty_binding_prop', 'error', id, 'binding has an empty property name');
        continue;
      }
      lw.write(`  ${binding.prop} = ${expr}\n`);
    }
    lw.write('}\n');
    nodeMap[id] = { startLine, endLine: lw.currentLine() - 1 };
  }
  if (lw.over) return tooLarge();
  return { content: lw.text(), nodeMap, diagnostics: rd.sink.list() };
}

/**
 * The D1 rule. For an edge A --> B (dataflow, source on the left) exactly one
 * endpoint is an argument that holds the reference:
 *
 *  - B's port is an accepts-role argument and A's port is a produces-role
 *    export (a DATA export such as discovery.*.targets) — the reference is
 *    emitted in B: `B { targets = A.targets }`.
 *  - A's port is a produces-role argument (forward_to) and B's port is an
 *    accepts-role export (a RECEIVER export such as remote_write.receiver) —
 *    the reference is emitted in A: `A { forward_to = [B.receiver] }`.
 *
 * Anything else is reported instead of silently dropped.
 *
 * Every resolved wire's text lands in the output, so once their total passes
 * MAX_RENDER_BYTES the render cannot fit: null reports that before the joined
 * lists are built.
 */
function resolveEdges(
  rd: Renderer,
  doc: GraphDocument,
  active: Map<string, GraphNode>,
  labels: Map<string, string>,
  components: Record<string, ComponentLike | undefined>,
): Map<string, Map<string, Wire[]>> | null {
  const out = new Map<string, Map<string, Wire[]>>();
  let total = 0;
  const add = (nodeId: string, port: string, wire: Wire) => {
    let byPort = out.get(nodeId);
    if (!byPort) {
      byPort = new Map<string, Wire[]>();
      out.set(nodeId, byPort);
    }
    const list = byPort.get(port);
    if (list) list.push(wire);
    else byPort.set(port, [wire]);
    total += utf8Length(wire.text);
  };
  const reference = (edge: GraphEdge, end: 'from' | 'to') =>
    `${(active.get(edge[end].node) as GraphNode).component}.${labels.get(edge[end].node)}.${edge[end].port}`;

  for (const [i, edge] of doc.edges.entries()) {
    if (total > MAX_RENDER_BYTES) return null;
    const fromNode = active.get(edge.from.node);
    const toNode = active.get(edge.to.node);
    // Wires to disabled or absent nodes are dropped along with the node
    // itself; that is the documented meaning of "disabled".
    if (!fromNode || !toNode) continue;
    const fromComp = components[fromNode.component];
    const toComp = components[toNode.component];
    const toIn = findInput(toComp, edge.to.port);
    const fromOut = findOutput(fromComp, edge.from.port);
    const fromIn = findInput(fromComp, edge.from.port);
    const toOut = findOutput(toComp, edge.to.port);
    const order = edge.order ?? 0;
    if (
      toIn &&
      inputRole(toIn) === ROLE_ACCEPTS &&
      fromOut &&
      outputRole(fromOut) === ROLE_PRODUCES
    )
      add(edge.to.node, edge.to.port, { text: reference(edge, 'from'), order, seq: i });
    else if (
      fromIn &&
      inputRole(fromIn) === ROLE_PRODUCES &&
      toOut &&
      outputRole(toOut) === ROLE_ACCEPTS
    )
      add(edge.from.node, edge.from.port, { text: reference(edge, 'to'), order, seq: i });
    else
      rd.add({
        layer: 'L1',
        severity: 'error',
        code: 'edge_unresolved',
        node_id: edge.from.node,
        node_id2: edge.to.node,
        message: `edge ${clip(edge.id)}: ${fromNode.component}.${clip(edge.from.port)} → ${toNode.component}.${clip(edge.to.port)} does not connect an argument to a matching export; the reference was not emitted`,
      });
  }
  return total > MAX_RENDER_BYTES ? null : out;
}

/** The schema type of every input port carrying Alloy's `[]discovery.Target` —
 *  17 ports on 17 components in the shipped artifact. The one port type whose
 *  Alloy-side value is ALREADY a list. */
const TARGETS_PORT_TYPE = 'targets';

/** Render the wires landing on one input port as an Alloy value.
 *
 *  Byte-for-byte parity with `refValue` in internal/visual/render.go — this
 *  file's whole contract is that the browser preview matches what the server
 *  renders, and renderTS.test.ts diffs both against the same goldens.
 *
 *  Every input in the shipped artifact has cardinality "list", so a `[a, b]`
 *  literal is the obvious rendering, and for the receiver-shaped port types it
 *  is correct. It is WRONG for "targets": a discovery export is already a
 *  `[]discovery.Target`, and Alloy v1.18.1 refuses the nested list at
 *  evaluation time (`target::ConvertFrom: conversion from '[]discovery.Target'
 *  is not supported`) — which `alloy validate` accepts and only `alloy run`
 *  catches. See docs/proofs/sandbox-sim-e2e.md §1. */
function refValue(input: { type?: string; cardinality?: string }, texts: string[]): string {
  if (input.type === TARGETS_PORT_TYPE) {
    return texts.length === 1 ? texts[0] : `array.concat(${texts.join(', ')})`;
  }
  return input.cardinality === 'list' ? `[${texts.join(', ')}]` : texts[0];
}

/** Turn the wires landing on a node into ordered, path-addressed values,
 *  following the component's declared input order. */
function nodeRefs(component: ComponentLike, wires: Map<string, Wire[]> | undefined): LevelRef[] {
  const refs: LevelRef[] = [];
  for (const input of component.inputs ?? []) {
    const ws = wires?.get(input.prop ?? '');
    if (!ws || ws.length === 0) continue;
    const sorted = [...ws].sort((a, b) => a.order - b.order || a.seq - b.seq);
    const texts = sorted.map((w) => w.text);
    refs.push({ path: portPath(input), value: refValue(input, texts) });
  }
  return refs;
}
