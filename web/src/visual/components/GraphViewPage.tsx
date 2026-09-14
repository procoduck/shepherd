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
} from '@xyflow/react';
import { useCallback, useEffect, useState } from 'react';
import '@xyflow/react/dist/base.css';
import { type GraphViewResult, graphView } from '../../api/client';
import { Modal } from '../../components/ui/Modal';
import { useOrg } from '../../hooks/useOrg';
import { useVisualStore } from '../store';
import type { PipelineNodeData } from './PipelineNode';
import { PipelineNode } from './PipelineNode';

const nodeTypes: NodeTypes = { pipeline: PipelineNode as NodeTypes[string] };

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
      fetch('/api/schema/current', { headers: { 'X-Requested-With': 'XMLHttpRequest' } })
        .then((r) => r.json())
        .then(setSchema)
        .catch(console.error);
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

  const handleRecreate = () => {
    if (!graphData) return;
    // Load the parsed graph into the store as a draft for a new visual pipeline.
    // DECISION: this is a copy — no link to the original pipeline.
    useVisualStore.getState().doc; // touch to ensure store is init
    // Navigate to new visual pipeline; the user will refine the imported graph.
    // We store the imported graph in sessionStorage for the new-pipeline page to pick up.
    sessionStorage.setItem('vb:import-graph', JSON.stringify(graphData.graph));
    navigate({ to: '/pipelines/visual/new' });
  };

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
        } as PipelineNodeData,
      })),
    );
    setRfEdges(
      (graphData?.graph.edges ?? []).map((e) => ({
        id: e.id,
        source: e.from.node,
        sourceHandle: e.from.port,
        target: e.to.node,
        targetHandle: e.to.port,
      })),
    );
  }, [graphData, schema]);

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
        <div className='ml-auto'>
          <button
            className='text-sm px-3 py-1 rounded border hover:bg-accent'
            onClick={() => setShowRecreateConfirm(true)}
            data-testid='recreate-as-visual-btn'
          >
            Recreate as visual pipeline
          </button>
        </div>
      </div>

      {/* Read-only canvas — no palette, no inspector, no controls that mutate */}
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
        </ReactFlow>
        {selectedNodeId &&
          (() => {
            const node = graphData?.graph.nodes.find((n) => n.id === selectedNodeId);
            return node ? (
              <div className='absolute right-0 top-0 w-72 m-2 bg-card border rounded p-3 text-xs shadow-lg z-10'>
                <div className='font-mono font-semibold mb-1'>{node.component}</div>
                <div className='text-muted mb-2'>"{node.label}"</div>
                {Object.entries(node.props).length > 0 && (
                  <div className='space-y-1'>
                    {Object.entries(node.props).map(([key, value]) => (
                      <div key={key} className='flex gap-2'>
                        <span className='text-muted'>{key}:</span>
                        <span className='font-mono truncate'>{String(value)}</span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
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
