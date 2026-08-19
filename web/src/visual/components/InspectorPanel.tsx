import { useState } from 'react';
import { useVisualStore } from '../store';
import { UpgradeReview } from './UpgradeReview';
export function InspectorPanel() {
  const selected = useVisualStore((s) => s.selected);
  const doc = useVisualStore((s) => s.doc);
  const schema = useVisualStore((s) => s.schema);
  const setDisabled = useVisualStore((s) => s.setDisabled);
  const updateNode = useVisualStore((s) => s.updateNode);
  const importGraph = useVisualStore((s) => s.importGraph);
  const [reviewOpen, setReviewOpen] = useState(false);
  const node = selected.length === 1 ? doc.nodes.find((n) => n.id === selected[0]) : undefined;
  const def = node && schema?.components[node.component];
  const schemaVersion = schema?._meta.alloy_version;
  const currentVersion = schemaVersion
    ? schemaVersion.startsWith('alloy-')
      ? schemaVersion
      : `alloy-${schemaVersion.startsWith('v') ? '' : 'v'}${schemaVersion}`
    : null;
  const upgradeNeeded = currentVersion != null && doc.schema_version !== currentVersion;
  if (!node || !def)
    return (
      <div className='w-[360px] border-l p-4 text-sm text-muted' data-testid='inspector'>
        <p>Select a node to inspect.</p>
        <div className='mt-4 text-xs'>
          Nodes: {doc.nodes.length}
          <br />
          Edges: {doc.edges.length}
        </div>
        {upgradeNeeded && (
          <div
            data-testid='upgrade-banner'
            className='mt-3 p-2 bg-yellow-50 border border-yellow-300 rounded text-xs'
          >
            <span>
              Authored against {doc.schema_version}; current is {currentVersion} —{' '}
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
            onAccept={(newVersion) => {
              importGraph({ ...doc, schema_version: newVersion });
              setReviewOpen(false);
            }}
          />
        )}
      </div>
    );
  return (
    <div className='w-[360px] border-l p-4 overflow-y-auto text-sm' data-testid='inspector'>
      <h3 className='font-semibold mb-3 font-mono text-xs'>{node.component}</h3>
      <div className='space-y-3'>
        {def.attributes.map((attr) => (
          <div key={attr.name}>
            <label className='block text-xs text-muted mb-0.5'>{attr.name}</label>
            {attr.type === 'secret' ? (
              <div
                data-testid={`attr-secret-${attr.name}`}
                className='text-xs italic border rounded px-2 py-1'
              >
                Use a config node binding
              </div>
            ) : attr.values?.length ? (
              <select
                data-testid={`attr-select-${attr.name}`}
                className='w-full border rounded px-2 py-1'
                value={String(node.props[attr.name] ?? '')}
                onChange={(e) =>
                  updateNode(node.id, { props: { ...node.props, [attr.name]: e.target.value } })
                }
              >
                <option value=''>—</option>
                {attr.values.map((v) => (
                  <option key={v}>{v}</option>
                ))}
              </select>
            ) : attr.type === 'bool' ? (
              <input
                data-testid={`attr-bool-${attr.name}`}
                type='checkbox'
                checked={Boolean(node.props[attr.name])}
                onChange={(e) =>
                  updateNode(node.id, { props: { ...node.props, [attr.name]: e.target.checked } })
                }
              />
            ) : (
              <input
                data-testid={`attr-input-${attr.name}`}
                type='text'
                className='w-full border rounded px-2 py-1'
                value={String(node.props[attr.name] ?? '')}
                onChange={(e) =>
                  updateNode(node.id, { props: { ...node.props, [attr.name]: e.target.value } })
                }
              />
            )}
          </div>
        ))}
      </div>
      <div className='mt-6 pt-4 border-t'>
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
  );
}
