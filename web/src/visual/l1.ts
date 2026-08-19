import { portHandleId } from './schemaAdapter';
import type { GraphDocument, GraphNode, L1Diagnostic, SchemaPayload } from './types';

/**
 * L1 — the client-side, schema-driven validation layer.
 *
 * Everything here is derived from the served schema payload; there is not a
 * single component name in this file. The rules follow decision D1: the canvas
 * presents LEFT-TO-RIGHT DATAFLOW and every port carries a ROLE.
 *
 *   role "produces" — data LEAVES the node here (drawn on the right)
 *   role "accepts"  — data ENTERS the node here (drawn on the left)
 *
 * A role is independent of whether the port came from the component's Arguments
 * (an `input` in the artifact) or its Exports (an `output`): a RECEIVER export
 * such as `prometheus.remote_write.receiver` accepts, and a receiver-list
 * argument such as `prometheus.scrape.forward_to` produces. Consequently L1
 * never reasons about `inputs` vs `outputs` — it reasons about roles:
 *
 *   - an edge always runs from a "produces" port to an "accepts" port;
 *   - `output_nowhere` fires for a node that produces data nobody consumes;
 *   - `dangling_input` fires for a node that accepts data nobody supplies.
 *
 * The other correctness rule this file exists for is required-ness. A required
 * attribute can be satisfied four ways — a literal prop, a WIRE landing on the
 * port that backs it, a binding, or (for a block attribute) the block instance
 * the user filled in — so the check runs over all four, and it recurses into
 * nested blocks (D2) carrying a `path` the inspector can focus.
 */

/** Ports and blocks gained `role`/`path`/`required` with the D1 port model;
 *  `types.ts` (shared, not owned here) does not declare them yet, so they are
 *  read through local shapes exactly as `renderTS.ts` does. Every field is
 *  optional and every reader has a legacy fallback, so a schema payload without
 *  them still validates the way it did before (D4: degrade, never crash). */
interface AttrLike {
  name: string;
  type?: string;
  required?: boolean;
  /** Set by the extractor on an attribute that is addressable as a port. */
  input_type?: string;
}
interface BlockLike {
  name: string;
  required?: boolean;
  repeatable?: boolean;
  truncated?: boolean;
  attributes?: AttrLike[];
  blocks?: BlockLike[];
}
interface PortLike {
  prop?: string;
  export?: string;
  type?: string;
  cardinality?: string;
  role?: string;
  path?: string[];
}
interface ComponentLike {
  stability?: string;
  category?: string;
  terminal_ok?: boolean;
  attributes?: AttrLike[];
  blocks?: BlockLike[];
  inputs?: PortLike[];
  outputs?: PortLike[];
}

export const ROLE_PRODUCES = 'produces';
export const ROLE_ACCEPTS = 'accepts';
export type PortRole = typeof ROLE_PRODUCES | typeof ROLE_ACCEPTS;

/** `{"$expr": "..."}` marks a props value as a raw Alloy expression rather than
 *  a literal — the only way to supply a secret, and therefore a value for the
 *  purposes of required-ness. Kept identical to the renderer's escape. */
const EXPR_KEY = '$expr';

/** A diagnostic plus the extra context the inspector needs to focus a field.
 *  `L1Diagnostic` (shared) has no room for it, so the surplus is declared here;
 *  the array is still assignable to `L1Diagnostic[]` for existing consumers. */
export interface L1DiagnosticEx extends L1Diagnostic {
  /** The other end of a two-node diagnostic (a wire, a label collision). */
  node_id2?: string;
  /** Prop path the diagnostic is about, with block instance indices:
   *  `["endpoint", "0", "url"]`. Absent for whole-node diagnostics. */
  path?: string[];
  /** Handle id of the port the diagnostic is about, when it is about one. */
  port?: string;
}

