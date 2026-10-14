/**
 * graphViewLayout.ts — module groups and the ELK layout of the read-only graph
 * view (GraphViewPage).
 *
 * GraphView expands each Alloy `declare` into a group node (component
 * `declare`) whose components carry its id in `parent` (internal/visual/
 * parse.go, ParseOptions.ExpandModules). This file decides what is visible
 * (a collapsed group hides everything inside it), builds the hierarchical ELK
 * graph for what is visible — groups as compound nodes, their ports pinned to
 * the side data crosses them on — and reads ELK's answer back as React Flow
 * positions (relative to the parent group, which is what `parentId` means to
 * React Flow) plus each group's size and port offsets.
 *
 * Pure apart from the ELK instance handed in: the page loads elkjs lazily (a
 * worker, elkLayout.ts), the unit tests use elkjs's bundled build.
 */
import type { ELK, ElkExtendedEdge, ElkNode, ElkPort } from 'elkjs/lib/elk-api';
import type { GraphViewEdge } from './graphViewFlow';
import type { GraphNode } from './types';

/** The component GraphView gives a module's group node. */
export const MODULE_COMPONENT = 'declare';

export const isModuleGroup = (n: Pick<GraphNode, 'component'>) => n.component === MODULE_COMPONENT;

/**
 * A group with more children than this starts collapsed. Every module the
 * grafana/k8s-monitoring chart renders has at most 15 (cluster_metrics), so
 * the chart's configs open fully expanded — tracing a module's components to
 * their destination is the point of the view — while a generated module with
 * dozens of components does not bury the rest of the pipeline. The user can
 * expand it.
 */
export const COLLAPSE_CHILDREN_OVER = 20;

/** Group geometry, in px. The header holds the module name and the toggle;
 *  ELK's padding keeps children clear of it. */
export const GROUP_HEADER_PX = 36;
export const GROUP_PADDING_PX = 24;
export const COLLAPSED_GROUP_WIDTH_PX = 240;
const PORT_ROW_PX = 22;

/** A leaf's size before it is measured (the server-grid render measures every
 *  leaf first, so this is only a floor). */
export const DEFAULT_LEAF_SIZE = { width: 220, height: 80 };

export interface GroupTree {
  /** Child ids per group id, in document order. */
  children: Map<string, string[]>;
  /** Parent id per node id ('' at the top level). */
  parentOf: Map<string, string>;
  groups: Set<string>;
}

export function groupTree(
  nodes: readonly Pick<GraphNode, 'id' | 'component' | 'parent'>[],
): GroupTree {
  const ids = new Set(nodes.map((n) => n.id));
  const children = new Map<string, string[]>();
  const parentOf = new Map<string, string>();
  const groups = new Set<string>();
  for (const n of nodes) {
    if (isModuleGroup(n)) groups.add(n.id);
    // A parent that is not in the graph is treated as the top level, so a
    // malformed response still draws every node.
    const parent = n.parent && ids.has(n.parent) ? n.parent : '';
    parentOf.set(n.id, parent);
    if (parent) children.set(parent, [...(children.get(parent) ?? []), n.id]);
  }
  return { children, parentOf, groups };
}

/** Every group id from `id`'s parent up to the top level. */
export function ancestors(tree: GroupTree, id: string): string[] {
  const out: string[] = [];
  for (let p = tree.parentOf.get(id) ?? ''; p; p = tree.parentOf.get(p) ?? '') {
    if (out.includes(p)) break; // a cycle in a malformed response
    out.push(p);
  }
  return out;
}

/** The groups that start collapsed (see COLLAPSE_CHILDREN_OVER). */
export function defaultCollapsed(tree: GroupTree): Set<string> {
  const out = new Set<string>();
  for (const g of tree.groups) {
    if ((tree.children.get(g)?.length ?? 0) > COLLAPSE_CHILDREN_OVER) out.add(g);
  }
  return out;
}

