import { SlidersHorizontal } from 'lucide-react';
import { useMemo, useState } from 'react';
import type { L1DiagnosticEx } from '../l1';
import { currentSchemaVersion } from '../schemaVersion';
import { selectSelectedNode, useVisualStore } from '../store';
import type { ComponentDef } from '../types';
import { CollapsiblePanel } from './CollapsiblePanel';
import { AttributeField } from './inspector/AttributeField';
import { BlockGroup } from './inspector/BlockGroup';
import { nextBlockOrder, withAttr } from './inspector/blockOps';
import { buildPortWireIndex, wireCountsFor, wireEdgesFor } from './inspector/portWiring';
import type { AttrLike, BlockLike } from './inspector/schemaShapes';
import { UpgradeReview } from './UpgradeReview';

const diagAt = (diags: L1DiagnosticEx[], path: string[]): string | undefined =>
  diags.find((d) => d.path && d.path.join('.') === path.join('.'))?.message;

/**
 * The single selected node's form: typed widgets per attribute (task item 1),
 * nested block sub-forms with add/remove (task item 2), disabled/explained
 * secrets (task item 3), required-field affordance + inline diagnostics (task
 * item 4), and doc-string + schema-default prefilling (task item 5).
 *
 * `def.attributes`/`def.blocks` are guarded with `?? []` throughout: the
 * review measured 15 components shipping `attributes: null` in an earlier
 * schema generation, and a schema payload is versioned data the inspector
 * does not control (D4 — degrade gracefully, never crash the canvas).
 */
/** The inspector's collapsible frame (draw.io's Format panel). Split out so both
 *  the empty and node-selected states share one panel chrome and one testid. */
function InspectorShell({ children }: { children: React.ReactNode }) {
  return (
    <CollapsiblePanel
      side='right'
      storageKey='vb.inspector.collapsed'
      title='Properties'
      width='w-[300px]'
      testId='inspector'
      collapsedIcon={<SlidersHorizontal size={16} />}
    >
      {children}
    </CollapsiblePanel>
  );
}

