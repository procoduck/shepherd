import { Trash2 } from 'lucide-react';
import type { DataTableColumn } from '@/components/ui/DataTable';
import { collectorStatusTone, toneClass } from '@/components/ui/statusTone';
import type { Assignment, CollectorInstance } from '@/gen/shepherd/mgmt/v1/fleet_pb';
import { formatTimestampRelative } from '@/lib/utils';

// Column definitions for CollectorDetailPage's Info (instances) and Access
// (assignments) tables. Split out to keep CollectorDetailPage under the
// file-size guard; no behavior of its own.

/** Badge colour classes for a collector/instance remote-config status. */
export function statusColor(status: string): string {
  return toneClass(collectorStatusTone(status));
}

/**
 * Hover text for a status badge. INACTIVE is derived by the server from the
 * last check-in (agent.inactive_after), not reported by Alloy, so it says so
 * (#237).
 */
export function statusTitle(status: string): string | undefined {
  return status === 'INACTIVE'
    ? 'No check-in within agent.inactive_after. Its last reported status shows again when it checks back in.'
    : undefined;
}

export const instanceColumns: DataTableColumn<CollectorInstance>[] = [
  {
    key: 'name',
    header: 'Name',
    headerClassName: 'px-4 py-2 text-left font-medium',
    cellClassName: 'px-4 py-2.5 font-mono text-xs',
    render: (i) => i.name,
  },
  {
    key: 'version',
    header: 'Version',
    headerClassName: 'px-4 py-2 text-left font-medium',
    cellClassName: 'px-4 py-2.5 text-muted',
    render: (i) => i.alloyVersion || '—',
  },
  {
    key: 'os',
    header: 'OS',
    headerClassName: 'px-4 py-2 text-left font-medium',
    cellClassName: 'px-4 py-2.5 text-muted',
    render: (i) => i.os || '—',
  },
  {
    key: 'lastSeen',
    header: 'Last seen',
    headerClassName: 'px-4 py-2 text-left font-medium',
    cellClassName: 'px-4 py-2.5 text-muted',
    render: (i) => formatTimestampRelative(i.lastSeen),
  },
  {
    key: 'status',
    header: 'Status',
    headerClassName: 'px-4 py-2 text-left font-medium',
    render: (i) => {
      const instStatus = i.remoteConfigStatus?.toUpperCase() ?? '';
      const instColor = statusColor(instStatus);
      return (
        <span
          title={statusTitle(instStatus)}
          className={`text-xs font-medium px-2 py-0.5 rounded border ${instColor}`}
        >
          {instStatus || 'UNKNOWN'}
        </span>
      );
    },
  },
  {
    key: 'error',
    header: 'Error',
    headerClassName: 'px-4 py-2 text-left font-medium',
    cellClassName: 'px-4 py-2.5 text-red-400 text-xs',
    render: (i) => i.remoteConfigError || '—',
  },
];

export function assignmentColumns(
  onRemove: (a: Assignment) => void,
  removePending: boolean,
): DataTableColumn<Assignment>[] {
  return [
    {
      key: 'group',
      header: 'Group',
      headerClassName: 'px-4 py-2 text-left font-medium',
      render: (a) => a.groupDisplayName || '—',
    },
    {
      key: 'groupId',
      header: 'Group ID',
      headerClassName: 'px-4 py-2 text-left font-medium',
      cellClassName: 'px-4 py-2.5 font-mono text-xs text-muted',
      render: (a) => a.groupId,
    },
    {
      key: 'added',
      header: 'Added',
      headerClassName: 'px-4 py-2 text-left font-medium',
      cellClassName: 'px-4 py-2.5 text-muted text-xs',
      render: (a) => formatTimestampRelative(a.createdAt),
    },
    {
      key: 'actions',
      header: '',
      headerClassName: 'px-4 py-2',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (a) => (
        <button
          type='button'
          onClick={() => onRemove(a)}
          disabled={removePending}
          aria-label={`Remove ${a.groupDisplayName || a.groupId}`}
          className='text-muted-3 transition-colors hover:text-red-400 disabled:opacity-50'
        >
          <Trash2 size={14} />
        </button>
      ),
    },
  ];
}
