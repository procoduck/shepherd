import { timestampDate } from '@bufbuild/protobuf/wkt';
import { useQuery } from '@tanstack/react-query';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useState } from 'react';
import { clients } from '@/api/transport';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input, Select } from '@/components/ui/Field';
import type { AuditEntry } from '@/gen/shepherd/mgmt/v1/audit_pb';
import { useMe } from '@/hooks/useMe';
import { useOrg } from '@/hooks/useOrg';
import { formatTimestampRelative } from '@/lib/utils';

const PAGE_SIZE = 25;

const auditColumns: DataTableColumn<AuditEntry>[] = [
  {
    key: 'when',
    header: 'When',
    cellClassName: 'px-4 py-2.5 text-muted whitespace-nowrap',
    render: (entry) => (
      <span title={entry.at ? timestampDate(entry.at).toLocaleString() : undefined}>
        {formatTimestampRelative(entry.at)}
      </span>
    ),
  },
  {
    key: 'actor',
    header: 'Actor',
    cellClassName: 'px-4 py-2.5 font-mono text-xs',
    render: (entry) => (
      <>
        {entry.actor || '—'}
        {entry.actorType && <span className='ml-1.5 text-muted-3'>({entry.actorType})</span>}
      </>
    ),
  },
  {
    key: 'onBehalfOf',
    header: 'On behalf of',
    cellClassName: 'px-4 py-2.5 font-mono text-xs',
    // The delegated half of a machine action. It was stored and returned by
    // the API but never shown, which defeats the point: two-part
    // attribution exists so a human reading this log can see who authorised
    // a machine's write. A dash means no delegation -- a person acting for
    // themselves -- not a missing value.
    render: (entry) =>
      entry.onBehalfOf ? entry.onBehalfOf : <span className='text-muted-3'>—</span>,
  },
  {
    key: 'action',
    header: 'Action',
    cellClassName: 'px-4 py-2.5 font-mono text-xs',
    render: (entry) => entry.action,
  },
  {
    key: 'resource',
    header: 'Resource',
    cellClassName: 'px-4 py-2.5 text-muted text-xs',
    render: (entry) => (
      // Type and id on separate lines: inline, a full UUID ran into the type
      // and read as one token (#212). The id is shortened; hover shows it all.
      <div className='flex flex-col'>
        <span>{entry.resourceType}</span>
        {entry.resourceId && (
          <span
            className='font-mono text-muted-3'
            title={entry.resourceId}
            data-testid='audit-resource-id'
          >
            {entry.resourceId.length > 13 ? `${entry.resourceId.slice(0, 8)}…` : entry.resourceId}
          </span>
        )}
      </div>
    ),
  },
];

// #253: the org view is that org's trail only. Platform events (rows with no
// org: local user management, single sign-on) are read in the app admin's
// global scope, which asks the server for every org by sending no org id —
// the server admits that only for an app admin.
type Scope = 'org' | 'all';

