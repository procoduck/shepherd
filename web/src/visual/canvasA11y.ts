/**
 * canvasA11y.ts — the canvas's keyboard and screen-reader model (#426), kept
 * free of React and of the DOM so every rule here is unit-tested directly
 * (canvasA11y.test.ts). The hook that drives it is
 * `components/useCanvasKeyboard.ts`; the shortcut sheet that documents it is
 * `components/CanvasShortcuts.tsx` (`CANVAS_SHORTCUTS` below is its content).
 */
import type { GraphDocument, GraphEdge, GraphNode, SchemaPayload } from './types';
import { orientConnection } from './wireOrient';

export type Direction = 'left' | 'right' | 'up' | 'down';

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

/** A node's accessible name:
 *  `prometheus.scrape "app", 1 input, 1 output` (+ `, disabled`, `, 2 errors`). */
export function nodeAriaLabel(
  node: Pick<GraphNode, 'component' | 'label' | 'disabled'>,
  inputs: number,
  outputs: number,
  problems: { errors: number; warnings: number } = { errors: 0, warnings: 0 },
): string {
  const parts = [
    `${node.component} "${node.label}"`,
    plural(inputs, 'input'),
    plural(outputs, 'output'),
  ];
  if (node.disabled) parts.push('disabled');
  if (problems.errors > 0) parts.push(plural(problems.errors, 'error'));
  if (problems.warnings > 0) parts.push(plural(problems.warnings, 'warning'));
  return parts.join(', ');
}

/** A port's accessible name — name, direction, wire type, wires on it:
 *  `forward_to, output, prom.metrics, 1 wire`. */
export function portAriaLabel(
  port: { label: string; role: 'accepts' | 'produces'; wireType: string },
  wires: number,
): string {
  return [
    port.label,
    port.role === 'accepts' ? 'input' : 'output',
    port.wireType || 'untyped',
    plural(wires, 'wire'),
  ].join(', ');
}

const nodeName = (n: Pick<GraphNode, 'component' | 'label'> | undefined, fallback: string) =>
  n ? `${n.component} "${n.label}"` : fallback;

/** A wire's accessible name, in dataflow order:
 *  `Wire from prometheus.scrape "app" forward_to to prometheus.remote_write "sink" receiver`. */
export function edgeAriaLabel(edge: GraphEdge, nodes: GraphNode[]): string {
  const from = nodes.find((n) => n.id === edge.from.node);
  const to = nodes.find((n) => n.id === edge.to.node);
  return `Wire from ${nodeName(from, edge.from.node)} ${edge.from.port} to ${nodeName(to, edge.to.node)} ${edge.to.port}`;
}

interface Placed {
  id: string;
  position: { x: number; y: number };
}

/** Nodes in reading order for a left-to-right dataflow: by column (x), then
 *  top to bottom (y), then id — what Home/End and entering the canvas use. */
export function readingOrder<T extends Placed>(nodes: T[]): T[] {
  return [...nodes].sort(
    (a, b) => a.position.x - b.position.x || a.position.y - b.position.y || (a.id < b.id ? -1 : 1),
  );
}

/**
 * The nearest node from `fromId` in `dir`: only nodes on that side count, and
 * a node off the axis costs twice its offset, so ArrowRight prefers the node
 * level with this one over a nearer one far above. Null when nothing lies
 * that way.
 */
export function spatialNeighbor(nodes: Placed[], fromId: string, dir: Direction): string | null {
  const from = nodes.find((n) => n.id === fromId);
  if (!from) return null;
  let best: { id: string; score: number } | null = null;
  for (const n of nodes) {
    if (n.id === fromId) continue;
    const dx = n.position.x - from.position.x;
    const dy = n.position.y - from.position.y;
    const [primary, secondary] =
      dir === 'right'
        ? [dx, dy]
        : dir === 'left'
          ? [-dx, dy]
          : dir === 'down'
            ? [dy, dx]
            : [-dy, dx];
    if (primary <= 0) continue;
    const score = primary + 2 * Math.abs(secondary);
    if (!best || score < best.score || (score === best.score && n.id < best.id))
      best = { id: n.id, score };
  }
  return best?.id ?? null;
}

/** One handle on the canvas: React Flow's source/target split, by node. */
export interface HandleRef {
  nodeId: string;
  handleId: string;
  rfType: 'source' | 'target';
}

export type ConnectOutcome =
  | { ok: true; from: GraphEdge['from']; to: GraphEdge['to'] }
  | { ok: false; reason: 'invalid' | 'cycle' | 'duplicate' };

