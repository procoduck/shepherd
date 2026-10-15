import { useNavigate, useParams } from '@tanstack/react-router';
import {
  applyEdgeChanges,
  applyNodeChanges,
  Background,
  type Edge,
  type EdgeChange,
  type Node,
  type NodeChange,
  type NodeTypes,
  ReactFlow,
  useNodesInitialized,
  useReactFlow,
} from '@xyflow/react';
import { X } from 'lucide-react';
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import '@xyflow/react/dist/base.css';
import { type GraphViewResult, graphView } from '../../api/client';
import { Modal } from '../../components/ui/Modal';
import { toneClass } from '../../components/ui/statusTone';
import {
  UNAVAILABLE_CLASS,
  UnavailableReason,
  unavailableProps,
} from '../../components/ui/unavailable';
import { useCanWrite, useOrg } from '../../hooks/useOrg';
import { exprOf } from '../bindings';
import { type ElkHandle, loadElk, withLayoutGuards } from '../elkLayout';
import { builderDraftFromGraphView, graphViewFlow, isOpaqueNode, RAW_KEY } from '../graphViewFlow';
import {
  collapsedGroupSize,
  defaultCollapsed,
  groupPorts,
  groupTree,
  isModuleGroup,
  type LayoutResult,
  layoutGraph,
} from '../graphViewLayout';
import { edgeNames, moduleOfInstance, summariseWarning } from '../graphViewText';
import { loadCurrentSchema } from '../schemaCache';
import { useVisualStore } from '../store';
import type { GraphBinding, GraphNode } from '../types';
import { CanvasControls, CanvasMiniMap, FIT_PADDING } from './CanvasChrome';
import { type ModuleGroupData, ModuleGroupNode } from './ModuleGroupNode';
import type { PipelineNodeData } from './PipelineNode';
import { PipelineNode } from './PipelineNode';

const nodeTypes: NodeTypes = {
  pipeline: PipelineNode as NodeTypes[string],
  module: ModuleGroupNode as NodeTypes[string],
};

type LayoutStatus = 'measuring' | 'elk' | 'grid';

// How far out this view may zoom. A chart-generated config is far larger than
// anything drawn in the builder (whose floor is 0.25): at React Flow's default
// of 0.5 the k8s-monitoring metrics graph (41 nodes) could not be fitted at
// 1440x900, and Fit View left three nodes outside the pane (2026-10-14
// walkthrough M2).
const GRAPH_MIN_ZOOM = 0.1;
// Every fit — the first one after the layout, and the Fit View button —
// frames the graph clear of the zoom controls and the minimap (CanvasChrome).
const GRAPH_FIT = { padding: FIT_PADDING, maxZoom: 1, minZoom: GRAPH_MIN_ZOOM };
type Size = { width: number; height: number };

/**
 * Drives the layout from inside React Flow (its hooks need the provider).
 *
 * The page first renders every node flat at the server's grid positions, so
 * React Flow measures each component's real size; the first time every node
 * is measured this hands those sizes to the page, which lays the graph out
 * with ELK. It fits the view once more after each layout the page marks as a
 * re-fit (the first ELK layout, or the fallback to the grid) and never on a
 * collapse or expand — that must not undo a pan or zoom the user has made.
 *
 * The `fitView` prop alone fits on mount, before this page's nodes, which
 * arrive in an effect, have sizes; with nothing measured that fit snapped to
 * the maximum zoom and showed one corner of the graph.
 */
/** Whether React Flow's store holds exactly the nodes the page seeded (it
 *  takes them in from an effect, so for a render it still has the last seed). */
function holds(held: readonly Node[], seeded: readonly Node[]) {
  const data = new Map(seeded.map((n) => [n.id, n.data]));
  return held.length === data.size && held.every((n) => data.get(n.id) === n.data);
}

