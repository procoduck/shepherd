import { useEffect, useState } from 'react';
import {
  stampSchemaVersion,
  type UpgradeCheckResult,
  type UpgradeItem,
  upgradeCheck,
} from '../../api/client';
import { useMe } from '../../hooks/useMe';
import { useVisualStore } from '../store';

interface UpgradeReviewProps {
  open: boolean;
  onClose: () => void;
  onAccept: (newVersion: string) => void;
}

function renderItemUI(item: UpgradeItem) {
  switch (item.class) {
    case 'component_removed':
      return (
        <span className='text-red-600'>Component no longer exists — resolve before saving</span>
      );
    case 'attr_removed':
      // The prop will be absent in the new schema; it is safe to save without it.
      // Full one-click removal from the graph is tracked for M8 hardening.
      return (
        <span data-testid='upgrade-discard-attr' className='text-amber-600'>
          Removed attribute: {item.detail} (will be dropped on save)
        </span>
      );
    case 'attr_added_required':
      return <span className='text-red-500'>Required attribute added: {item.detail}</span>;
    case 'enum_value_removed':
      return <span>Enum value removed: {item.detail}</span>;
    case 'port_type_changed':
      return <span>Port type changed: {item.detail}</span>;
    case 'stability_changed':
      return <span>Stability changed: {item.detail}</span>;
    case 'migration_available':
      return <span>Migration available → {item.detail}</span>;
  }
}

export function UpgradeReview({ open, onClose, onAccept }: UpgradeReviewProps) {
  const doc = useVisualStore((s) => s.doc);
  const importGraph = useVisualStore((s) => s.importGraph);
  const { data: me } = useMe();
  const [result, setResult] = useState<UpgradeCheckResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!open || !me?.orgs[0]?.id) return;
    setResult(null);
    setError(null);
    upgradeCheck(me.orgs[0].id, doc)
      .then(setResult)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : 'Failed to check upgrade'));
  }, [doc, me, open]);

  if (!open) return null;
  return (
    <div
      data-testid='upgrade-review'
      className='fixed inset-0 z-50 bg-black/40 flex items-center justify-center'
    >
      <div className='bg-card border rounded-lg p-6 max-w-xl w-full shadow-lg'>
        <h2>Upgrade Review</h2>
        {error ? (
          <p data-testid='upgrade-error'>{error}</p>
        ) : !result ? (
          <p data-testid='upgrade-loading'>Loading…</p>
        ) : (
          <>
            <p>
              From {result.old_version} → {result.new_version}
            </p>
            {result.items.map((item, i) => (
              <div key={i} data-testid={`upgrade-item-${item.class}`} className='flex gap-2 py-2'>
                <span data-testid='upgrade-item-node'>{item.node_label}</span>
                <span data-testid='upgrade-item-detail'>{renderItemUI(item)}</span>
              </div>
            ))}
            {result.items.length === 0 && (
              <p data-testid='upgrade-no-items'>No structural changes detected.</p>
            )}
            <div className='flex gap-2 mt-4 flex-col'>
              <p className='text-xs text-muted'>
                Accept stamps the new schema version in your local draft. Save the pipeline to
                persist the upgrade.
              </p>
              <div className='flex gap-2'>
                <button
                  data-testid='upgrade-accept'
                  onClick={() => {
                    importGraph(stampSchemaVersion(doc, result.new_version));
                    onAccept(result.new_version);
                  }}
                >
                  Accept upgrade
                </button>
                <button data-testid='upgrade-close' onClick={onClose}>
                  Cancel
                </button>
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
