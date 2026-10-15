import { syncStatusTone, toneClass } from '@/components/ui/statusTone';
import type { RepoLink } from '@/gen/shepherd/mgmt/v1/gitops_pb';

/**
 * A repo link's sync status badge, with the reason the last sync recorded
 * (sync_error) underneath when it ended in "error" or "warning". A warning
 * is a sync that held deletes back because it would have removed every
 * pipeline of the link, or more than half of them; the reason says what to
 * do about it. The reason is plain text right after the badge in the same
 * cell, so a screen reader reads it in order — no aria wiring on the
 * non-focusable badge, which would not be announced anyway.
 */
export function SyncStatusCell({ link }: { link: RepoLink }) {
  const status = link.syncStatus;
  const reason = status === 'error' || status === 'warning' ? link.syncError : '';
  return (
    <div className='flex flex-col items-start gap-1'>
      <span
        data-testid={
          status === 'error' || status === 'warning' ? `sync-status-${status}` : undefined
        }
        className={`text-xs px-1.5 py-0.5 rounded ${toneClass(syncStatusTone(status))}`}
      >
        {status || 'pending'}
      </span>
      {reason && (
        <p
          data-testid='sync-error'
          // The tone's own text/surface pair: contrast-checked in both
          // themes (theme.test.ts), unlike tone text on the page background.
          className={`max-w-sm rounded border px-1.5 py-1 text-xs break-words ${toneClass(syncStatusTone(status))}`}
        >
          {reason}
        </p>
      )}
    </div>
  );
}