export interface ResolvedPort {
  /** React Flow handle id — the id an edge endpoint stores. */
  id: string;
  /** Schema path, e.g. `["forward_to"]` or `["output", "metrics"]`. */
  path: string[];
  type: string;
  role: PortRole;
  cardinality?: string;
  /** Whether the port came from the Arguments struct or the Exports struct.
   *  Only ever needed for message wording — the rules use `role`. */
  origin: 'argument' | 'export';
}

export function sanitizeLabel(label: string): string {
  return label
    .replace(/[^a-z0-9_]/gi, '_')
    .replace(/^[^a-z_]/i, '_$&')
    .toLowerCase();
}

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

/** The `{"$expr": "..."}` escape, or null. */
function rawExpr(value: unknown): string | null {
  if (!isPlainObject(value)) return null;
  const keys = Object.keys(value);
  if (keys.length !== 1 || keys[0] !== EXPR_KEY) return null;
  const expr = value[EXPR_KEY];
  return typeof expr === 'string' && expr.trim() !== '' ? expr.trim() : null;
}

/** Does this props value count as "set"? `false` and `0` do; `""`, `[]`, `{}`
 *  and a blank `$expr` do not. */
function hasValue(value: unknown): boolean {
  if (value === undefined || value === null) return false;
  if (typeof value === 'string') return value.trim() !== '';
  if (Array.isArray(value)) return value.length > 0;
  if (isPlainObject(value)) {
    const keys = Object.keys(value);
    if (keys.length === 1 && keys[0] === EXPR_KEY) return rawExpr(value) !== null;
    return keys.length > 0;
  }
  return true;
}

/** Normalize a props value into block instances (D2): an object is one
 *  instance, an array is many, anything else is not a block body. */
function blockInstances(value: unknown): Record<string, unknown>[] {
  if (value === undefined || value === null) return [];
  if (Array.isArray(value)) return value.filter(isPlainObject);
  if (isPlainObject(value)) return rawExpr(value) === null ? [value] : [];
  return [];
}

/** Every value reachable at a schema path, descending through block instances
 *  (so `["endpoint","url"]` finds the url of each `endpoint` block). */
function valuesAtPath(props: Record<string, unknown>, path: string[]): unknown[] {
  let level: unknown[] = [props];
  for (const segment of path) {
    const next: unknown[] = [];
    for (const container of level)
      for (const candidate of Array.isArray(container) ? container : [container])
        if (isPlainObject(candidate) && Object.hasOwn(candidate, segment))
          next.push(candidate[segment]);
    level = next;
  }
  return level;
}

const portPath = (port: PortLike, fallback: string): string[] =>
  port.path && port.path.length > 0 ? port.path : [fallback];

/**
 * Resolve one component's ports into the role-keyed model the rules use.
 *
 * The role falls back to the pre-D1 convention (an argument accepts, an export
 * produces) when the schema does not carry one, which is exactly right for
 * data-typed ports and keeps older payloads working.
 */
export function resolvePorts(def: ComponentLike | undefined): ResolvedPort[] {
  if (!def) return [];
  const ports: ResolvedPort[] = [];
  (def.inputs ?? []).forEach((port, i) => {
    const id = portHandleId(port, i);
    ports.push({
      id,
      path: portPath(port, id),
      type: port.type ?? '',
      role: port.role === ROLE_PRODUCES ? ROLE_PRODUCES : ROLE_ACCEPTS,
      cardinality: port.cardinality,
      origin: 'argument',
    });
  });
  (def.outputs ?? []).forEach((port, i) => {
    const id = portHandleId(port, i);
    ports.push({
      id,
      path: portPath(port, id),
      type: port.type ?? '',
      role: port.role === ROLE_ACCEPTS ? ROLE_ACCEPTS : ROLE_PRODUCES,
      cardinality: port.cardinality,
      origin: 'export',
    });
  });
  return ports;
}

