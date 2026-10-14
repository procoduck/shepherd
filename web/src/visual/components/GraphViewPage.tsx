import { useNavigate, useParams } from '@tanstack/react-router';
import {
  applyEdgeChanges,
  applyNodeChanges,
  Background,
  Controls,
  type Edge,
  type EdgeChange,
  MiniMap,
  type Node,
  type NodeChange,
  type NodeTypes,
  ReactFlow,
  useNodesInitialized,
  useReactFlow,
} from '@xyflow/react';
import { X } from 'lucide-react';
import { useCallback, useEffect, useId, useMemo, useState } from 'react';
import '@xyflow/react/dist/base.css';
import { type GraphViewResult, graphView } from '../../api/client';
import { Modal } from '../../components/ui/Modal';
import {
  UNAVAILABLE_CLASS,
  UnavailableReason,
  unavailableProps,
} from '../../components/ui/unavailable';
import { useOrg } from '../../hooks/useOrg';
import { exprOf } from '../bindings';
import { graphViewFlow, isOpaqueNode, RAW_KEY } from '../graphViewFlow';
import { loadCurrentSchema } from '../schemaCache';
import { useVisualStore } from '../store';
import type { GraphBinding, GraphNode } from '../types';
import type { PipelineNodeData } from './PipelineNode';
import { PipelineNode } from './PipelineNode';

const nodeTypes: NodeTypes = { pipeline: PipelineNode as NodeTypes[string] };

// The `fitView` prop fits once, on mount — before this page's nodes, which
// arrive in an effect, have sizes; with nothing measured the fit snapped to the
// maximum zoom and showed one corner of the graph. This fits again each time
// React Flow reports every node measured (the nodes are re-seeded once more
// when the schema arrives); the graph itself never changes after that.
function FitWhenMeasured() {
  const initialized = useNodesInitialized();
  const { fitView } = useReactFlow();
  useEffect(() => {
    if (initialized) void fitView({ padding: 0.1, maxZoom: 1 });
  }, [initialized, fitView]);
  return null;
}

const WARNING_PREFIX = 'this pipeline could not be fully mapped to a graph: ';

/** The server's warning (ParseAlloy joins every unmapped item with "; "), one
 *  item per line. */
function GraphViewWarning({ warning }: { warning: string }) {
  const items = warning.startsWith(WARNING_PREFIX)
    ? warning.slice(WARNING_PREFIX.length).split('; ')
    : [warning];
  return (
    <div
      role='note'
      aria-label='Mapping warnings'
      data-testid='graph-view-warning'
      className='max-h-32 shrink-0 overflow-y-auto border-b border-amber-500/30 bg-amber-500/10 px-4 py-2 text-xs text-amber-400'
    >
      <p className='font-medium'>This pipeline could not be fully mapped to a graph:</p>
      <ul className='list-disc pl-5'>
        {items.map((item, i) => (
          <li key={i}>{item}</li>
        ))}
      </ul>
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
}: {
  node: GraphNode;
  nodes: GraphNode[];
  bindings: GraphBinding[];
  onClose: () => void;
}) {
  const byId = (id: string) => nodes.find((n) => n.id === id);
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
      {isOpaqueNode(node.component) && (
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

  const [graphData, setGraphData] = useState<GraphViewResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showRecreateConfirm, setShowRecreateConfirm] = useState(false);
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);

  // Load schema if not already loaded
  useEffect(() => {
    if (!schema) {
      // The shared copy (schemaCache.ts) — the route started the fetch.
      loadCurrentSchema().then(setSchema).catch(console.error);
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
  // draft would carry `opaque.*` components that fail to render. Recreate is
  // unavailable until every block maps, and says how many do not.
  const opaqueCount = graphData?.graph.nodes.filter((n) => isOpaqueNode(n.component)).length ?? 0;
  const recreateReason =
    opaqueCount > 0
      ? `${opaqueCount} ${opaqueCount === 1 ? 'block' : 'blocks'} can't be carried over to the visual builder: ${opaqueCount === 1 ? "it isn't a component" : "they aren't components"} the builder can edit.`
      : undefined;
  const recreateReasonId = useId();

  const handleRecreate = () => {
    if (!graphData || recreateReason) return;
    // Load the parsed graph into the store as a draft for a new visual pipeline.
    // DECISION: this is a copy — no link to the original pipeline.
    useVisualStore.getState().doc; // touch to ensure store is init
    // Navigate to new visual pipeline; the user will refine the imported graph.
    // We store the imported graph in sessionStorage for the new-pipeline page to pick up.
    // The view's bindings are dropped: the server reports them so this page
    // can show references, but each one's expression already sits in its
    // node's props, so carried into the builder it would render twice.
    sessionStorage.setItem('vb:import-graph', JSON.stringify({ ...graphData.graph, bindings: [] }));
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

  // This view is read-only, but it is still React Flow in CONTROLLED mode, so it
  // owes the same contract as the editable canvas (see CanvasPane's
  // "controlled-mode contract"): React Flow's own fields — `measured` above all
  // — reach it only through the change stream that applyNodeChanges applies.
  // Without a change handler nothing here was ever measured, so this view's
  // minimap drew an empty rectangle exactly as the editor's did. The graph
  // itself never changes after it loads, so seeding on load is enough; React
  // Flow owns the arrays from then on.
  const [rfNodes, setRfNodes] = useState<Node<PipelineNodeData>[]>([]);
  const [rfEdges, setRfEdges] = useState<Edge[]>([]);

  useEffect(() => {
    setRfNodes(
      (graphData?.graph.nodes ?? []).map((n) => ({
        id: n.id,
        type: 'pipeline',
        position: n.position,
        data: {
          ...n,
          schema: schema?.components[n.component],
          diagnostics: [],
          readOnly: true,
          genericPorts: flow.genericPorts[n.id],
        } as PipelineNodeData,
      })),
    );
    // #310: named by node labels, not React Flow's "Edge from <id> to <id>".
    const label = (id: string) => graphData?.graph.nodes.find((n) => n.id === id)?.label ?? id;
    const docEdges = graphData?.graph.edges ?? [];
    setRfEdges(
      flow.edges.map((e, i) => ({
        ...e,
        // Named in dataflow order, whichever way React Flow had to hold it.
        ariaLabel: `Edge from ${label(docEdges[i].from.node)} to ${label(docEdges[i].to.node)}`,
      })),
    );
  }, [graphData, schema, flow]);

  const onNodesChange = useCallback(
    (changes: NodeChange<Node<PipelineNodeData>>[]) =>
      setRfNodes((cur) => applyNodeChanges<Node<PipelineNodeData>>(changes, cur)),
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
        {graphData?.opaque && (
          <span className='text-xs text-amber-600 bg-amber-50 px-2 py-0.5 rounded border border-amber-200'>
            Partial — some expressions could not be mapped
          </span>
        )}
        <div className='ml-auto flex items-center gap-2'>
          {recreateReason && (
            <span className='text-xs text-muted' aria-hidden='true'>
              {opaqueCount} {opaqueCount === 1 ? 'block' : 'blocks'} not mapped
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
      {graphData?.warning && <GraphViewWarning warning={graphData.warning} />}

      {/* Read-only canvas — no palette, no controls that mutate */}
      <div className='flex-1 relative' data-testid='graph-view-canvas'>
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
        >
          <Background />
          <Controls showInteractive={false} />
          <MiniMap position='bottom-left' />
          <FitWhenMeasured />
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