/** Whether a node is hidden: some group around it is collapsed. */
export function isHidden(tree: GroupTree, collapsed: ReadonlySet<string>, id: string): boolean {
  return ancestors(tree, id).some((g) => collapsed.has(g));
}

/** The side and handles of one port on a group, as graphViewFlow drew them. */
export interface GroupPort {
  key: string;
  label: string;
  side: 'left' | 'right';
  /** The React Flow handle ids at this port (a source and/or a target). */
  handles: { handleId: string; rfType: 'source' | 'target' }[];
}

/** Collects each group's ports from the flow edges that touch it. */
export function groupPorts(
  edges: readonly GraphViewEdge[],
  groups: ReadonlySet<string>,
): Map<string, GroupPort[]> {
  const out = new Map<string, GroupPort[]>();
  const add = (node: string, handleId: string, rfType: 'source' | 'target') => {
    if (!groups.has(node)) return;
    // Generic handle ids are generic:<side>:<port> (graphViewFlow.ts).
    const [, side, ...rest] = handleId.split(':');
    const label = rest.join(':');
    const key = `${side}:${label}`;
    const list = out.get(node) ?? [];
    let port = list.find((p) => p.key === key);
    if (!port) {
      port = { key, label, side: side === 'right' ? 'right' : 'left', handles: [] };
      list.push(port);
    }
    if (!port.handles.some((h) => h.handleId === handleId && h.rfType === rfType))
      port.handles.push({ handleId, rfType });
    out.set(node, list);
  };
  for (const e of edges) {
    add(e.source, e.sourceHandle, 'source');
    add(e.target, e.targetHandle, 'target');
  }
  return out;
}

/** A collapsed group's size: its header plus one row per port on its longer side. */
export function collapsedGroupSize(ports: readonly GroupPort[]) {
  const rows = Math.max(
    ports.filter((p) => p.side === 'left').length,
    ports.filter((p) => p.side === 'right').length,
    1,
  );
  return { width: COLLAPSED_GROUP_WIDTH_PX, height: GROUP_HEADER_PX + rows * PORT_ROW_PX + 8 };
}

/** The fixed offset of a collapsed group's (or the grid fallback's) port row. */
export const portRowTop = (index: number) =>
  GROUP_HEADER_PX + index * PORT_ROW_PX + PORT_ROW_PX / 2;

export interface LayoutInput {
  nodes: readonly Pick<GraphNode, 'id' | 'component' | 'parent'>[];
  edges: readonly GraphViewEdge[];
  collapsed: ReadonlySet<string>;
  /** Measured leaf sizes by node id (DEFAULT_LEAF_SIZE when missing). */
  sizes: ReadonlyMap<string, { width: number; height: number }>;
}

export interface LayoutResult {
  /** Position of each visible node, relative to its parent group. */
  positions: Map<string, { x: number; y: number }>;
  /** Size of each visible group. */
  groupSizes: Map<string, { width: number; height: number }>;
  /** Vertical offset of each group port, by group id then GroupPort.key. */
  portTops: Map<string, Map<string, number>>;
  /** The visible flow edges (both ends visible). */
  edges: GraphViewEdge[];
  ms: number;
}

const portId = (group: string, key: string) => `${group}::${key}`;

/** Lays out the visible part of a module-grouped graph with ELK (layered,
 *  left to right, nested groups laid out with their contents). */