export function InspectorPanel() {
  // Narrow, per-concern subscriptions (W5-10) instead of the whole `doc` —
  // `doc` gets a brand-new object reference on EVERY store mutation (every
  // node/edge/binding change, `nodes`/`edges`/`bindings` spread alongside
  // whichever one key actually changed), so subscribing to it re-rendered
  // this panel on every keystroke anywhere in the graph, selected node or
  // not. `selectSelectedNode` is reference-stable across an unrelated node's
  // update (see store.ts); `edges`/`bindings` keep their own array reference
  // whenever a mutation left them untouched (store.ts spreads `state.doc`
  // and overwrites only the key that changed).
  const node = useVisualStore(selectSelectedNode);
  const edges = useVisualStore((s) => s.doc.edges);
  const bindings = useVisualStore((s) => s.doc.bindings);
  const nodesCount = useVisualStore((s) => s.doc.nodes.length);
  const docSchemaVersion = useVisualStore((s) => s.doc.schema_version);
  const schema = useVisualStore((s) => s.schema);
  const rawDiagnostics = useVisualStore((s) => s.diagnostics);
  const setDisabled = useVisualStore((s) => s.setDisabled);
  const updateNode = useVisualStore((s) => s.updateNode);
  const moveEdge = useVisualStore((s) => s.moveEdge);
  const [reviewOpen, setReviewOpen] = useState(false);
  const [showOptional, setShowOptional] = useState(false);
  const def: ComponentDef | undefined = node && schema?.components[node.component];
  const currentVersion = currentSchemaVersion(schema);
  const upgradeNeeded = currentVersion != null && docSchemaVersion !== currentVersion;

  const diagnostics = rawDiagnostics as L1DiagnosticEx[];
  const nodeDiagnostics = useMemo(
    () => (node ? diagnostics.filter((d) => d.node_id === node.id) : []),
    [diagnostics, node],
  );
  const portIndex = useMemo(() => buildPortWireIndex(def), [def]);
  const wireCounts = useMemo(
    () => (node ? wireCountsFor(edges, node.id) : new Map<string, number>()),
    [edges, node],
  );

  if (!node || !def)
    return (
      <InspectorShell>
        <div className='p-4 text-sm text-muted'>
          <p>Select a node to inspect.</p>
          <div className='mt-4 text-xs'>
            Nodes: {nodesCount}
            <br />
            Edges: {edges.length}
          </div>
          {upgradeNeeded && (
            <div
              data-testid='upgrade-banner'
              className='mt-3 p-2 bg-yellow-50 border border-yellow-300 rounded text-xs'
            >
              <span>
                Authored against {docSchemaVersion}; current is {currentVersion} —{' '}
              </span>
              <button
                data-testid='upgrade-review-open'
                className='underline font-medium'
                onClick={() => setReviewOpen(true)}
              >
                Review upgrade
              </button>
            </div>
          )}
          {reviewOpen && (
            <UpgradeReview
              open={reviewOpen}
              onClose={() => setReviewOpen(false)}
              onAccept={() => {
                // UpgradeReview's own Accept handler already imported the
                // pruned+stamped document (pruneRemovedAttrs + stampSchemaVersion)
                // before calling this callback — that import is the sole
                // source of truth for the post-upgrade doc. Re-deriving a
                // document here from a fresh (or worse, stale) store read
                // and re-importing it is redundant at best and, at worst
                // (a render-time snapshot, a future non-synchronous import),
                // silently discards W5-04's pruning. Just close the review.
                setReviewOpen(false);
              }}
            />
          )}
        </div>
      </InspectorShell>
    );

  const attrs: AttrLike[] = def.attributes ?? [];
  const blocks: BlockLike[] = def.blocks ?? [];
  const required = attrs.filter((a) => a.required);
  const optional = attrs.filter((a) => !a.required);

  // Blocks also carry an ORDER, which `props` (an object keyed by block name)
  // cannot hold. Alloy runs loki.process stages in document order, so a node
  // that gained stage.drop after stage.json has to remember that or the
  // renderer falls back to schema order and re-sequences the user's pipeline.
  const blockNames = new Set((def?.blocks ?? []).map((b) => b.name));
  const setProp = (name: string, value: unknown) =>
    updateNode(node.id, {
      props: withAttr(node.props ?? {}, name, value),
      block_order: nextBlockOrder(node.block_order, name, value, blockNames.has(name)),
    });

  const renderAttr = (attr: AttrLike) => {
    const port = portIndex.byPath.get(attr.name);
    return (
      <AttributeField
        key={attr.name}
        attr={attr}
        schemaPath={[attr.name]}
        value={node.props?.[attr.name]}
        onChange={(v) => setProp(attr.name, v)}
        port={port}
        wireCount={port ? (wireCounts.get(port.id) ?? 0) : 0}
        binding={bindings.find((b) => b.node === node.id && b.prop === attr.name)}
        nodeId={node.id}
        instancePath={[attr.name]}
        wireEdges={port ? wireEdgesFor(edges, node.id, port.id) : undefined}
        onMoveEdge={moveEdge}
        error={diagAt(nodeDiagnostics, [attr.name])}
      />
    );
  };

  return (
    <InspectorShell>
      <div className='overflow-y-auto text-sm flex-1 min-h-0'>
        <div className='sticky top-0 bg-background border-b px-4 py-3 z-10'>
          <h3 className='font-semibold font-mono text-xs' data-testid='inspector-component'>
            {node.component}
          </h3>
          {def.doc && <p className='mt-1 text-xs text-muted'>{def.doc}</p>}
          {nodeDiagnostics.length > 0 && (
            <p className='mt-1 text-xs text-red-500' data-testid='inspector-diagnostic-count'>
              {nodeDiagnostics.length} problem{nodeDiagnostics.length === 1 ? '' : 's'} on this node
            </p>
          )}
        </div>

        <div className='p-4 space-y-3'>
          {required.length > 0 && (
            <div className='space-y-3'>
              <h4 className='text-[11px] uppercase tracking-wide text-muted-2'>Required</h4>
              {required.map(renderAttr)}
            </div>
          )}

          {optional.length > 0 &&
            (showOptional ? (
              <div className='space-y-3'>
                <div className='flex items-center justify-between'>
                  <h4 className='text-[11px] uppercase tracking-wide text-muted-2'>
                    Optional ({optional.length})
                  </h4>
                  <button
                    type='button'
                    className='text-[11px] underline text-muted'
                    onClick={() => setShowOptional(false)}
                  >
                    hide
                  </button>
                </div>
                {optional.map(renderAttr)}
              </div>
            ) : (
              <button
                type='button'
                data-testid='inspector-show-optional'
                className='text-xs underline text-muted'
                onClick={() => setShowOptional(true)}
              >
                Show {optional.length} optional attribute{optional.length === 1 ? '' : 's'}
              </button>
            ))}

          {blocks.length > 0 && (
            <div className='space-y-3 pt-1'>
              <h4 className='text-[11px] uppercase tracking-wide text-muted-2'>Blocks</h4>
              {blocks.map((block) => (
                <BlockGroup
                  key={block.name}
                  block={block}
                  value={node.props?.[block.name]}
                  onChange={(v) => setProp(block.name, v)}
                  schemaPath={[block.name]}
                  instancePath={[block.name]}
                  nodeId={node.id}
                  diagnostics={nodeDiagnostics}
                  bindings={bindings}
                  portByPath={portIndex.byPath}
                  wireCounts={wireCounts}
                  edges={edges}
                  onMoveEdge={moveEdge}
                  depth={0}
                />
              ))}
            </div>
          )}

          {attrs.length === 0 && blocks.length === 0 && (
            <p className='text-xs text-muted-2'>This component has no configurable fields.</p>
          )}
        </div>

        <div className='px-4 pb-4 pt-2 border-t'>
          <h4 className='text-xs font-semibold mb-2'>Danger</h4>
          <label className='flex items-center gap-2 text-xs'>
            <input
              data-testid='node-disable-toggle'
              type='checkbox'
              checked={node.disabled}
              onChange={(e) => setDisabled(node.id, e.target.checked)}
            />
            Disable this node
          </label>
        </div>
      </div>
    </InspectorShell>
  );
}
