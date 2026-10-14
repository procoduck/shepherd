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
    const inst =
      instances === 0
        ? ''
        : instances === 1
          ? ', its instance as a read-only node'
          : ', their instances as read-only nodes';
    lines.push(`${groups}${inst} — the builder cannot edit modules.`);
  }
  if (other.length > 0) {
    lines.push(
      `${plural(other.length, 'item', 'items')} could not be mapped to the graph${other.length === 1 ? `: ${other[0]}` : '.'}`,
    );
  }
  return { items, lines, unmapped: other.length > 0 };
}

/** The `declare` group a module instance (`opaque.<module>`) instantiates: the
 *  module of that name in the instance's own scope, else any of that name. */
export function moduleOfInstance(
  node: GraphNode,
  nodes: readonly GraphNode[],
): GraphNode | undefined {
  if (!isOpaqueNode(node.component)) return undefined;
  const name = node.component.slice('opaque.'.length);
  const candidates = nodes.filter((n) => isModuleGroup(n) && n.label === name);
  return candidates.find((n) => (n.parent ?? '') === (node.parent ?? '')) ?? candidates[0];
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