export async function layoutGraph(elk: ELK, input: LayoutInput): Promise<LayoutResult> {
  const started = performance.now();
  const tree = groupTree(input.nodes);
  const visible = input.nodes.filter((n) => !isHidden(tree, input.collapsed, n.id));
  const visibleIds = new Set(visible.map((n) => n.id));
  const edges = input.edges.filter((e) => visibleIds.has(e.source) && visibleIds.has(e.target));
  const ports = groupPorts(edges, tree.groups);

  const elkNodes = new Map<string, ElkNode>();
  for (const n of visible) {
    const isGroup = tree.groups.has(n.id);
    const expanded = isGroup && !input.collapsed.has(n.id);
    const node: ElkNode = { id: n.id };
    if (isGroup) {
      node.ports = (ports.get(n.id) ?? []).map(
        (p): ElkPort => ({
          id: portId(n.id, p.key),
          width: 8,
          height: 8,
          layoutOptions: { 'elk.port.side': p.side === 'left' ? 'WEST' : 'EAST' },
        }),
      );
      node.layoutOptions = { 'elk.portConstraints': 'FIXED_SIDE' };
      if (expanded) {
        node.children = [];
        node.edges = [];
        node.layoutOptions['elk.padding'] =
          `[top=${GROUP_HEADER_PX + GROUP_PADDING_PX / 2},left=${GROUP_PADDING_PX},bottom=${GROUP_PADDING_PX},right=${GROUP_PADDING_PX}]`;
      } else {
        Object.assign(node, collapsedGroupSize(ports.get(n.id) ?? []));
      }
    } else {
      Object.assign(node, input.sizes.get(n.id) ?? DEFAULT_LEAF_SIZE);
    }
    elkNodes.set(n.id, node);
  }

  const root: ElkNode = {
    id: '__root__',
    layoutOptions: {
      'elk.algorithm': 'layered',
      'elk.direction': 'RIGHT',
      'elk.hierarchyHandling': 'INCLUDE_CHILDREN',
      'elk.layered.spacing.nodeNodeBetweenLayers': '60',
      'elk.spacing.nodeNode': '30',
      'elk.spacing.componentComponent': '40',
    },
    children: [],
    edges: [],
  };
  const containerOf = (id: string) => elkNodes.get(tree.parentOf.get(id) ?? '') ?? root;
  for (const n of visible) containerOf(n.id).children?.push(elkNodes.get(n.id) as ElkNode);

  // ELK wants each edge in its endpoints' lowest common ancestor; an edge
  // between a group and something inside it lives in that group.
  for (const e of edges) {
    const above = { source: ancestors(tree, e.source), target: ancestors(tree, e.target) };
    const lca = above.target.includes(e.source)
      ? e.source
      : above.source.includes(e.target)
        ? e.target
        : above.source.find((g) => above.target.includes(g));
    const container = lca ? (elkNodes.get(lca) ?? root) : root;
    const end = (node: string, handleId: string) => {
      if (!tree.groups.has(node)) return node;
      const [, side, ...rest] = handleId.split(':');
      return portId(node, `${side}:${rest.join(':')}`);
    };
    // In dataflow direction, so ELK's layers read left to right the way the
    // data moves.
    const ends = [end(e.source, e.sourceHandle), end(e.target, e.targetHandle)];
    if (e.reversed) ends.reverse();
    const edge: ElkExtendedEdge = { id: e.id, sources: [ends[0]], targets: [ends[1]] };
    container.edges = [...(container.edges ?? []), edge];
  }

  const laid = await elk.layout(root);
  const positions = new Map<string, { x: number; y: number }>();
  const groupSizes = new Map<string, { width: number; height: number }>();
  const portTops = new Map<string, Map<string, number>>();
  const walk = (node: ElkNode) => {
    for (const c of node.children ?? []) {
      positions.set(c.id, { x: c.x ?? 0, y: c.y ?? 0 });
      if (tree.groups.has(c.id)) {
        groupSizes.set(c.id, { width: c.width ?? 0, height: c.height ?? 0 });
        const tops = new Map<string, number>();
        for (const p of c.ports ?? []) {
          tops.set(p.id.slice(c.id.length + 2), (p.y ?? 0) + (p.height ?? 0) / 2);
        }
        portTops.set(c.id, tops);
      }
      walk(c);
    }
  };
  walk(laid);
  return { positions, groupSizes, portTops, edges, ms: performance.now() - started };
}
