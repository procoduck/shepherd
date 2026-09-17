import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import {
  diffRevisions,
  type GraphBindingChange,
  type GraphChangeKind,
  type GraphEdgeChange,
  type GraphNodeChange,
} from '../../api/client';
import { clients } from '../../api/transport';
import { Modal } from '../../components/ui/Modal';

/**
 * RevisionCompare is the visual builder's counterpart to the text editor's
 * RevisionDiff (#118). It lists a saved pipeline's revisions and, on selection,
 * renders the server-computed structural graph diff of that revision against the
 * pipeline's current saved state — added / removed / changed nodes, wires and
 * bindings, rather than a line diff of the generated Alloy.
 *
 * Read-only: restoring a revision stays the text editor's job for now.
 */
export function RevisionCompare({
  pipelineId,
  orgId,
  onClose,
}: {
  pipelineId: string;
  orgId: string;
  onClose: () => void;
}) {
  const [fromRevision, setFromRevision] = useState<number | null>(null);

  const { data: revisionsData } = useQuery({
    queryKey: ['revisions', orgId, pipelineId],
    queryFn: () => clients.pipeline.listRevisions({ orgId, id: pipelineId }),
    enabled: !!orgId,
  });
  const revisions = revisionsData?.items ?? [];

  const { data: diff, isFetching } = useQuery({
    queryKey: ['revision-graph-diff', orgId, pipelineId, fromRevision],
    queryFn: () => diffRevisions(orgId, pipelineId, fromRevision ?? 0, 0),
    enabled: !!orgId && fromRevision != null,
  });

  return (
    <Modal title='Compare revisions' onClose={onClose} size='xl' testId='revision-compare'>
      <div className='flex gap-4' style={{ minHeight: '20rem' }}>
        {/* Revision list */}
        <div className='w-44 shrink-0 space-y-1 overflow-y-auto' style={{ maxHeight: '28rem' }}>
          {revisions.length === 0 && (
            <p className='text-xs text-muted'>No revisions yet — save a change first.</p>
          )}
          {revisions.map((r) => (
            <button
              key={r.revision}
              type='button'
              data-testid={`compare-revision-${r.revision}`}
              onClick={() => setFromRevision(r.revision)}
              className={`w-full rounded border px-2 py-1.5 text-left text-xs ${
                fromRevision === r.revision
                  ? 'border-indigo-500 bg-indigo-500/10'
                  : 'border-border bg-card/40 hover:border-border-strong'
              }`}
            >
              <div className='font-medium text-zinc-200'>#{r.revision}</div>
              <div className='text-muted-2 truncate'>{r.changedBy}</div>
              {r.changeNote && <div className='text-muted italic truncate'>{r.changeNote}</div>}
            </button>
          ))}
        </div>

        {/* Diff */}
        <div className='flex-1 overflow-y-auto' style={{ maxHeight: '28rem' }}>
          {fromRevision == null ? (
            <p className='text-sm text-muted'>
              Pick a revision to see what changed between it and the current version.
            </p>
          ) : isFetching || !diff ? (
            <p className='text-sm text-muted' data-testid='compare-loading'>
              Comparing…
            </p>
          ) : (
            <div className='space-y-4' data-testid='compare-diff'>
              <p className='text-xs text-muted-2'>
                Revision <span className='text-zinc-200'>#{fromRevision}</span> vs current
              </p>

              {(diff.from_opaque || diff.to_opaque) && (
                <div className='rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-400'>
                  {diff.warning ||
                    'One side had no saved graph and was reconstructed from its config, so this diff is best-effort.'}
                </div>
              )}

              {diff.node_changes.length === 0 &&
              diff.edge_changes.length === 0 &&
              diff.binding_changes.length === 0 ? (
                <p className='text-sm text-emerald-400' data-testid='compare-no-changes'>
                  No graph changes between #{fromRevision} and the current version.
                </p>
              ) : (
                <>
                  <DiffSection title='Nodes' count={diff.node_changes.length}>
                    {diff.node_changes.map((n) => (
                      <NodeChangeRow key={n.id} change={n} />
                    ))}
                  </DiffSection>
                  <DiffSection title='Wires' count={diff.edge_changes.length}>
                    {diff.edge_changes.map((e) => (
                      <EdgeChangeRow key={e.id} change={e} />
                    ))}
                  </DiffSection>
                  <DiffSection title='Bindings' count={diff.binding_changes.length}>
                    {diff.binding_changes.map((b) => (
                      <BindingChangeRow key={`${b.node}.${b.prop}`} change={b} />
                    ))}
                  </DiffSection>
                </>
              )}
            </div>
          )}
        </div>
      </div>
    </Modal>
  );
}