function LayoutController({
  nodes,
  ready,
  status,
  measureKey,
  fitKey,
  focus,
  onMeasured,
}: {
  /** The nodes the page seeded: sizes are read, and the view fitted, only
   *  once React Flow holds exactly these (a stale store still reports the
   *  previous seed measured). */
  nodes: readonly Node[];
  /** False until the schema has loaded (or failed to): it decides the ports,
   *  so a node measured before it has the wrong height. */
  ready: boolean;
  status: LayoutStatus;
  /** Changes whenever the page re-seeds the nodes for a new measurement. */
  measureKey: number;
  fitKey: number;
  /** A node to bring into view (Show module), once per request. */
  focus: { id: string; seq: number } | null;
  onMeasured: (sizes: Map<string, Size>) => void;
}) {
  const initialized = useNodesInitialized();
  const { fitView, getNodes } = useReactFlow();
  const reported = useRef(false);
  useEffect(() => {
    reported.current = false;
  }, [measureKey]);
  useEffect(() => {
    if (!initialized || !ready || status !== 'measuring' || reported.current) return;
    const held = getNodes();
    if (!holds(held, nodes)) return;
    reported.current = true;
    const sizes = new Map<string, Size>();
    for (const n of held) {
      if (n.type !== 'pipeline' || !n.measured?.width || !n.measured?.height) continue;
      sizes.set(n.id, { width: n.measured.width, height: n.measured.height });
    }
    onMeasured(sizes);
  }, [initialized, ready, status, nodes, getNodes, onMeasured]);
  const fitted = useRef(-1);
  useEffect(() => {
    if (!initialized || fitKey === fitted.current || !holds(getNodes(), nodes)) return;
    // After React Flow has taken the new positions in.
    const frame = requestAnimationFrame(() => {
      fitted.current = fitKey;
      void fitView(GRAPH_FIT);
    });
    return () => cancelAnimationFrame(frame);
  }, [initialized, fitKey, nodes, getNodes, fitView]);
  const focused = useRef(-1);
  useEffect(() => {
    if (!focus || focus.seq === focused.current) return;
    focused.current = focus.seq;
    void fitView({ ...GRAPH_FIT, nodes: [{ id: focus.id }], duration: 200 });
  }, [focus, fitView]);
  return null;
}

/** The server's warning, summarised one line per category (graphViewText.ts),
 *  with every item it wrote behind "Show all": a config of four modules listed
 *  eight lines in a scrolling box (2026-10-14 walkthrough L1). */