/**
 * Whether `a` and `b` may be wired, as the mouse path decides it: one is a
 * React Flow source and the other a target, wireOrient finds a role-valid
 * orientation, the wire is not already there, and it closes no cycle. The
 * outcome carries the oriented endpoints the store's `addEdge` takes.
 */
export function checkConnection(
  schema: SchemaPayload | null,
  doc: Pick<GraphDocument, 'nodes' | 'edges'>,
  a: HandleRef,
  b: HandleRef,
): ConnectOutcome {
  if (a.rfType === b.rfType || a.nodeId === b.nodeId) return { ok: false, reason: 'invalid' };
  const [source, target] = a.rfType === 'source' ? [a, b] : [b, a];
  const oriented = orientConnection(schema, doc, {
    source: source.nodeId,
    sourceHandle: source.handleId,
    target: target.nodeId,
    targetHandle: target.handleId,
  });
  if (!oriented) return { ok: false, reason: 'invalid' };
  const { from, to } = oriented;
  if (
    doc.edges.some(
      (e) =>
        e.from.node === from.node &&
        e.from.port === from.port &&
        e.to.node === to.node &&
        e.to.port === to.port,
    )
  )
    return { ok: false, reason: 'duplicate' };
  const adj = new Map<string, string[]>();
  for (const e of doc.edges) adj.set(e.from.node, [...(adj.get(e.from.node) ?? []), e.to.node]);
  adj.set(from.node, [...(adj.get(from.node) ?? []), to.node]);
  const seen = new Set<string>();
  const reachesSource = (u: string): boolean => {
    if (u === from.node) return true;
    if (seen.has(u)) return false;
    seen.add(u);
    return (adj.get(u) ?? []).some(reachesSource);
  };
  if (reachesSource(to.node)) return { ok: false, reason: 'cycle' };
  return { ok: true, from, to };
}

/** The wires on one handle — for a target handle, the edges React Flow draws
 *  into it; for a source handle, out of it. */
export function edgesOnHandle(
  edges: GraphEdge[],
  rfEndpoints: (e: GraphEdge) => {
    source: string;
    sourceHandle: string;
    target: string;
    targetHandle: string;
  },
  handle: HandleRef,
): GraphEdge[] {
  return edges.filter((e) => {
    const rf = rfEndpoints(e);
    return handle.rfType === 'source'
      ? rf.source === handle.nodeId && rf.sourceHandle === handle.handleId
      : rf.target === handle.nodeId && rf.targetHandle === handle.handleId;
  });
}

/** The shortcut sheet (CanvasShortcuts.tsx) and the docs page list exactly
 *  this, so the two cannot drift from what useCanvasKeyboard does. */
export const CANVAS_SHORTCUTS: ReadonlyArray<{
  group: string;
  items: ReadonlyArray<{ keys: string; action: string }>;
}> = [
  {
    group: 'Nodes',
    items: [
      { keys: 'Tab / Shift+Tab', action: 'Move into the canvas, then between nodes and wires' },
      { keys: 'Arrow keys', action: 'Focus the nearest node in that direction' },
      { keys: 'Home / End', action: 'Focus the first / last node (left to right)' },
      { keys: 'Space', action: 'Select the focused node (the Properties panel shows it)' },
      { keys: 'I', action: 'Open the focused node in the inspector' },
      { keys: 'Shift+Arrow keys', action: 'Move the focused node (8px per press)' },
      { keys: 'Delete / Backspace', action: 'Delete the focused node and its wires' },
      { keys: 'Enter', action: "Go to the focused node's ports" },
    ],
  },
  {
    group: 'Ports and wires',
    items: [
      { keys: 'Arrow keys', action: "Move between the node's ports" },
      { keys: 'Enter / Space', action: 'Start a wire from the focused port' },
      {
        keys: 'Arrow keys (wiring)',
        action: 'Move between compatible ports only — incompatible ones are skipped',
      },
      { keys: 'Enter / Space (wiring)', action: 'Connect to the focused port' },
      { keys: 'Escape', action: 'Cancel the wire, or go back to the node' },
      { keys: 'Delete / Backspace', action: 'Delete the wires on the focused port' },
      { keys: 'Delete / Backspace on a wire', action: 'Delete that wire' },
    ],
  },
  {
    group: 'Canvas',
    items: [
      { keys: '?', action: 'Show these shortcuts' },
      { keys: 'Ctrl/⌘+A', action: 'Select every node' },
      { keys: 'Ctrl/⌘+C, Ctrl/⌘+V', action: 'Copy and paste the selection' },
      { keys: 'Ctrl/⌘+Z, Shift+Ctrl/⌘+Z', action: 'Undo, redo' },
    ],
  },
];