const KIND_LABEL: Record<GraphChangeKind, string> = {
  added: 'Added',
  removed: 'Removed',
  changed: 'Changed',
};
const KIND_CLASS: Record<GraphChangeKind, string> = {
  added: 'text-emerald-400 border-emerald-500/40',
  removed: 'text-red-400 border-red-500/40',
  changed: 'text-amber-400 border-amber-500/40',
};

function KindBadge({ kind }: { kind: GraphChangeKind }) {
  return (
    <span
      data-testid={`change-kind-${kind}`}
      className={`shrink-0 rounded border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide ${KIND_CLASS[kind]}`}
    >
      {KIND_LABEL[kind]}
    </span>
  );
}

function DiffSection({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: React.ReactNode;
}) {
  if (count === 0) return null;
  return (
    <div className='space-y-1.5'>
      <h3 className='text-xs font-semibold text-muted'>
        {title} ({count})
      </h3>
      <div className='space-y-1'>{children}</div>
    </div>
  );
}

function NodeChangeRow({ change }: { change: GraphNodeChange }) {
  return (
    <div className='rounded border border-border bg-card/40 px-2 py-1.5 text-xs'>
      <div className='flex items-center gap-2'>
        <KindBadge kind={change.kind} />
        <span className='font-mono text-zinc-200'>
          {change.component}
          {change.label ? ` "${change.label}"` : ''}
        </span>
      </div>
      {change.field_changes.length > 0 && (
        <ul className='mt-1 space-y-0.5 pl-1'>
          {change.field_changes.map((f) => (
            <li key={f.field} className='text-muted-2'>
              <span className='font-mono text-muted'>{f.field}</span>{' '}
              <span className='text-red-400'>{f.old_value || '∅'}</span>
              {' → '}
              <span className='text-emerald-400'>{f.new_value || '∅'}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const port = (p: { node: string; port: string }) => (p.port ? `${p.node}.${p.port}` : p.node);

function EdgeChangeRow({ change }: { change: GraphEdgeChange }) {
  return (
    <div className='flex items-center gap-2 rounded border border-border bg-card/40 px-2 py-1.5 text-xs'>
      <KindBadge kind={change.kind} />
      <span className='font-mono text-zinc-200'>
        {port(change.from)} → {port(change.to)}
      </span>
    </div>
  );
}

const ref = (r: { node: string; export: string; expr: string }) => {
  if (r.expr) return r.expr;
  if (!r.node && !r.export) return '∅';
  return r.export ? `${r.node}.${r.export}` : r.node;
};

function BindingChangeRow({ change }: { change: GraphBindingChange }) {
  return (
    <div className='rounded border border-border bg-card/40 px-2 py-1.5 text-xs'>
      <div className='flex items-center gap-2'>
        <KindBadge kind={change.kind} />
        <span className='font-mono text-zinc-200'>
          {change.node}.{change.prop}
        </span>
      </div>
      {change.kind === 'changed' && (
        <div className='mt-1 pl-1 text-muted-2'>
          <span className='text-red-400'>{ref(change.old_ref)}</span>
          {' → '}
          <span className='text-emerald-400'>{ref(change.new_ref)}</span>
        </div>
      )}
    </div>
  );
}