function GraphViewWarning({ items, lines }: { items: string[]; lines: string[] }) {
  return (
    <div
      role='note'
      aria-label='Mapping warnings'
      data-testid='graph-view-warning'
      className='max-h-40 shrink-0 overflow-y-auto border-b border-amber-500/30 bg-amber-500/10 px-4 py-1.5 text-xs text-amber-400'
    >
      <ul data-testid='graph-view-warning-summary'>
        {lines.map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
      <details data-testid='graph-view-warning-details'>
        <summary className='cursor-pointer select-none underline-offset-2 hover:underline'>
          Show all {items.length} {items.length === 1 ? 'detail' : 'details'}
        </summary>
        <ul className='list-disc pl-5'>
          {items.map((item, i) => (
            <li key={i}>{item}</li>
          ))}
        </ul>
      </details>
    </div>
  );
}

/** One prop's value: an expression as written, a nested block or list as
 *  formatted JSON (never `[object Object]`), a scalar as itself. */
function PropValue({ value }: { value: unknown }) {
  const expr = exprOf(value);
  if (expr !== undefined) return <code className='font-mono break-all'>{expr}</code>;
  if (value !== null && typeof value === 'object')
    return (
      <pre className='max-h-48 overflow-auto whitespace-pre rounded bg-panel p-1 font-mono'>
        {JSON.stringify(value, null, 2)}
      </pre>
    );
  return <span className='font-mono break-all'>{String(value)}</span>;
}

const nodeName = (n: GraphNode | undefined) =>
  n ? `${n.component.replace(/^opaque\./, '')} "${n.label}"` : 'a removed node';

function NodeInspector({
  node,
  nodes,
  bindings,
  onClose,
  onSelect,
}: {
  node: GraphNode;
  nodes: GraphNode[];
  bindings: GraphBinding[];
  onClose: () => void;
  onSelect: (id: string) => void;
}) {
  const byId = (id: string) => nodes.find((n) => n.id === id);
  // An opaque block that instantiates a module the graph draws as a group is
  // that module's instance, not an unknown block (2026-10-14 walkthrough L2).
  const module = moduleOfInstance(node, nodes);
  const raw = node.props[RAW_KEY];
  const props = Object.entries(node.props).filter(([key]) => key !== RAW_KEY);
  const uses = bindings.filter((b) => b.node === node.id);
  const usedBy = bindings.filter((b) => b.ref.node === node.id);
  return (
    <aside
      aria-label='Node details'
      data-testid='graph-view-inspector'
      className='absolute right-0 top-0 z-10 m-2 flex max-h-[calc(100%-1rem)] w-96 flex-col overflow-y-auto rounded border bg-card p-3 text-xs shadow-lg'
    >
      <div className='mb-1 flex items-start gap-2'>
        <div className='min-w-0 flex-1'>
          <div className='font-mono font-semibold break-all'>
            {isOpaqueNode(node.component) ? node.component.slice('opaque.'.length) : node.component}
          </div>
          <div className='text-muted'>"{node.label}"</div>
        </div>
        <button
          type='button'
          aria-label='Close node details'
          className='rounded p-0.5 text-muted hover:bg-accent'
          onClick={onClose}
        >
          <X size={14} />
        </button>
      </div>
      {module && (
        <p className='mb-2 text-muted'>
          An instance of module {module.label}: it sets the module's arguments, and the module's
          components are drawn in its group.{' '}
          <button
            type='button'
            className='underline underline-offset-2 hover:text-accent'
            onClick={() => onSelect(module.id)}
          >
            Show module {module.label}
          </button>
        </p>
      )}
      {isOpaqueNode(node.component) && !module && (
        <p className='mb-2 text-amber-400'>
          Not mapped: this block is not a component the graph can show. Its wires are drawn; its
          contents are shown as written.
        </p>
      )}
      {props.length > 0 && (
        <div className='space-y-1'>
          {props.map(([key, value]) => (
            <div key={key} data-testid={`inspector-prop-${key}`} className='flex flex-col gap-0.5'>
              <span className='text-muted'>{key}:</span>
              <PropValue value={value} />
            </div>
          ))}
        </div>
      )}
      {(uses.length > 0 || usedBy.length > 0) && (
        <div data-testid='inspector-bindings' className='mt-2 space-y-1 border-t pt-2'>
          <div className='text-muted'>References</div>
          {uses.map((b, i) => (
            <div key={`u${i}`}>
              <span className='font-mono'>{b.prop}</span> uses {nodeName(byId(b.ref.node))}
              <code className='block font-mono break-all text-muted'>{b.ref.expr}</code>
            </div>
          ))}
          {usedBy.map((b, i) => (
            <div key={`r${i}`}>
              {b.ref.export} is used by {nodeName(byId(b.node))} in{' '}
              <span className='font-mono'>{b.prop}</span>
            </div>
          ))}
        </div>
      )}
      {typeof raw === 'string' && (
        <div className='mt-2 border-t pt-2'>
          <div className='mb-1 text-muted'>Source</div>
          <pre
            data-testid='inspector-raw'
            className='max-h-80 overflow-auto whitespace-pre rounded bg-panel p-1 font-mono'
            style={{ tabSize: 2 }}
          >
            {raw}
          </pre>
        </div>
      )}
    </aside>
  );
}

export function GraphViewPage() {
  const { id } = useParams({ strict: false }) as { id: string };
  const navigate = useNavigate();
  const schema = useVisualStore((s) => s.schema);
  const setSchema = useVisualStore((s) => s.setSchema);
  const { orgId, orgs, setOrgId } = useOrg();
  // Recreate creates a pipeline: the same role rule as a new builder graph.
  const canWrite = useCanWrite();

  const [graphData, setGraphData] = useState<GraphViewResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showRecreateConfirm, setShowRecreateConfirm] = useState(false);
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [schemaFailed, setSchemaFailed] = useState(false);

  // Load schema if not already loaded
  useEffect(() => {
    if (!schema) {
      // The shared copy (schemaCache.ts) — the route started the fetch.
      loadCurrentSchema()
        .then(setSchema)
        .catch((e) => {
          console.error(e);
          setSchemaFailed(true);
        });
    }
  }, [schema, setSchema]);

  // Load graph view for the selected org. A /graph URL is shareable, so it
  // may name a pipeline that lives in an org other than the one currently
  // selected — try the selected org first, then the user's other orgs, and
  // move the selection to whichever one answers (same fallback
  // VisualBuilderPage uses for the same reason).
  useEffect(() => {
    if (!id || !orgId) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    (async () => {
      let effectiveOrgId = orgId;
      let data: GraphViewResult | null = await graphView(orgId, id).catch(() => null);
      if (!data) {
        for (const candidate of orgs) {
          if (candidate.id === orgId) continue;
          const found = await graphView(candidate.id, id).catch(() => null);
          if (found) {
            data = found;
            effectiveOrgId = candidate.id;
            break;
          }
        }
      }
      if (cancelled) return;
      if (!data) {
        setError('Graph not found in any organisation you can access.');
        setLoading(false);
        return;
      }
      if (effectiveOrgId !== orgId) setOrgId(effectiveOrgId);
      setGraphData(data);
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [id, orgId, orgs, setOrgId]);

  // Blocks the graph model could not map cannot become builder nodes: the
  // draft would carry `opaque.*` components that fail to render. A module's
  // group cannot either — the builder has no modules, and its components
  // would land flattened at the top level. Recreate is unavailable until
  // every block maps, and says how many do not.
  const opaqueCount =
    graphData?.graph.nodes.filter((n) => isOpaqueNode(n.component) || isModuleGroup(n)).length ?? 0;
  const recreateReason = !canWrite
    ? "Viewers can't create pipelines — ask an org editor or admin"
    : opaqueCount > 0
      ? `${opaqueCount} ${opaqueCount === 1 ? 'block' : 'blocks'} can't be carried over to the visual builder: ${opaqueCount === 1 ? "it isn't a component" : "they aren't components"} the builder can edit.`
      : undefined;
  const recreateReasonId = useId();
  const warning = useMemo(
    () =>
      graphData?.warning ? summariseWarning(graphData.warning, graphData.graph.nodes) : undefined,
    [graphData],
  );
  // Modules drawn as groups are shown, only not editable; "Partial" is for
  // what the graph genuinely could not map (2026-10-14 walkthrough L1).
  const badge = graphData?.opaque
    ? warning && !warning.unmapped
      ? 'Modules shown as groups'
      : 'Partial — some expressions could not be mapped'
    : undefined;

  const handleRecreate = () => {
    if (!graphData || recreateReason) return;
    // Load the parsed graph into the store as a draft for a new visual pipeline.
    // DECISION: this is a copy — no link to the original pipeline.
    useVisualStore.getState().doc; // touch to ensure store is init
    // Navigate to new visual pipeline; the user will refine the imported graph.
    // We store the imported graph in sessionStorage for the new-pipeline page to pick up.
    // Without the view-only bindings (builderDraftFromGraphView says why).
    sessionStorage.setItem(
      'vb:import-graph',
      JSON.stringify(builderDraftFromGraphView(graphData.graph)),
    );
    navigate({ to: '/pipelines/visual/new' });
  };

  // Edges resolved against the nodes' real handles, plus the generic handles
  // a node needs for an edge its schema does not describe (graphViewFlow.ts) —
  // without them an opaque node's edges were silently not drawn.
  const flow = useMemo(
    () =>
      graphViewFlow(
        { nodes: graphData?.graph.nodes ?? [], edges: graphData?.graph.edges ?? [] },
        (component) => schema?.components[component],
      ),
    [graphData, schema],
  );

  // Module groups (GraphView expands each `declare`, graphViewLayout.ts).
  const tree = useMemo(() => groupTree(graphData?.graph.nodes ?? []), [graphData]);
  const ports = useMemo(() => groupPorts(flow.edges, tree.groups), [flow, tree]);
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  useEffect(() => setCollapsed(defaultCollapsed(tree)), [tree]);
  const toggleGroup = useCallback(
    (groupId: string) =>
      setCollapsed((cur) => {
        const next = new Set(cur);
        if (!next.delete(groupId)) next.add(groupId);
        return next;
      }),
    [],
  );

  // The layout: measure every node at the server's grid, then ELK (loaded on
  // this route only, in a worker — elkLayout.ts); the grid stays if elkjs
  // cannot load or the layout fails. A graph with no edges and no groups
  // needs no layout pass beyond the grid either way.
  const [status, setStatus] = useState<LayoutStatus>('measuring');
  const statusRef = useRef(status);
  statusRef.current = status;
  // The layout and the collapse state it was computed for: a toggle shows
  // when its layout lands, never half-applied.
  const [layout, setLayout] = useState<(LayoutResult & { collapsed: Set<string> }) | null>(null);
  const [measureKey, setMeasureKey] = useState(0);
  const [fitKey, setFitKey] = useState(0);
  // The fit request the seeded nodes carry out (set with them, so a fit never
  // runs against the previous layout's nodes).
  const [appliedFitKey, setAppliedFitKey] = useState(0);
  const [sizes, setSizes] = useState<Map<string, Size> | null>(null);
  const elkRef = useRef<Promise<ElkHandle> | null>(null);
  useEffect(() => {
    elkRef.current = loadElk();
    elkRef.current.catch(() => undefined);
    return () => {
      void elkRef.current?.then((h) => h.dispose()).catch(() => undefined);
    };
  }, []);
  // A new graph or schema re-measures: the schema decides which ports a node has.
  useEffect(() => {
    setStatus('measuring');
    setLayout(null);
    setSizes(null);
    setMeasureKey((k) => k + 1);
  }, [graphData, schema]);
  const onMeasured = useCallback((measured: Map<string, Size>) => setSizes(measured), []);

  useEffect(() => {
    if (!sizes || !graphData) return;
    let cancelled = false;
    (async () => {
      try {
        const handle = await (elkRef.current ?? Promise.reject(new Error('no layout engine')));
        const result = await withLayoutGuards(
          handle,
          layoutGraph(handle.elk, {
            nodes: graphData.graph.nodes,
            edges: flow.edges,
            collapsed,
            sizes,
          }),
        );
        if (cancelled) return;
        setLayout({ ...result, collapsed });
        // Re-fit on the first layout only, never on a collapse or expand.
        if (statusRef.current !== 'elk') setFitKey((k) => k + 1);
        setStatus('elk');
      } catch (e) {
        if (cancelled) return;
        console.warn('graph view: layout unavailable, showing the server grid', e);
        setLayout(null);
        if (statusRef.current !== 'grid') setFitKey((k) => k + 1);
        setStatus('grid');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [sizes, graphData, flow, collapsed]);

  // This view is read-only, but it is still React Flow in CONTROLLED mode, so it
  // owes the same contract as the editable canvas (see CanvasPane's
  // "controlled-mode contract"): React Flow's own fields — `measured` above all
  // — reach it only through the change stream that applyNodeChanges applies.
  // Without a change handler nothing here was ever measured, so this view's
  // minimap drew an empty rectangle exactly as the editor's did. The nodes are
  // re-seeded only when the graph, the schema or the layout changes; React
  // Flow owns the arrays in between.
  const [rfNodes, setRfNodes] = useState<Node[]>([]);
  const [rfEdges, setRfEdges] = useState<Edge[]>([]);

  useEffect(() => {
    const nodes = graphData?.graph.nodes ?? [];
    const laidOut = status === 'elk' && layout !== null;
    const out: Node[] = [];
    for (const n of nodes) {
      if (tree.groups.has(n.id)) {
        const isCollapsed = !laidOut || layout.collapsed.has(n.id);
        if (laidOut && !layout.positions.has(n.id)) continue;
        const groupPortsOf = ports.get(n.id) ?? [];
        const size = laidOut
          ? (layout.groupSizes.get(n.id) ?? collapsedGroupSize(groupPortsOf))
          : collapsedGroupSize(groupPortsOf);
        out.push({
          id: n.id,
          type: 'module',
          position: laidOut ? (layout.positions.get(n.id) ?? n.position) : n.position,
          ...(laidOut && tree.parentOf.get(n.id)
            ? { parentId: tree.parentOf.get(n.id), extent: 'parent' as const }
            : {}),
          style: { width: size.width, height: size.height },
          // Behind the children it holds.
          zIndex: -1,
          data: {
            id: n.id,
            label: n.label,
            childCount: tree.children.get(n.id)?.length ?? 0,
            collapsed: isCollapsed,
            collapsible: laidOut,
            ports: groupPortsOf,
            portTops: laidOut
              ? Object.fromEntries(layout.portTops.get(n.id) ?? new Map<string, number>())
              : undefined,
            onToggle: toggleGroup,
          } satisfies ModuleGroupData,
        });
        continue;
      }
      if (laidOut && !layout.positions.has(n.id)) continue;
      const parent = tree.parentOf.get(n.id);
      out.push({
        id: n.id,
        type: 'pipeline',
        position: laidOut ? (layout.positions.get(n.id) ?? n.position) : n.position,
        ...(laidOut && parent ? { parentId: parent, extent: 'parent' as const } : {}),
        data: {
          ...n,
          schema: schema?.components[n.component],
          diagnostics: [],
          readOnly: true,
          genericPorts: flow.genericPorts[n.id],
          // A module instance is not chipped "not mapped" (public-docs
          // review 2026-10-15), matching the inspector (#344).
          moduleInstanceOf: moduleOfInstance(n, nodes)?.label,
        } as PipelineNodeData,
      });
    }
    setRfNodes(out);
    setAppliedFitKey(fitKey);
    // #310: named by the nodes, not React Flow's "Edge from <id> to <id>" —
    // each by its kind and label, since chart configs reuse a label across
    // kinds ("Edge from nodes to nodes", 2026-10-14 walkthrough L4), and
    // made unique among the edges drawn (edgeNames: ports, module, number).
    const docEdges = new Map((graphData?.graph.edges ?? []).map((e) => [e.id, e]));
    const drawn = laidOut ? layout.edges : flow.edges;
    const names = edgeNames(
      drawn.map(
        (e) =>
          // Named in dataflow order, whichever way React Flow had to hold it.
          docEdges.get(e.id) ?? {
            id: e.id,
            from: { node: e.source, port: '' },
            to: { node: e.target, port: '' },
          },
      ),
      nodes,
    );
    setRfEdges(drawn.map(({ reversed: _, ...e }) => ({ ...e, ariaLabel: names.get(e.id) })));
  }, [graphData, schema, flow, tree, ports, status, layout, toggleGroup, fitKey]);

  // Show module (the inspector): select the node on the canvas as a click
  // would, open its details, and bring it into view.
  const [focus, setFocus] = useState<{ id: string; seq: number } | null>(null);
  const showNode = useCallback((nodeId: string) => {
    setSelectedNodeId(nodeId);
    setRfNodes((cur) => cur.map((n) => ({ ...n, selected: n.id === nodeId })));
    setFocus((f) => ({ id: nodeId, seq: (f?.seq ?? 0) + 1 }));
  }, []);

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => setRfNodes((cur) => applyNodeChanges(changes, cur)),
    [],
  );
  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => setRfEdges((cur) => applyEdgeChanges<Edge>(changes, cur)),
    [],
  );

  if (loading) {
    return (
      <div className='flex items-center justify-center h-full p-8 text-sm text-muted'>
        Loading graph…
      </div>
    );
  }

  if (error) {
    return (
      <div className='flex items-center justify-center h-full p-8 text-sm text-red-500'>
        Failed to load graph: {error}
      </div>
    );
  }

  return (
    <div className='flex flex-col h-full' data-testid='graph-view'>
      {/* Toolbar — read-only, no save */}
      <div
        className='h-11 border-b flex items-center px-4 gap-4 shrink-0'
        data-testid='graph-view-toolbar'
      >
        <span className='text-sm font-medium text-muted'>Graph view — read only</span>
        {badge && (
          <span
            data-testid='graph-view-badge'
            // The contrast-checked status tones, in both themes: informational
            // for modules shown as groups, a warning for unmapped content.
            className={`text-xs px-2 py-0.5 rounded border ${toneClass(
              warning && !warning.unmapped ? 'info' : 'warn',
            )}`}
          >
            {badge}
          </span>
        )}
        <div className='ml-auto flex items-center gap-2'>
          {canWrite && recreateReason && (
            <span className='text-xs text-muted' aria-hidden='true'>
              {opaqueCount} {opaqueCount === 1 ? 'block' : 'blocks'} can't be carried over
            </span>
          )}
          <button
            type='button'
            className={`text-sm px-3 py-1 rounded border hover:bg-accent ${UNAVAILABLE_CLASS}`}
            onClick={() => {
              if (!recreateReason) setShowRecreateConfirm(true);
            }}
            data-testid='recreate-as-visual-btn'
            {...unavailableProps(recreateReason, recreateReasonId)}
          >
            Recreate as visual pipeline
          </button>
          <UnavailableReason id={recreateReasonId} reason={recreateReason} />
        </div>
      </div>
      {warning && <GraphViewWarning items={warning.items} lines={warning.lines} />}

      {/* Read-only canvas — no palette, no controls that mutate */}
      {status === 'grid' && (
        <div
          role='note'
          data-testid='graph-view-layout-fallback'
          className='shrink-0 border-b px-4 py-1 text-xs text-muted'
        >
          The layout engine could not load, so nodes are shown on a plain grid and modules are not
          drawn around their components.
        </div>
      )}
      <div
        className='flex-1 relative'
        data-testid='graph-view-canvas'
        data-layout={status}
        data-layout-ms={layout ? Math.round(layout.ms) : undefined}
      >
        <ReactFlow
          nodes={rfNodes}
          edges={rfEdges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          nodesDraggable={false}
          nodesConnectable={false}
          elementsSelectable
          onNodeClick={(_, node) => setSelectedNodeId(node.id)}
          fitView
          fitViewOptions={GRAPH_FIT}
          minZoom={GRAPH_MIN_ZOOM}
        >
          <Background />
          {/* Zoom controls top-left, clear of the minimap (2026-10-14
              walkthrough M1); both as the builder draws them. */}
          <CanvasControls fitViewOptions={GRAPH_FIT} showInteractive={false} />
          <CanvasMiniMap />
          <LayoutController
            nodes={rfNodes}
            ready={schema !== null || schemaFailed}
            status={status}
            measureKey={measureKey}
            fitKey={appliedFitKey}
            focus={focus}
            onMeasured={onMeasured}
          />
        </ReactFlow>
        {selectedNodeId &&
          (() => {
            const node = graphData?.graph.nodes.find((n) => n.id === selectedNodeId);
            return node ? (
              <NodeInspector
                node={node}
                nodes={graphData?.graph.nodes ?? []}
                bindings={graphData?.graph.bindings ?? []}
                onClose={() => setSelectedNodeId(null)}
                onSelect={showNode}
              />
            ) : null;
          })()}
      </div>

      {/* Recreate confirm dialog */}
      {showRecreateConfirm && (
        <Modal
          title='Recreate as visual pipeline?'
          onClose={() => setShowRecreateConfirm(false)}
          testId='recreate-confirm-dialog'
        >
          <p className='text-sm text-muted mb-4'>
            This creates a <strong>new, separate draft</strong> from this pipeline&apos;s current
            content. The conversion is lossy — complex expressions and unsupported constructs may
            not transfer correctly. The original pipeline is not changed.
          </p>
          <div className='flex gap-3 justify-end'>
            <button
              className='text-sm px-3 py-1 rounded border hover:bg-accent'
              onClick={() => setShowRecreateConfirm(false)}
            >
              Cancel
            </button>
            <button
              className='text-sm px-3 py-1 rounded bg-indigo-600 text-white hover:bg-indigo-700'
              onClick={handleRecreate}
              data-testid='recreate-confirm-btn'
            >
              Recreate (lossy)
            </button>
          </div>
        </Modal>
      )}
    </div>
  );
}
