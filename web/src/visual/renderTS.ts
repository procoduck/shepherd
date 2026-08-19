import type { GraphDocument, GraphEdge, GraphNode, SchemaPayload } from './types';

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

/** Serialize by JSON shape, for list elements, map values and untyped attributes. */
function serializeNatural(value: unknown): string {
  if (value === null || value === undefined) return 'null';
  if (typeof value === 'string') return quote(value);
  if (typeof value === 'boolean') return String(value);
  if (typeof value === 'number') return formatNumber(value);
  if (Array.isArray(value)) return `[${value.map(serializeNatural).join(', ')}]`;
  if (isPlainObject(value)) {
    const expr = rawExpr(value);
    if (expr !== null) return expr;
    return `{${Object.keys(value)
      .sort(compareBytes)
      .map((k) => `${IDENT_RE.test(k) ? k : quote(k)} = ${serializeNatural(value[k])}`)
      .join(', ')}}`;
  }
  return quote(String(value));
}

interface LevelRef {
  path: string[];
  value: string;
}

class Renderer {
  diagnostics: RenderDiagnostic[] = [];

  diag(code: string, severity: 'error' | 'warning', nodeId: string, message: string) {
    this.diagnostics.push({ layer: 'L1', severity, code, node_id: nodeId, message });
  }

