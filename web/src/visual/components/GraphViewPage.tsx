import { useNavigate, useParams } from '@tanstack/react-router';
import {
  Background,
  Controls,
  type Edge,
  MiniMap,
  type Node,
  type NodeTypes,
  ReactFlow,
} from '@xyflow/react';
import { useEffect, useState } from 'react';
import '@xyflow/react/dist/base.css';
import { type GraphViewResult, graphView } from '../../api/client';
import { clients } from '../../api/transport';
import { useVisualStore } from '../store';
import type { PipelineNodeData } from './PipelineNode';
import { PipelineNode } from './PipelineNode';

const nodeTypes: NodeTypes = { pipeline: PipelineNode as NodeTypes[string] };

export function GraphViewPage() {
  const { id } = useParams({ strict: false }) as { id: string };
  const navigate = useNavigate();
  const schema = useVisualStore((s) => s.schema);
  const setSchema = useVisualStore((s) => s.setSchema);

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

  // Load graph view
  useEffect(() => {
    if (!id) return;
    // Infer org from the first org the actor belongs to.
    clients.me
      .getMe({})
      .then((me) => {
        const orgId = me.orgs?.[0]?.id;
        if (!orgId) throw new Error('no org');
        return graphView(orgId, id);
      })
      .then((data) => {
        setGraphData(data);
        setLoading(false);
      })
      .catch((e: Error) => {
        setError(e.message);
        setLoading(false);
      });
  }, [id]);

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

  // Convert graph doc to React Flow nodes/edges for display
  const rfNodes: Node<PipelineNodeData>[] = (graphData?.graph.nodes ?? []).map((n) => ({
    id: n.id,
    type: 'pipeline',
    position: n.position,
    data: {
      ...n,
      schema: schema?.components[n.component],
      diagnostics: [],
      readOnly: true,
    } as PipelineNodeData,
  }));

  const rfEdges: Edge[] = (graphData?.graph.edges ?? []).map((e) => ({
    id: e.id,
    source: e.from.node,
    sourceHandle: e.from.port,
    target: e.to.node,
    targetHandle: e.to.port,
  }));

  if (loading) {
    return (
      <div className='flex items-center justify-center h-full p-8 text-sm text-muted-foreground'>
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
        <span className='text-sm font-medium text-muted-foreground'>Graph view — read only</span>
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
                <div className='text-muted-foreground mb-2'>"{node.label}"</div>
                {Object.entries(node.props).length > 0 && (
                  <div className='space-y-1'>
                    {Object.entries(node.props).map(([key, value]) => (
                      <div key={key} className='flex gap-2'>
                        <span className='text-muted-foreground'>{key}:</span>
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
        <div className='fixed inset-0 bg-black/40 flex items-center justify-center z-50'>
          <div className='bg-card border rounded-lg p-6 max-w-md w-full shadow-lg'>
            <h2 className='font-semibold mb-3'>Recreate as visual pipeline?</h2>
            <p className='text-sm text-muted-foreground mb-4'>
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
          </div>
        </div>
      )}
    </div>
  );
}
