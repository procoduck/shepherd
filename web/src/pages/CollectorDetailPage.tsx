import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { CheckCircle, Copy, Plus, Search } from 'lucide-react';
import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { CollectorAttributes, CollectorLabelsButton } from '@/components/CollectorLabels';
import { CollectorReconciliation } from '@/components/CollectorReconciliation';
import { QueryError } from '@/components/QueryError';
import { DataTable } from '@/components/ui/DataTable';
import { Field, Input } from '@/components/ui/Field';
import { useMe } from '@/hooks/useMe';
import { useOrgId } from '@/hooks/useOrg';
import { formatTimestampRelative } from '@/lib/utils';
import { assignmentColumns, instanceColumns, STATUS_COLORS } from './collectorColumns';

type Tab = 'config' | 'reconciliation' | 'info' | 'attributes' | 'access';

export function CollectorDetailPage() {
  const { id } = useParams({ from: '/shell/content/collectors/$id' });
  const orgId = useOrgId();
  const { data: me } = useMe();
  const [tab, setTab] = useState<Tab>('config');
  const [copied, setCopied] = useState(false);

  const isOrgAdmin =
    !!me?.isAppAdmin || me?.orgs?.some((o) => o.id === orgId && o.role === 'admin') === true;

  // Keep the Access tab from being stranded selected-but-hidden if the org
  // switcher moves to an org where the viewer isn't an admin.
  useEffect(() => {
    if (tab === 'access' && !isOrgAdmin) setTab('config');
  }, [tab, isOrgAdmin]);

  const {
    data: collector,
    isLoading: collectorLoading,
    isError: collectorIsError,
    error: collectorError,
  } = useQuery({
    queryKey: ['collector', orgId, id],
    queryFn: () => clients.fleet.getCollector({ orgId, id }),
    enabled: !!orgId,
    refetchInterval: 15000,
  });

  const { data: servedConfig, isLoading: configLoading } = useQuery({
    queryKey: ['served-config', orgId, id],
    queryFn: () => clients.fleet.getServedConfig({ orgId, id }),
    enabled: !!orgId && tab === 'config',
    refetchInterval: 30000,
  });

  const qc = useQueryClient();
  const [groupQuery, setGroupQuery] = useState('');
  const [debouncedGroupQuery, setDebouncedGroupQuery] = useState('');
  const [pastedGroupId, setPastedGroupId] = useState('');
  const [pastedDisplayName, setPastedDisplayName] = useState('');

  // Debounce the search box so a burst of keystrokes fires one lookup, not
  // one per character.
  useEffect(() => {
    const t = setTimeout(() => setDebouncedGroupQuery(groupQuery), 300);
    return () => clearTimeout(t);
  }, [groupQuery]);

  const { data: assignments, isLoading: assignmentsLoading } = useQuery({
    queryKey: ['assignments', orgId, id],
    queryFn: () => clients.fleet.listAssignments({ orgId, collectorId: id }),
    enabled: !!orgId && tab === 'access' && isOrgAdmin,
  });

  const { data: groupResults, isFetching: searching } = useQuery({
    queryKey: ['group-search', orgId, debouncedGroupQuery],
    queryFn: () => clients.admin.searchGroups({ orgId, q: debouncedGroupQuery }),
    enabled: !!orgId && tab === 'access' && isOrgAdmin && debouncedGroupQuery.trim().length > 0,
  });

  const invalidateAssignments = () =>
    qc.invalidateQueries({ queryKey: ['assignments', orgId, id] });

  const addAssignment = useMutation({
    mutationFn: (vars: { groupId: string; groupDisplayName: string }) =>
      clients.fleet.createAssignment({
        orgId,
        collectorId: id,
        groupId: vars.groupId,
        groupDisplayName: vars.groupDisplayName,
      }),
    onSuccess: () => {
      toast.success('Group added');
      setPastedGroupId('');
      setPastedDisplayName('');
      setGroupQuery('');
      invalidateAssignments();
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to add group'),
  });

  const removeAssignment = useMutation({
    mutationFn: (groupId: string) =>
      clients.fleet.deleteAssignment({ orgId, collectorId: id, groupId }),
    onSuccess: () => {
      toast.success('Group removed');
      invalidateAssignments();
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to remove group'),
  });

  function addByPaste(e: React.FormEvent) {
    e.preventDefault();
    const groupId = pastedGroupId.trim();
    if (!groupId) return;
    addAssignment.mutate({ groupId, groupDisplayName: pastedDisplayName.trim() });
  }

  function copyHash() {
    if (!servedConfig?.hash) return;
    const showCopied = () => {
      setCopied(true);
      toast.success('Hash copied to clipboard');
      setTimeout(() => setCopied(false), 2000);
    };
    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(servedConfig.hash).then(showCopied).catch(showCopied);
    } else {
      showCopied();
    }
  }

  if (!orgId || collectorLoading) {
    return <div className='text-sm text-muted p-6'>Loading…</div>;
  }
  if (collectorIsError) {
    return (
      <div className='p-6'>
        <QueryError error={collectorError} noun='this collector' />
      </div>
    );
  }

  const detail = collector;
  const status = detail?.remoteConfigStatus?.toUpperCase() ?? '';
  const statusColor = STATUS_COLORS[status] ?? 'text-muted bg-border border-border-strong';
  const instances = detail?.instances ?? [];
  const latestOs = instances[0]?.os;
  const tabs: Tab[] = isOrgAdmin
    ? ['config', 'reconciliation', 'info', 'attributes', 'access']
    : ['config', 'reconciliation', 'info', 'attributes'];
  const tabLabels: Record<Tab, string> = {
    config: 'Served Config',
    reconciliation: 'Reconciliation',
    info: 'Info',
    attributes: 'Attributes & Labels',
    access: 'Access',
  };

  return (
    <div className='space-y-4'>
      <div className='flex items-start justify-between'>
        <div className='space-y-1'>
          <h1 className='text-xl font-semibold'>
            {detail?.cluster ?? id} / {detail?.role ?? '—'}
          </h1>
          <div className='flex items-center gap-3 text-xs text-muted-2'>
            {detail?.alloyVersion && <span>Alloy {detail.alloyVersion}</span>}
            {latestOs && <span>{latestOs}</span>}
            <span>Last seen {formatTimestampRelative(detail?.lastSeen)}</span>
          </div>
        </div>
        <div className='flex items-center gap-3'>
          <CollectorLabelsButton canEdit={isOrgAdmin} onClick={() => setTab('attributes')} />
          <span
            data-testid='collector-status'
            className={`text-xs font-medium px-2 py-0.5 rounded border ${statusColor}`}
          >
            {status || 'UNKNOWN'}
          </span>
        </div>
      </div>

      {detail?.remoteConfigError && (
        <div className='rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400'>
          <span className='font-medium'>Config error: </span>
          {detail.remoteConfigError}
        </div>
      )}

      <div className='flex border-b border-border'>
        {tabs.map((currentTab) => (
          <button
            key={currentTab}
            onClick={() => setTab(currentTab)}
            className={`px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors ${
              tab === currentTab
                ? 'border-indigo-500 text-indigo-400'
                : 'border-transparent text-muted hover:text-zinc-200'
            }`}
          >
            {tabLabels[currentTab]}
          </button>
        ))}
      </div>

      {tab === 'config' && (
        <div className='space-y-3'>
          {servedConfig?.hash && (
            <div className='flex items-center gap-2'>
              <span className='text-xs text-muted-2'>Hash:</span>
              <code className='text-xs font-mono text-zinc-300 bg-card px-2 py-0.5 rounded'>
                {servedConfig.hash.slice(0, 16)}…
              </code>
              <button
                onClick={copyHash}
                className='text-muted-2 hover:text-zinc-200 transition-colors'
                aria-label='Copy hash'
                data-testid='copy-hash-btn'
              >
                {copied ? (
                  <CheckCircle size={14} className='text-emerald-400' />
                ) : (
                  <Copy size={14} />
                )}
              </button>
              {servedConfig.computedAt && (
                <span className='text-xs text-muted-3'>
                  computed {formatTimestampRelative(servedConfig.computedAt)}
                </span>
              )}
            </div>
          )}

          {configLoading ? (
            <p className='text-sm text-muted'>Loading config…</p>
          ) : servedConfig?.content ? (
            <pre className='rounded-lg border border-border bg-background p-4 text-xs font-mono text-zinc-300 overflow-auto max-h-[60vh] whitespace-pre-wrap'>
              {servedConfig.content}
            </pre>
          ) : (
            <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
              <p className='text-sm text-muted-2'>No config served yet.</p>
              <p className='text-xs text-muted-3 mt-1'>
                Claim this cluster to an org and enable a matching pipeline.
              </p>
            </div>
          )}
        </div>
      )}

      {tab === 'reconciliation' && <CollectorReconciliation orgId={orgId} id={id} />}

      {tab === 'attributes' && detail && (
        <CollectorAttributes
          key={`${orgId}:${id}`}
          orgId={orgId}
          collector={detail}
          canEdit={isOrgAdmin}
        />
      )}

      {tab === 'info' && (
        <div className='space-y-6'>
          <dl className='space-y-3 text-sm'>
            {[
              ['Collector ID', id],
              ['Cluster', detail?.cluster],
              ['Role', detail?.role],
              ['Alloy version', detail?.alloyVersion || '—'],
              ['Last seen', formatTimestampRelative(detail?.lastSeen)],
              ['Status', status || '—'],
            ].map(([label, value]) => (
              <div key={label} className='flex gap-4'>
                <dt className='w-32 text-muted-2 shrink-0'>{label}</dt>
                <dd className='text-zinc-200 font-mono text-xs'>{value}</dd>
              </div>
            ))}
          </dl>

          <div className='space-y-2'>
            <h2 className='text-sm font-medium text-zinc-300'>Instances</h2>
            {instances.length === 0 ? (
              <p className='text-sm text-muted-2'>No instances have reported in yet.</p>
            ) : (
              <DataTable
                columns={instanceColumns}
                rows={instances}
                rowKey={(inst) => inst.name}
                rowClassName='border-t border-border'
                scrollX
              />
            )}
          </div>
        </div>
      )}

      {tab === 'access' && isOrgAdmin && (
        <div className='space-y-6'>
          <div className='space-y-3'>
            <h2 className='text-sm font-medium text-zinc-300'>Add a group</h2>

            <div className='relative max-w-md'>
              <Search
                size={14}
                className='pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted-3'
              />
              <input
                value={groupQuery}
                onChange={(e) => setGroupQuery(e.target.value)}
                placeholder='Search groups by name…'
                className='w-full rounded-md border border-border-strong bg-card py-1.5 pl-8 pr-3 text-sm'
                data-testid='group-search-input'
              />
              {groupQuery.trim() !== '' && (
                <div className='absolute z-10 mt-1 w-full rounded-md border border-border bg-background shadow-lg'>
                  {searching || debouncedGroupQuery.trim() !== groupQuery.trim() ? (
                    <p className='px-3 py-2 text-xs text-muted'>Searching…</p>
                  ) : (groupResults?.items ?? []).length === 0 ? (
                    <p className='px-3 py-2 text-xs text-muted-2'>
                      No groups found. Paste a group ID below instead.
                    </p>
                  ) : (
                    <ul>
                      {(groupResults?.items ?? []).map((g) => (
                        <li key={g.id}>
                          <button
                            type='button'
                            onClick={() =>
                              addAssignment.mutate({
                                groupId: g.id,
                                groupDisplayName: g.displayName,
                              })
                            }
                            disabled={addAssignment.isPending}
                            className='block w-full px-3 py-2 text-left text-sm hover:bg-card disabled:opacity-50'
                          >
                            {g.displayName}
                            <span className='ml-1.5 font-mono text-xs text-muted-3'>{g.id}</span>
                          </button>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
            </div>

            <form onSubmit={addByPaste} className='flex flex-wrap items-end gap-2 max-w-md'>
              <Field label='Group ID' className='flex-1 min-w-40'>
                <Input
                  value={pastedGroupId}
                  onChange={(e) => setPastedGroupId(e.target.value)}
                  placeholder='11111111-1111-1111-1111-111111111111'
                  mono
                  data-testid='group-id-input'
                />
              </Field>
              <Field label='Display name' optional className='flex-1 min-w-40'>
                <Input
                  value={pastedDisplayName}
                  onChange={(e) => setPastedDisplayName(e.target.value)}
                  placeholder='SRE Readers'
                />
              </Field>
              <button
                type='submit'
                disabled={!pastedGroupId.trim() || addAssignment.isPending}
                className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
                data-testid='add-assignment-btn'
              >
                <Plus size={14} /> Add
              </button>
            </form>
          </div>

          <div className='space-y-2'>
            <h2 className='text-sm font-medium text-zinc-300'>Groups with access</h2>
            {assignmentsLoading ? (
              <p className='text-sm text-muted'>Loading…</p>
            ) : (assignments?.items ?? []).length === 0 ? (
              <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
                <p className='text-sm text-muted-2'>No groups have access to this collector yet.</p>
              </div>
            ) : (
              <DataTable
                columns={assignmentColumns(
                  (a) => removeAssignment.mutate(a.groupId),
                  removeAssignment.isPending,
                )}
                rows={assignments?.items ?? []}
                rowKey={(a) => a.id}
                rowClassName='border-t border-border'
                scrollX
              />
            )}
          </div>
        </div>
      )}
    </div>
  );
}