  /**
   * Serialize one value according to its declared schema type. Returns null
   * when nothing should be emitted; the reason is reported as a diagnostic.
   */
  serializeTyped(nodeId: string, name: string, value: unknown, type: string): string | null {
    if (value === null || value === undefined) return null;
    const expr = rawExpr(value);
    if (expr !== null) return expr;
    const mismatch = (want: string): null => {
      this.diag(
        'prop_type_mismatch',
        'error',
        nodeId,
        `"${name}" is declared ${want} in the schema but the value is ${jsonKind(value)}; it was not emitted`,
      );
      return null;
    };
    switch (type) {
      case 'secret':
        this.diag(
          'secret_by_value',
          'error',
          nodeId,
          `"${name}" is a secret: supply it as an expression (a binding or {"$expr": ...}), never as a literal`,
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
            `"${name}" = "${s}" is not a duration (expected a unit such as 30s, 5m, 1h)`,
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
        if (Array.isArray(value)) return serializeNatural(value);
        return mismatch('a list');
      case 'map':
        if (isPlainObject(value)) return serializeNatural(value);
        return mismatch('a map');
      case 'capsule':
        // A capsule can only come from another component's export, so a string
        // is an expression, not a literal.
        if (typeof value === 'string') return value.trim() === '' ? null : value.trim();
        return mismatch('an expression');
      default:
        return serializeNatural(value);
    }
  }

  /**
   * Emit one block body: schema-ordered attributes (with wired references
   * taking the place of the attribute they satisfy), then any remaining
   * top-level references, then nested blocks, then a diagnostic for every prop
   * the schema does not know.
   */
  body(
    indent: string,
    nodeId: string,
    attrs: AttrLike[],
    blocks: BlockLike[],
    props: Record<string, unknown>,
    refs: LevelRef[],
  ): string {
    let out = '';
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
        out += `${indent}${attr.name} = ${wired}\n`;
        consumed.add(attr.name);
        here.delete(attr.name);
        continue;
      }
      if (!Object.hasOwn(props, attr.name)) continue;
      consumed.add(attr.name);
      const text = this.serializeTyped(nodeId, attr.name, props[attr.name], attr.type ?? '');
      if (text !== null) out += `${indent}${attr.name} = ${text}\n`;
    }
    for (const [name, value] of here) {
      if (Object.hasOwn(props, name)) {
        conflict(name);
        consumed.add(name);
      }
      out += `${indent}${name} = ${value}\n`;
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
        out += `${indent}${block.name} {\n`;
        out += this.body(
          `${indent}  `,
          nodeId,
          block.attributes ?? [],
          block.blocks ?? [],
          instance,
          i === 0 ? childRefs : [],
        );
        out += `${indent}}\n`;
      }
    }

    for (const key of Object.keys(props)
      .filter((k) => !consumed.has(k))
      .sort(compareBytes))
      this.diag(
        'unknown_prop',
        'warning',
        nodeId,
        `"${key}" is not an attribute or block of this component in this schema; it was not emitted`,
      );
    return out;
  }

  /** Props for a component the schema does not describe: emitted by JSON shape
   *  so nothing is silently dropped. */
  untypedProps(indent: string, nodeId: string, props: Record<string, unknown>): string {
    let out = '';
    for (const key of Object.keys(props).sort(compareBytes)) {
      if (props[key] === null || props[key] === undefined) continue;
      const text = this.serializeTyped(nodeId, key, props[key], '');
      if (text !== null) out += `${indent}${key} = ${text}\n`;
    }
    return out;
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

export function renderTS(doc: GraphDocument, schema: SchemaPayload): RenderTSResult {
  const rd = new Renderer();
  const components = schema.components as unknown as Record<string, ComponentLike | undefined>;
  const labels = new Map<string, string>();
  const seen = new Map<string, string>();
  let collision = false;
  for (const node of doc.nodes) {
    const label = sanitize(node.label);
    labels.set(node.id, label);
    if (node.disabled) continue;
    const key = `${node.component}\0${label}`;
    const old = seen.get(key);
    if (old) {
      collision = true;
      rd.diagnostics.push({
        layer: 'L1',
        severity: 'error',
        code: 'label_collision',
        node_id: old,
        node_id2: node.id,
        message: `nodes ${old} and ${node.id} have the same label`,
      });
      continue;
    }
    seen.set(key, node.id);
  }
  if (collision) {
    // A collision makes every reference ambiguous, so no content is safe to
    // emit; every colliding pair is reported, not just the first.
    return { content: '', nodeMap: {}, diagnostics: rd.diagnostics };
  }

  const active = new Map<string, GraphNode>();
  for (const node of doc.nodes) if (!node.disabled) active.set(node.id, node);
  const indegree = new Map<string, number>();
  for (const id of active.keys()) indegree.set(id, 0);
  const adjacency = new Map<string, string[]>();
  for (const edge of doc.edges) {
    if (!active.has(edge.from.node) || !active.has(edge.to.node)) continue;
    adjacency.set(edge.from.node, [...(adjacency.get(edge.from.node) ?? []), edge.to.node]);
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
  const queue = [...active.keys()].filter((id) => indegree.get(id) === 0).sort(compare);
  const order: string[] = [];
  while (queue.length) {
    const id = queue.shift() as string;
    order.push(id);
    for (const to of adjacency.get(id) ?? []) {
      indegree.set(to, (indegree.get(to) as number) - 1);
      if (indegree.get(to) === 0) {
        queue.push(to);
        queue.sort(compare);
      }
    }
  }
  if (order.length < active.size) {
    const placed = new Set(order);
    const rest = [...active.keys()].filter((id) => !placed.has(id)).sort(compare);
    const names = rest.map(
      (id) => `${(active.get(id) as GraphNode).component} ${quote(labels.get(id) as string)}`,
    );
    rd.diagnostics.push({
      layer: 'L1',
      severity: 'error',
      code: 'cycle',
      node_id: rest[0],
      message: `graph has a cycle through ${names.join(', ')}; the emission order of those components is arbitrary`,
    });
    order.push(...rest);
  }

  const wires = resolveEdges(rd, doc, active, labels, components);

  const version = schema._meta.alloy_version || doc.schema_version;
  let content = `// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); graph revision ${doc.nodes.length}, schema ${version}\n`;
  for (const node of doc.nodes)
    if (node.disabled) content += `// node ${quote(node.label)} disabled\n`;
  const nodeMap: RenderTSResult['nodeMap'] = {};
  for (const id of order) {
    const node = active.get(id) as GraphNode;
    const component = components[node.component];
    if (node.component !== '' && component === undefined)
      rd.diagnostics.push({
        layer: 'L1',
        severity: 'error',
        code: 'unknown_component',
        node_id: id,
        message: `component "${node.component}" is not in the schema for this graph`,
      });
    content += `\n${node.component} ${quote(labels.get(id) as string)} {\n`;
    const startLine = content.split('\n').length - 1;
    const props = node.props ?? {};
    content += component
      ? rd.body(
          '  ',
          id,
          component.attributes ?? [],
          component.blocks ?? [],
          props,
          nodeRefs(component, wires.get(id)),
        )
      : rd.untypedProps('  ', id, props);
    for (const binding of doc.bindings.filter((b) => b.node === id)) {
      const expr = binding.ref.expr.trim();
      if (expr === '') {
        rd.diag(
          'empty_binding_expr',
          'error',
          id,
          `binding for "${binding.prop}" has an empty expression`,
        );
        continue;
      }
      if (binding.prop === '') {
        rd.diag('empty_binding_prop', 'error', id, 'binding has an empty property name');
        continue;
      }
      content += `  ${binding.prop} = ${expr}\n`;
    }
    content += '}\n';
    nodeMap[id] = { startLine, endLine: content.split('\n').length - 1 };
  }
  return { content, nodeMap, diagnostics: rd.diagnostics };
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
 */
function resolveEdges(
  rd: Renderer,
  doc: GraphDocument,
  active: Map<string, GraphNode>,
  labels: Map<string, string>,
  components: Record<string, ComponentLike | undefined>,
): Map<string, Map<string, Wire[]>> {
  const out = new Map<string, Map<string, Wire[]>>();
  const add = (nodeId: string, port: string, wire: Wire) => {
    const byPort = out.get(nodeId) ?? new Map<string, Wire[]>();
    byPort.set(port, [...(byPort.get(port) ?? []), wire]);
    out.set(nodeId, byPort);
  };
  const reference = (edge: GraphEdge, end: 'from' | 'to') =>
    `${(active.get(edge[end].node) as GraphNode).component}.${labels.get(edge[end].node)}.${edge[end].port}`;

  doc.edges.forEach((edge, i) => {
    const fromNode = active.get(edge.from.node);
    const toNode = active.get(edge.to.node);
    // Wires to disabled or absent nodes are dropped along with the node
    // itself; that is the documented meaning of "disabled".
    if (!fromNode || !toNode) return;
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
      rd.diagnostics.push({
        layer: 'L1',
        severity: 'error',
        code: 'edge_unresolved',
        node_id: edge.from.node,
        node_id2: edge.to.node,
        message: `edge ${edge.id}: ${fromNode.component}.${edge.from.port} → ${toNode.component}.${edge.to.port} does not connect an argument to a matching export; the reference was not emitted`,
      });
  });
  return out;
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
    refs.push({
      path: portPath(input),
      value: input.cardinality === 'list' ? `[${texts.join(', ')}]` : texts[0],
    });
  }
  return refs;
}
