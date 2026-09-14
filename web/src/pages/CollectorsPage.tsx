import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useMemo, useState } from 'react';
import { clients } from '@/api/transport';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input, Select } from '@/components/ui/Field';
import type { Collector } from '@/gen/shepherd/mgmt/v1/fleet_pb';
import { useOrgId } from '@/hooks/useOrg';
import { formatTimestampRelative } from '@/lib/utils';

const STATUS_COLORS: Record<string, string> = {
  APPLIED: 'text-emerald-400 bg-emerald-400/10 border-emerald-400/20',
  APPLYING: 'text-yellow-400 bg-yellow-400/10 border-yellow-400/20',
  FAILED: 'text-red-400 bg-red-400/10 border-red-400/20',
};

const collectorColumns: DataTableColumn<Collector>[] = [
  {
    key: 'cluster',
    header: 'Cluster',
    render: (c) => (
      <Link to='/collectors/$id' params={{ id: c.id }}>
        {c.cluster}
      </Link>
    ),
  },
  { key: 'role', header: 'Role', cellClassName: 'px-4 py-2.5 text-muted', render: (c) => c.role },
  {
    key: 'status',
    header: 'Status',
    render: (c) => {
      const status = c.remoteConfigStatus?.toUpperCase() ?? '';
      const statusColor = STATUS_COLORS[status] ?? 'text-muted bg-border border-border-strong';
      return (
        <span className={`text-xs font-medium px-2 py-0.5 rounded border ${statusColor}`}>
          {status || 'UNKNOWN'}
        </span>
      );
    },
  },
  {
    key: 'lastSeen',
    header: 'Last Seen',
    cellClassName: 'px-4 py-2.5 text-muted',
    render: (c) => formatTimestampRelative(c.lastSeen),
  },
  {
    key: 'version',
    header: 'Version',
    cellClassName: 'px-4 py-2.5 text-muted',
    render: (c) => c.alloyVersion || '—',
  },
  {
    key: 'labels',
    header: 'Labels',
    render: (c) => (
      <div className='flex flex-wrap gap-2'>
        {Object.entries(c.labels)
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([key, value]) => (
            <span key={key} className='break-all font-mono text-xs'>
              {key}={value}
            </span>
          ))}
      </div>
    ),
  },
];

export function CollectorsPage() {
  const orgId = useOrgId();
  const [search, setSearch] = useState('');
  const [groupBy, setGroupBy] = useState('');
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['collectors', orgId],
    queryFn: () => clients.fleet.listCollectors({ orgId }),
    enabled: !!orgId,
  });
  const collectors = data?.items ?? [];
  const labelKeys = useMemo(
    () => [...new Set(collectors.flatMap((c) => Object.keys(c.labels)))].sort(),
    [collectors],
  );
  const activeGroup = labelKeys.includes(groupBy) ? groupBy : '';
  const query = search.trim().toLowerCase();
  const filtered = useMemo(
    () =>
      collectors.filter((c) =>
        [
          c.cluster,
          c.role,
          c.alloyVersion,
          c.remoteConfigStatus || 'unknown',
          ...Object.entries(c.labels).map(([key, value]) => `${key}=${value}`),
        ].some((value) => value.toLowerCase().includes(query)),
      ),
    [collectors, query],
  );
  const groups = useMemo(() => {
    const result = new Map<string | undefined, Collector[]>();
    for (const collector of filtered) {
      const value = activeGroup ? collector.labels[activeGroup] : '';
      const groupKey = activeGroup && value === undefined ? undefined : value;
      const group = result.get(groupKey) ?? [];
      group.push(collector);
      result.set(groupKey, group);
    }
    return result;
  }, [activeGroup, filtered]);

  return (
    <div className='space-y-4'>
      <div className='flex items-center justify-between'>
        <h1 className='text-xl font-semibold'>Collectors</h1>
      </div>
      <div className='flex items-end gap-3'>
        <Field label='Search collectors' className='flex-1'>
          <Input value={search} onChange={(e) => setSearch(e.target.value)} />
        </Field>
        <Field label='Group by'>
          <Select value={activeGroup} onChange={(e) => setGroupBy(e.target.value)}>
            <option value=''>None</option>
            {labelKeys.map((key) => (
              <option key={key} value={key}>
                {key}
              </option>
            ))}
          </Select>
        </Field>
        <span className='py-2 text-xs text-muted'>
          {filtered.length} / {collectors.length}
        </span>
      </div>
      {isError ? (
        <QueryError error={error} noun='collectors' />
      ) : isLoading || !orgId ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : filtered.length === 0 ? (
        <p className='text-sm text-muted'>No collectors found.</p>
      ) : (
        [...groups.entries()]
          .sort(([a], [b]) => (a ?? '').localeCompare(b ?? ''))
          .map(([value, rows], groupIndex) => (
            <section
              key={value === undefined ? 'unlabeled' : `value:${value}`}
              className='space-y-2'
            >
              {activeGroup && (
                <h2 id={`collector-group-${groupIndex}`} className='text-sm font-medium'>
                  {value === undefined ? 'Unlabeled' : `${activeGroup}=${value}`} ({rows.length})
                </h2>
              )}
              <DataTable
                ariaLabelledBy={activeGroup ? `collector-group-${groupIndex}` : undefined}
                columns={collectorColumns}
                rows={rows}
                rowKey={(c) => c.id}
                rowClassName='border-t border-border hover:bg-card/60 cursor-pointer'
              />
            </section>
          ))
      )}
    </div>
  );
}
