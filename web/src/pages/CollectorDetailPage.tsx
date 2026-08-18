import { useQuery } from '@tanstack/react-query';
import { useParams } from '@tanstack/react-router';
import { CheckCircle, Copy } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { orgApi } from '@/api/client';
import { useOrgId } from '@/hooks/useOrg';

function relativeTime(ts: string | undefined): string {
  if (!ts) return 'unknown';
  const diff = Date.now() - new Date(ts).getTime();
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return 'just now';
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

const STATUS_COLORS: Record<string, string> = {
  APPLIED: 'text-emerald-400 bg-emerald-400/10 border-emerald-400/20',
  APPLYING: 'text-yellow-400 bg-yellow-400/10 border-yellow-400/20',
  FAILED: 'text-red-400 bg-red-400/10 border-red-400/20',
};

export function CollectorDetailPage() {
  const { id } = useParams({ from: '/shell/content/collectors/$id' });
  const orgId = useOrgId();
  const [tab, setTab] = useState<'config' | 'info'>('config');
  const [copied, setCopied] = useState(false);

  const { data: collector, isLoading: collectorLoading } = useQuery({
    queryKey: ['collector', orgId, id],
    queryFn: () => orgApi.getCollector(orgId, id),
    enabled: !!orgId,
    refetchInterval: 15000,
  });

  const { data: servedConfig, isLoading: configLoading } = useQuery({
    queryKey: ['served-config', orgId, id],
    queryFn: () => orgApi.getServedConfig(orgId, id),
    enabled: !!orgId && tab === 'config',
    refetchInterval: 30000,
  });

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
    return <div className='text-sm text-zinc-400 p-6'>Loading…</div>;
  }

  const detail = collector;
  const status = detail?.remote_config_status?.toUpperCase() ?? '';
  const statusColor = STATUS_COLORS[status] ?? 'text-zinc-400 bg-zinc-800 border-zinc-700';
  const instances = detail?.instances ?? [];
  const latestOs = instances[0]?.os;

  return (
    <div className='space-y-4'>
      <div className='flex items-start justify-between'>
        <div className='space-y-1'>
          <h1 className='text-xl font-semibold'>
            {detail?.cluster ?? id} / {detail?.role ?? '—'}
          </h1>
          <div className='flex items-center gap-3 text-xs text-zinc-500'>
            {detail?.alloy_version && <span>Alloy {detail.alloy_version}</span>}
            {latestOs && <span>{latestOs}</span>}
            <span>Last seen {relativeTime(detail?.last_seen)}</span>
          </div>
        </div>
        <span className={`text-xs font-medium px-2 py-0.5 rounded border ${statusColor}`}>
          {status || 'UNKNOWN'}
        </span>
      </div>

      {detail?.remote_config_error && (
        <div className='rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400'>
          <span className='font-medium'>Config error: </span>
          {detail.remote_config_error}
        </div>
      )}

      <div className='flex border-b border-zinc-800'>
        {(['config', 'info'] as const).map((currentTab) => (
          <button
            key={currentTab}
            onClick={() => setTab(currentTab)}
            className={`px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors ${
              tab === currentTab
                ? 'border-indigo-500 text-indigo-400'
                : 'border-transparent text-zinc-400 hover:text-zinc-200'
            }`}
          >
            {currentTab === 'config' ? 'Served Config' : 'Info'}
          </button>
        ))}
      </div>

      {tab === 'config' && (
        <div className='space-y-3'>
          {servedConfig?.hash && (
            <div className='flex items-center gap-2'>
              <span className='text-xs text-zinc-500'>Hash:</span>
              <code className='text-xs font-mono text-zinc-300 bg-zinc-900 px-2 py-0.5 rounded'>
                {servedConfig.hash.slice(0, 16)}…
              </code>
              <button
                onClick={copyHash}
                className='text-zinc-500 hover:text-zinc-200 transition-colors'
                aria-label='Copy hash'
                data-testid='copy-hash-btn'
              >
                {copied ? (
                  <CheckCircle size={14} className='text-emerald-400' />
                ) : (
                  <Copy size={14} />
                )}
              </button>
              {servedConfig.computed_at && (
                <span className='text-xs text-zinc-600'>
                  computed {relativeTime(servedConfig.computed_at)}
                </span>
              )}
            </div>
          )}

          {configLoading ? (
            <p className='text-sm text-zinc-400'>Loading config…</p>
          ) : servedConfig?.content ? (
            <pre className='rounded-lg border border-zinc-800 bg-zinc-950 p-4 text-xs font-mono text-zinc-300 overflow-auto max-h-[60vh] whitespace-pre-wrap'>
              {servedConfig.content}
            </pre>
          ) : (
            <div className='rounded-lg border border-zinc-800 bg-zinc-900/40 p-8 text-center'>
              <p className='text-sm text-zinc-500'>No config served yet.</p>
              <p className='text-xs text-zinc-600 mt-1'>
                Claim this cluster to an org and enable a matching pipeline.
              </p>
            </div>
          )}
        </div>
      )}

      {tab === 'info' && (
        <div className='space-y-6'>
          <dl className='space-y-3 text-sm'>
            {[
              ['Collector ID', id],
              ['Cluster', detail?.cluster],
              ['Role', detail?.role],
              ['Alloy version', detail?.alloy_version ?? '—'],
              ['Last seen', relativeTime(detail?.last_seen)],
              ['Status', status || '—'],
            ].map(([label, value]) => (
              <div key={label} className='flex gap-4'>
                <dt className='w-32 text-zinc-500 shrink-0'>{label}</dt>
                <dd className='text-zinc-200 font-mono text-xs'>{value}</dd>
              </div>
            ))}
          </dl>

          <div className='space-y-2'>
            <h2 className='text-sm font-medium text-zinc-300'>Instances</h2>
            {instances.length === 0 ? (
              <p className='text-sm text-zinc-500'>No instances have reported in yet.</p>
            ) : (
              <div className='rounded-lg border border-zinc-800 overflow-hidden overflow-x-auto'>
                <table className='w-full text-sm'>
                  <thead className='bg-zinc-900 text-zinc-400'>
                    <tr>
                      <th className='px-4 py-2 text-left font-medium'>Name</th>
                      <th className='px-4 py-2 text-left font-medium'>Version</th>
                      <th className='px-4 py-2 text-left font-medium'>OS</th>
                      <th className='px-4 py-2 text-left font-medium'>Last seen</th>
                      <th className='px-4 py-2 text-left font-medium'>Status</th>
                      <th className='px-4 py-2 text-left font-medium'>Error</th>
                    </tr>
                  </thead>
                  <tbody>
                    {instances.map((inst) => {
                      const instStatus = inst.remote_config_status?.toUpperCase() ?? '';
                      const instColor =
                        STATUS_COLORS[instStatus] ?? 'text-zinc-400 bg-zinc-800 border-zinc-700';
                      return (
                        <tr key={inst.name} className='border-t border-zinc-800'>
                          <td className='px-4 py-2.5 font-mono text-xs'>{inst.name}</td>
                          <td className='px-4 py-2.5 text-zinc-400'>{inst.alloy_version ?? '—'}</td>
                          <td className='px-4 py-2.5 text-zinc-400'>{inst.os ?? '—'}</td>
                          <td className='px-4 py-2.5 text-zinc-400'>
                            {relativeTime(inst.last_seen)}
                          </td>
                          <td className='px-4 py-2.5'>
                            <span
                              className={`text-xs font-medium px-2 py-0.5 rounded border ${instColor}`}
                            >
                              {instStatus || 'UNKNOWN'}
                            </span>
                          </td>
                          <td className='px-4 py-2.5 text-red-400 text-xs'>
                            {inst.remote_config_error ?? '—'}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