interface ComponentIndex {
  ports: ResolvedPort[];
  /** Handle id → port, per role. A component may reuse one name for both roles
   *  (`database_observability.*` accepts and re-exports `targets`), so the role
   *  is part of the key — which is also what disambiguates an edge endpoint. */
  produces: Map<string, ResolvedPort>;
  accepts: Map<string, ResolvedPort>;
  /** Dotted schema path → the attribute that declares it. */
  attrByPath: Map<string, AttrLike>;
  /** Dotted schema path → the port that addresses it. */
  portByPath: Map<string, ResolvedPort>;
}

const indexCache = new WeakMap<object, ComponentIndex>();

function indexOf(def: ComponentLike | undefined): ComponentIndex | undefined {
  if (!def) return undefined;
  const cached = indexCache.get(def);
  if (cached) return cached;
  const ports = resolvePorts(def);
  const index: ComponentIndex = {
    ports,
    produces: new Map(ports.filter((p) => p.role === ROLE_PRODUCES).map((p) => [p.id, p])),
    accepts: new Map(ports.filter((p) => p.role === ROLE_ACCEPTS).map((p) => [p.id, p])),
    attrByPath: new Map(),
    portByPath: new Map(ports.map((p) => [p.path.join('.'), p])),
  };
  const walk = (attrs: AttrLike[] | undefined, blocks: BlockLike[] | undefined, at: string[]) => {
    for (const attr of attrs ?? []) index.attrByPath.set([...at, attr.name].join('.'), attr);
    for (const block of blocks ?? []) walk(block.attributes, block.blocks, [...at, block.name]);
  };
  walk(def.attributes, def.blocks, []);
  indexCache.set(def, index);
  return index;
}

/**
 * Wire-type compatibility. The eight wire types are closed and per-signal since
 * the D1 schema phase refined otel ports: `otel.metrics`/`otel.logs`/
 * `otel.traces` are the signal-specific consumer fields, and `otel.any` is the
 * genuinely polymorphic `ConsumerExports.Input`, so it — and only it — is the
 * wildcard, and only within the otel family.
 */
export function portsCompatible(fromType: string, toType: string): boolean {
  if (fromType === toType) return fromType !== '';
  const otel = (t: string) => t.startsWith('otel.');
  return (fromType === 'otel.any' || toType === 'otel.any') && otel(fromType) && otel(toType);
}

/** Whether a wire may run from `from` to `to`: dataflow leaves a produces port
 *  and enters an accepts port, and the wire types must be compatible. Exported
 *  so connect-time validation enforces exactly the rule L1 reports on. */
export function canConnectPorts(from: ResolvedPort, to: ResolvedPort): boolean {
  return (
    from.role === ROLE_PRODUCES && to.role === ROLE_ACCEPTS && portsCompatible(from.type, to.type)
  );
}

interface Wire {
  edgeIndex: number;
  from: { node: string; port: ResolvedPort };
  to: { node: string; port: ResolvedPort };
}

