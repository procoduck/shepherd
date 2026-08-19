import { timestampDate } from '@bufbuild/protobuf/wkt';
import { useQuery } from '@tanstack/react-query';
import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useState } from 'react';
import { clients } from '@/api/transport';
import { useOrgId } from '@/hooks/useOrg';
import { formatTimestampRelative } from '@/lib/utils';

const PAGE_SIZE = 25;

export function AuditPage() {
  const orgId = useOrgId();

  // Draft filter inputs vs. applied filters: typing doesn't refetch on every
  // keystroke — Filter/Clear commits the draft and resets to page 1.
  const [actorDraft, setActorDraft] = useState('');
  const [actionDraft, setActionDraft] = useState('');
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [offset, setOffset] = useState(0);

  const { data, isLoading, isFetching } = useQuery({
    queryKey: ['audit', orgId, actor, action, offset],
    queryFn: () => clients.audit.listAudit({ orgId, actor, action, limit: PAGE_SIZE, offset }),
    enabled: !!orgId,
  });

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
        <p className='text-sm text-muted'>Org-scoped audit trail, newest first.</p>
      </div>

      <form onSubmit={applyFilters} className='flex flex-wrap items-end gap-3'>
        <label className='block text-xs font-medium text-muted'>
          Actor
          <input
            value={actorDraft}
            onChange={(e) => setActorDraft(e.target.value)}
            placeholder='user@example.com'
            className='mt-1 block w-56 rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
          />
        </label>
        <label className='block text-xs font-medium text-muted'>
          Action
          <input
            value={actionDraft}
            onChange={(e) => setActionDraft(e.target.value)}
            placeholder='pipeline.update'
            className='mt-1 block w-56 rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm'
          />
        </label>
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

      {!orgId ? (
        <p className='text-sm text-muted'>No organisation context.</p>
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
          <div className='rounded-lg border border-border overflow-hidden overflow-x-auto'>
            <table className='w-full text-sm'>
              <thead className='bg-card text-muted'>
                <tr>
                  <th className='px-4 py-3 text-left font-medium'>When</th>
                  <th className='px-4 py-3 text-left font-medium'>Actor</th>
                  <th className='px-4 py-3 text-left font-medium'>Action</th>
                  <th className='px-4 py-3 text-left font-medium'>Resource</th>
                </tr>
              </thead>
              <tbody>
                {items.map((entry) => (
                  <tr key={String(entry.id)} className='border-t border-border hover:bg-card/60'>
                    <td
                      className='px-4 py-2.5 text-muted whitespace-nowrap'
                      title={entry.at ? timestampDate(entry.at).toLocaleString() : undefined}
                    >
                      {formatTimestampRelative(entry.at)}
                    </td>
                    <td className='px-4 py-2.5 font-mono text-xs'>
                      {entry.actor || '—'}
                      {entry.actorType && (
                        <span className='ml-1.5 text-muted-3'>({entry.actorType})</span>
                      )}
                    </td>
                    <td className='px-4 py-2.5 font-mono text-xs'>{entry.action}</td>
                    <td className='px-4 py-2.5 text-muted text-xs'>
                      {entry.resourceType}
                      {entry.resourceId && (
                        <span className='ml-1 font-mono text-muted-3'>{entry.resourceId}</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

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