export function AuditPage() {
  const { orgId, orgs } = useOrg();
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const [scopeChoice, setScope] = useState<Scope>('org');
  const scope: Scope = isAppAdmin ? scopeChoice : 'org';
  const queryOrgId = scope === 'all' ? '' : orgId;

  // Draft filter inputs vs. applied filters: typing doesn't refetch on every
  // keystroke — Filter/Clear commits the draft and resets to page 1.
  const [actorDraft, setActorDraft] = useState('');
  const [actionDraft, setActionDraft] = useState('');
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [offset, setOffset] = useState(0);

  const { data, isLoading, isFetching, isError, error } = useQuery({
    queryKey: ['audit', queryOrgId, scope, actor, action, offset],
    queryFn: () =>
      clients.audit.listAudit({ orgId: queryOrgId, actor, action, limit: PAGE_SIZE, offset }),
    enabled: scope === 'all' || !!orgId,
  });

  // Only in the global scope, where rows come from several orgs and from none.
  const orgColumn: DataTableColumn<AuditEntry> = {
    key: 'org',
    header: 'Organisation',
    cellClassName: 'px-4 py-2.5 text-muted text-xs',
    render: (entry) =>
      entry.orgId ? (
        (orgs.find((o) => o.id === entry.orgId)?.name ?? entry.orgId)
      ) : (
        <span className='italic'>Platform</span>
      ),
  };

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const hasFilters = actor !== '' || action !== '';

  function applyFilters(e: React.FormEvent) {
    e.preventDefault();
    setActor(actorDraft.trim());
    setAction(actionDraft.trim());
    setOffset(0);
  }

  function clearFilters() {
    setActorDraft('');
    setActionDraft('');
    setActor('');
    setAction('');
    setOffset(0);
  }

  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + PAGE_SIZE, total);

  return (
    <div className='space-y-4'>
      <div>
        <h1 className='text-xl font-semibold'>Audit log</h1>
        <p className='text-sm text-muted'>
          {scope === 'all'
            ? 'Every organisation plus platform events (users, single sign-on), newest first.'
            : 'This organisation’s audit trail, newest first.'}
        </p>
      </div>

      <form onSubmit={applyFilters} className='flex flex-wrap items-end gap-3'>
        {isAppAdmin && (
          <div className='w-64'>
            <Field label='Scope'>
              <Select
                value={scope}
                onChange={(e) => {
                  setScope(e.target.value as Scope);
                  setOffset(0);
                }}
              >
                <option value='org'>This organisation</option>
                <option value='all'>All organisations and platform events</option>
              </Select>
            </Field>
          </div>
        )}
        <div className='w-56'>
          <Field label='Actor'>
            <Input
              value={actorDraft}
              onChange={(e) => setActorDraft(e.target.value)}
              placeholder='user@example.com'
            />
          </Field>
        </div>
        <div className='w-56'>
          <Field label='Action'>
            <Input
              value={actionDraft}
              onChange={(e) => setActionDraft(e.target.value)}
              placeholder='pipeline.update'
            />
          </Field>
        </div>
        <button
          type='submit'
          className='rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
        >
          Filter
        </button>
        {(hasFilters || actorDraft !== '' || actionDraft !== '') && (
          <button
            type='button'
            onClick={clearFilters}
            className='px-3 py-1.5 text-xs text-muted hover:text-zinc-200'
          >
            Clear
          </button>
        )}
      </form>

      {scope === 'org' && !orgId ? (
        <p className='text-sm text-muted'>No organisation context.</p>
      ) : isError ? (
        <QueryError error={error} noun='the audit log' />
      ) : isLoading ? (
        <p className='text-sm text-muted'>Loading…</p>
      ) : items.length === 0 ? (
        <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
          <p className='text-sm text-muted'>
            {hasFilters ? 'No audit entries match those filters.' : 'No audit entries yet.'}
          </p>
        </div>
      ) : (
        <>
          <DataTable
            columns={scope === 'all' ? [orgColumn, ...auditColumns] : auditColumns}
            rows={items}
            rowKey={(entry) => String(entry.id)}
            scrollX
          />

          <div className='flex items-center justify-between text-xs text-muted'>
            <span>
              {rangeStart}–{rangeEnd} of {total}
              {isFetching && <span className='ml-2 text-muted-3'>refreshing…</span>}
            </span>
            <div className='flex items-center gap-2'>
              <button
                type='button'
                onClick={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))}
                disabled={offset === 0}
                aria-label='Previous page'
                className='flex items-center gap-1 rounded-md border border-border px-2 py-1 hover:bg-card disabled:opacity-40 disabled:hover:bg-transparent'
              >
                <ChevronLeft size={14} /> Prev
              </button>
              <button
                type='button'
                onClick={() => setOffset((o) => o + PAGE_SIZE)}
                disabled={offset + PAGE_SIZE >= total}
                aria-label='Next page'
                className='flex items-center gap-1 rounded-md border border-border px-2 py-1 hover:bg-card disabled:opacity-40 disabled:hover:bg-transparent'
              >
                Next <ChevronRight size={14} />
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