export function validateGraph(
  doc: GraphDocument,
  schema: SchemaPayload,
  opts: { allowExperimental?: boolean } = {},
): L1DiagnosticEx[] {
  const diags: L1DiagnosticEx[] = [];
  const components = schema.components as unknown as Record<string, ComponentLike | undefined>;
  const compDef = (name: string) => components[name];
  const nodeById = new Map(doc.nodes.map((n) => [n.id, n]));
  const active = doc.nodes.filter((n) => !n.disabled);
  const activeIds = new Set(active.map((n) => n.id));
  const push = (d: Omit<L1DiagnosticEx, 'layer'>) => diags.push({ layer: 'L1', ...d });
  const named = (n: GraphNode) => `${n.component} "${n.label}"`;

  // --- Labels. A disabled node is not emitted, so it cannot collide (this is
  // also the renderer's rule; L1 used to disagree with it).
  const labels = new Map<string, string[]>();
  for (const n of active) {
    const key = `${n.component}\u0000${sanitizeLabel(n.label)}`;
    labels.set(key, [...(labels.get(key) ?? []), n.id]);
  }
  for (const ids of labels.values())
    if (ids.length > 1)
      for (const id of ids)
        push({
          severity: 'error',
          code: 'label_collision',
          node_id: id,
          node_id2: ids.find((other) => other !== id),
          message: `Duplicate label on ${nodeById.get(id)?.component ?? id}`,
        });

  // --- Stability gate.
  if (!opts.allowExperimental)
    for (const n of active)
      if (compDef(n.component)?.stability === 'experimental')
        push({
          severity: 'error',
          code: 'experimental_gated',
          node_id: n.id,
          message: `${n.component} is experimental and requires the org toggle to be enabled`,
        });

  // --- Edges: resolve both endpoints by role, then check the wire type.
  const wires: Wire[] = [];
  doc.edges.forEach((edge, edgeIndex) => {
    const fromNode = nodeById.get(edge.from.node);
    const toNode = nodeById.get(edge.to.node);
    if (!fromNode || !toNode) return;
    // A wire to or from a disabled node is dropped with the node itself; that
    // is what "disabled" means, so it is not validated and does not satisfy.
    if (!activeIds.has(fromNode.id) || !activeIds.has(toNode.id)) return;
    const fromIndex = indexOf(compDef(fromNode.component));
    const toIndex = indexOf(compDef(toNode.component));
    // Unknown component: the renderer reports it; L1 cannot say anything about
    // ports it has no schema for, so it says nothing rather than guessing.
    if (!fromIndex || !toIndex) return;
    const fromPort = fromIndex.produces.get(edge.from.port);
    const toPort = toIndex.accepts.get(edge.to.port);
    const endpointProblem = (
      node: GraphNode,
      index: ComponentIndex,
      port: string,
      want: PortRole,
    ) => {
      const other = (want === ROLE_PRODUCES ? index.accepts : index.produces).get(port);
      if (other)
        push({
          severity: 'error',
          code: 'port_role_invalid',
          node_id: node.id,
          node_id2: node.id === fromNode.id ? toNode.id : fromNode.id,
          port,
          message:
            want === ROLE_PRODUCES
              ? `${named(node)} port "${port}" accepts data — a wire must leave a port that produces data`
              : `${named(node)} port "${port}" produces data — a wire must land on a port that accepts data`,
        });
      else
        push({
          severity: 'error',
          code: 'unknown_port',
          node_id: node.id,
          node_id2: node.id === fromNode.id ? toNode.id : fromNode.id,
          port,
          message: `${named(node)} has no port "${port}" in this schema`,
        });
    };
    if (!fromPort) {
      endpointProblem(fromNode, fromIndex, edge.from.port, ROLE_PRODUCES);
      return;
    }
    if (!toPort) {
      endpointProblem(toNode, toIndex, edge.to.port, ROLE_ACCEPTS);
      return;
    }
    if (!portsCompatible(fromPort.type, toPort.type))
      push({
        severity: 'error',
        code: 'type_mismatch',
        node_id: toNode.id,
        node_id2: fromNode.id,
        port: toPort.id,
        message: `Wire type mismatch: ${fromPort.type || '?'} → ${toPort.type || '?'}`,
      });
    // A type-mismatched wire still counts as wired: the user's intent is on the
    // canvas, and reporting "nothing supplies this" on top of the mismatch just
    // buries the one diagnostic that says what to fix.
    wires.push({
      edgeIndex,
      from: { node: fromNode.id, port: fromPort },
      to: { node: toNode.id, port: toPort },
    });
  });

  const producedBy = new Map<string, number>();
  const acceptedBy = new Map<string, number>();
  const key = (nodeId: string, port: ResolvedPort) => `${nodeId}\u0000${port.id}`;
  for (const w of wires) {
    const f = key(w.from.node, w.from.port);
    const t = key(w.to.node, w.to.port);
    producedBy.set(f, (producedBy.get(f) ?? 0) + 1);
    acceptedBy.set(t, (acceptedBy.get(t) ?? 0) + 1);
  }

  // --- Cycles, over the dataflow direction the canvas draws.
  const adjacency = new Map(active.map((n) => [n.id, new Set<string>()]));
  for (const e of doc.edges)
    if (activeIds.has(e.from.node) && activeIds.has(e.to.node))
      adjacency.get(e.from.node)?.add(e.to.node);
  if (
    detectCycle(
      active.map((n) => n.id),
      adjacency,
    )
  )
    push({
      severity: 'error',
      code: 'cycle',
      message: 'Graph contains a cycle — Alloy graphs must be acyclic',
    });

  // --- Per node: required attributes and blocks, secrets, then port roles.
  for (const node of active) {
    const def = compDef(node.component);
    const index = indexOf(def);
    if (!def || !index) continue;
    const props = node.props ?? {};
    /** Ports already covered by an error, so the node-level warnings below do
     *  not repeat them. Keyed by role as well as id: a component may reuse one
     *  name on both sides (`database_observability.*` accepts and re-exports
     *  `targets`) and one end being reported says nothing about the other. */
    const reported = new Set<string>();
    const reportedKey = (port: ResolvedPort) => `${port.role} ${port.id}`;
    const bound = (path: string[]) => {
      const dotted = path.join('.');
      return doc.bindings.some(
        (b) => b.node === node.id && b.prop === dotted && b.ref.expr.trim() !== '',
      );
    };
    const wired = (port: ResolvedPort) =>
      (port.role === ROLE_PRODUCES ? producedBy : acceptedBy).get(key(node.id, port)) ?? 0;
    /** A port is satisfied by a wire; an ARGUMENT port is also satisfied by a
     *  literal value or a binding, because those are the same slot written by
     *  hand. An EXPORT port has no slot — only a wire can satisfy it. */
    const satisfied = (port: ResolvedPort) =>
      wired(port) > 0 ||
      (port.origin === 'argument' &&
        (valuesAtPath(props, port.path).some(hasValue) || bound(port.path)));
    /** Is any port addressing something inside this block wired up? A wired
     *  port emits the block, so the block is not missing. */
    const blockCarriesWire = (at: string[]) =>
      index.ports.some(
        (p) =>
          p.path.length > at.length &&
          at.every((segment, i) => p.path[i] === segment) &&
          wired(p) > 0,
      );

    const walk = (
      attrs: AttrLike[] | undefined,
      blocks: BlockLike[] | undefined,
      scope: Record<string, unknown>,
      /** Path with block instance indices — what the inspector focuses. */
      path: string[],
      /** Path without indices — what the schema is keyed by. */
      schemaPath: string[],
    ) => {
      const inBlock = schemaPath.length > 0 ? ` in block "${schemaPath.join('.')}"` : '';
      for (const attr of attrs ?? []) {
        const here = [...schemaPath, attr.name];
        // A port-backed attribute (`targets`, `forward_to`, `output.metrics`)
        // is owned by the port rules below, which know about wires. This is the
        // false positive that made L1 permanently red on 42 components.
        if (index.portByPath.has(here.join('.'))) continue;
        const value = scope[attr.name];
        if (attr.type === 'secret' && typeof value === 'string' && value.trim() !== '')
          push({
            severity: 'error',
            code: 'secret_by_value',
            node_id: node.id,
            path: [...path, attr.name],
            message: `${attr.name}${inBlock} is secret-typed — use a config node binding, not a literal value`,
          });
        if (!attr.required) continue;
        if (hasValue(value) || bound([...schemaPath, attr.name])) continue;
        push({
          severity: 'error',
          code: 'required_attr_missing',
          node_id: node.id,
          path: [...path, attr.name],
          message: `${named(node)} is missing required attribute "${attr.name}"${inBlock}`,
        });
      }
      for (const block of blocks ?? []) {
        const here = [...schemaPath, block.name];
        const instances = blockInstances(scope[block.name]);
        if (instances.length === 0) {
          if (!block.required) continue;
          if (blockCarriesWire(here)) continue;
          for (const port of index.ports)
            if (port.path.length > here.length && here.every((s, i) => port.path[i] === s))
              reported.add(reportedKey(port));
          push({
            severity: 'error',
            code: 'required_block_missing',
            node_id: node.id,
            path: [...path, block.name],
            message: `${named(node)} is missing required block "${here.join('.')}"`,
          });
          continue;
        }
        instances.forEach((instance, i) =>
          walk(
            block.attributes,
            block.blocks,
            instance,
            block.repeatable ? [...path, block.name, String(i)] : [...path, block.name],
            here,
          ),
        );
      }
    };
    walk(def.attributes, def.blocks, props, [], []);

    // --- Port roles (D1).
    for (const port of index.ports) {
      // Required-ness belongs to the ARGUMENT the port addresses. An export
      // has no attribute of its own, so it is never required — even where a
      // component names an argument and an export alike.
      const attr =
        port.origin === 'argument' ? index.attrByPath.get(port.path.join('.')) : undefined;
      const count = wired(port);
      if (attr?.required && !satisfied(port)) {
        reported.add(reportedKey(port));
        push(
          port.role === ROLE_ACCEPTS
            ? {
                severity: 'error',
                code: 'dangling_input',
                node_id: node.id,
                port: port.id,
                path: port.path,
                message: `${named(node)} needs "${port.path.join('.')}": wire a source into it or set a value`,
              }
            : {
                severity: 'error',
                code: 'output_nowhere',
                node_id: node.id,
                port: port.id,
                path: port.path,
                message: `${named(node)} needs "${port.path.join('.')}": wire it to a destination`,
              },
        );
      }
      if (port.cardinality === 'scalar' && count > 1)
        push({
          severity: 'warning',
          code: 'scalar_input_multi_wire',
          node_id: node.id,
          port: port.id,
          message: `${named(node)} port "${port.id}" takes a single value but has ${count} wires`,
        });
    }
    // A node whose every produces port is idle emits data nobody reads; a node
    // whose every accepts port is idle reads data nobody sends. One diagnostic
    // per node, not per port, so a three-signal otel node wired for metrics
    // alone stays quiet.
    const idle = (role: PortRole) => {
      const ports = index.ports.filter((p) => p.role === role && !reported.has(reportedKey(p)));
      return ports.length > 0 && ports.every((p) => !satisfied(p)) ? ports : null;
    };
    const idleProduces = idle(ROLE_PRODUCES);
    // `terminal_ok` marks a component that is legitimately a dead end
    // (`prometheus.exporter.self`). It never suppresses a required port above.
    if (idleProduces && !(def.terminal_ok ?? false))
      push({
        severity: 'warning',
        code: 'output_nowhere',
        node_id: node.id,
        port: idleProduces[0].id,
        message: `${named(node)} produces data that nothing consumes — wire ${idleProduces
          .map((p) => `"${p.path.join('.')}"`)
          .join(' or ')} onward`,
      });
    const idleAccepts = idle(ROLE_ACCEPTS);
    if (idleAccepts)
      push({
        severity: 'warning',
        code: 'dangling_input',
        node_id: node.id,
        port: idleAccepts[0].id,
        message: `${named(node)} accepts data but nothing supplies it — wire a source into ${idleAccepts
          .map((p) => `"${p.path.join('.')}"`)
          .join(' or ')}`,
      });
  }

  if (
    doc.nodes.length > 0 &&
    !active.some((n) => compDef(n.component)?.category === 'destinations')
  )
    push({
      severity: 'error',
      code: 'no_destination',
      message: 'Pipeline has no Destination nodes — data goes nowhere',
    });
  return diags;
}

function detectCycle(ids: string[], adj: Map<string, Set<string>>): boolean {
  const color = new Map(ids.map((id) => [id, 0]));
  function dfs(u: string): boolean {
    color.set(u, 1);
    for (const v of adj.get(u) ?? []) {
      if (color.get(v) === 1) return true;
      if (color.get(v) === 0 && dfs(v)) return true;
    }
    color.set(u, 2);
    return false;
  }
  return ids.some((id) => color.get(id) === 0 && dfs(id));
}
