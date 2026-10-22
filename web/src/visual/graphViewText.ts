import { isOpaqueNode } from './graphViewFlow';
import { isModuleGroup } from './graphViewLayout';
import type { GraphNode } from './types';

/**
 * The read-only graph view's words for what the server's parse reports
 * (2026-10-14 walkthrough L1, L2, L4). Display only: the server's warning
 * strings (internal/visual/parse.go) are unchanged and stay one click away.
 */

const WARNING_PREFIX = 'this pipeline could not be fully mapped to a graph: ';
const MODULE_LINE =
  /^module "(.+)" is shown as a group of its components \(the builder cannot edit modules\)$/;
const NOT_COMPONENT_LINE = /^"(.+)" is not a component and cannot be edited as a node$/;

export type WarningSummary = {
  /** The server's items, one per line, as it wrote them. */
  items: string[];
  /** One line per category. */
  lines: string[];
  /** Whether anything beyond modules (drawn as groups) and their instances
   *  went unmapped — what the "Partial" badge is for. */
  unmapped: boolean;
};

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/**
 * Summarises the server's warning (ParseAlloy joins every unmapped item with
 * "; "). A module the graph draws as a group reports two lines — the module,
 * and its instance (`"x" is not a component`) — so a config of four modules
 * listed eight; they are one category here. Anything else is genuinely
 * unmapped and counted as such.
 */
export function summariseWarning(warning: string, nodes: readonly GraphNode[]): WarningSummary {
  const items = warning.startsWith(WARNING_PREFIX)
    ? warning.slice(WARNING_PREFIX.length).split('; ')
    : [warning];
  const shown = new Set(nodes.filter(isModuleGroup).map((n) => n.label));
  const modules = new Set<string>();
  let instances = 0;
  const other: string[] = [];
  for (const item of items) {
    const mod = MODULE_LINE.exec(item);
    if (mod) {
      modules.add(mod[1]);
      continue;
    }
    const inst = NOT_COMPONENT_LINE.exec(item);
    if (inst && shown.has(inst[1])) {
      instances++;
      continue;
    }
    other.push(item);
  }
  const lines: string[] = [];
  if (modules.size > 0) {
    const groups =
      modules.size === 1
        ? '1 module is shown as a group of its components'
        : `${modules.size} modules are shown as groups of their components`;
    // "its"/"their" only when every module has exactly one instance;
    // otherwise the instances are counted.
    const nodesWord = instances === 1 ? 'a read-only node' : 'read-only nodes';
    const inst =
      instances === 0
        ? ''
        : instances === modules.size
          ? instances === 1
            ? ', its instance as a read-only node'
            : ', their instances as read-only nodes'
          : `, ${plural(instances, 'instance', 'instances')} as ${nodesWord}`;
    lines.push(`${groups}${inst} — the builder cannot edit modules.`);
  }
  if (other.length > 0) {
    lines.push(
      `${plural(other.length, 'item', 'items')} could not be mapped to the graph${other.length === 1 ? `: ${other[0]}` : '.'}`,
    );
  }
  return { items, lines, unmapped: other.length > 0 };
}

/** The `declare` group a module instance (`opaque.<module>`) instantiates,
 *  resolved as Alloy scopes it: the module of that name declared in the
 *  instance's own scope, else in the nearest enclosing one — never one
 *  declared inside a sibling module. */
export function moduleOfInstance(
  node: GraphNode,
  nodes: readonly GraphNode[],
): GraphNode | undefined {
  return moduleResolver(nodes)(node);
}

/** moduleOfInstance for many nodes of one graph: the id and scope indexes
 *  are built once, so resolving every node stays linear in the graph's size
 *  (chart-generated configs run to hundreds of nodes). */
export function moduleResolver(
  nodes: readonly GraphNode[],
): (node: GraphNode) => GraphNode | undefined {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  // scope ('' for the top level) → module name → the first group declaring it.
  const modules = new Map<string, Map<string, GraphNode>>();
  for (const n of nodes) {
    if (!isModuleGroup(n)) continue;
    const scope = n.parent ?? '';
    let named = modules.get(scope);
    if (!named) {
      named = new Map();
      modules.set(scope, named);
    }
    if (!named.has(n.label)) named.set(n.label, n);
  }
  return (node) => {
    if (!isOpaqueNode(node.component)) return undefined;
    const name = node.component.slice('opaque.'.length);
    const seen = new Set<string>();
    let scope: string | undefined = node.parent ?? '';
    while (scope !== undefined && !seen.has(scope)) {
      seen.add(scope);
      const here: string = scope;
      const found = modules.get(here)?.get(name);
      if (found) return found;
      scope = here === '' ? undefined : (byId.get(here)?.parent ?? '');
    }
    return undefined;
  };
}

type EdgeEnds = {
  id: string;
  from: { node: string; port: string };
  to: { node: string; port: string };
};

/**
 * Each edge's accessible name — `Edge from <kind label> to <kind label>` —
 * made unique among the edges given. Parallel edges between the same two
 * nodes (an OTel batch processor's metrics, logs and traces outputs into
 * one exporter) add their ports; edges whose ends share a kind and label
 * in different module scopes add the source's module; anything still alike
 * is numbered.
 */
export function edgeNames(
  edges: readonly EdgeEnds[],
  nodes: readonly GraphNode[],
): Map<string, string> {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const name = (id: string) => {
    const n = byId.get(id);
    return n ? nodeKindLabel(n) : id;
  };
  const names = new Map(
    edges.map((e) => [e.id, `Edge from ${name(e.from.node)} to ${name(e.to.node)}`]),
  );
  const refine = (extra: (e: EdgeEnds) => string) => {
    const count = new Map<string, number>();
    for (const n of names.values()) count.set(n, (count.get(n) ?? 0) + 1);
    for (const e of edges) {
      const current = names.get(e.id) as string;
      if ((count.get(current) ?? 0) < 2) continue;
      const more = extra(e);
      if (more) names.set(e.id, `${current} ${more}`);
    }
  };
  refine((e) => (e.from.port || e.to.port ? `(${e.from.port} to ${e.to.port})` : ''));
  refine((e) => {
    const parent = byId.get(e.from.node)?.parent;
    const scope = parent ? byId.get(parent) : undefined;
    return scope ? `in module ${scope.label}` : '';
  });
  const seen = new Map<string, number>();
  for (const e of edges) {
    const current = names.get(e.id) as string;
    const k = (seen.get(current) ?? 0) + 1;
    seen.set(current, k);
    if (k > 1) names.set(e.id, `${current} (${k})`);
  }
  return names;
}

/** A node named by its kind and label — `discovery.relabel nodes`, `module
 *  cluster_metrics`, `cluster_metrics feature` (an instance) — so two nodes
 *  sharing a label stay apart in an edge's accessible name. */
export function nodeKindLabel(node: GraphNode): string {
  if (isModuleGroup(node)) return `module ${node.label}`;
  const kind = isOpaqueNode(node.component)
    ? node.component.slice('opaque.'.length)
    : node.component;
  return `${kind} ${node.label}`;
}
