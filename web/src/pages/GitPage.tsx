import { timestampDate } from '@bufbuild/protobuf/wkt';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { PlugZap, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { AdminModal, AdminModalActions } from '@/components/admin/AdminModal';
import type {
  GitCredential,
  RepoLink,
  TestCredentialResponse,
} from '@/gen/shepherd/mgmt/v1/gitops_pb';
import { useOrgId } from '@/hooks/useOrg';

// CREDENTIAL_KINDS mirrors docs/git-provider-design.md §3.2's six auth
// strategies. Order matters here: it's also the <select> option order.
const CREDENTIAL_KINDS = ['pat', 'basic', 'ssh', 'ado_sp', 'github_app', 'none'] as const;
type CredentialKind = (typeof CREDENTIAL_KINDS)[number];

const KIND_LABELS: Record<CredentialKind, string> = {
  pat: 'Personal access token',
  basic: 'Basic auth (username + password)',
  ssh: 'SSH deploy key',
  ado_sp: 'Azure DevOps service principal',
  github_app: 'GitHub App',
  none: 'None (public repository)',
};

const inputClass =
  'mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm font-mono';
const textInputClass =
  'mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm';
const labelClass = 'block text-xs font-medium text-muted';

type CredentialForm = {
  name: string;
  kind: CredentialKind;
  username: string;
  adoOrgUrl: string;
  entraTenantId: string;
  clientId: string;
  appId: string;
  installationId: string;
  apiBaseUrl: string;
  clientSecret: string;
  secret2: string;
  sshKnownHosts: string;
  caCert: string;
  tlsInsecureSkipVerify: boolean;
};

const emptyCredentialForm: CredentialForm = {
  name: '',
  kind: 'pat',
  username: '',
  adoOrgUrl: '',
  entraTenantId: '',
  clientId: '',
  appId: '',
  installationId: '',
  apiBaseUrl: '',
  clientSecret: '',
  secret2: '',
  sshKnownHosts: '',
  caCert: '',
  tlsInsecureSkipVerify: false,
};

type RepoLinkForm = {
  repoUrl: string;
  branch: string;
  path: string;
  collectorId: string;
  credentialId: string;
};

const emptyLinkForm: RepoLinkForm = {
  repoUrl: '',
  branch: 'main',
  path: '/',
  collectorId: '',
  credentialId: '',
};

export function GitPage() {
  const orgId = useOrgId();
  const qc = useQueryClient();

  const [showCreateCred, setShowCreateCred] = useState(false);
  const [credForm, setCredForm] = useState<CredentialForm>(emptyCredentialForm);
  const [deleteCred, setDeleteCred] = useState<GitCredential | null>(null);
  const [testCred, setTestCred] = useState<GitCredential | null>(null);
  const [testForm, setTestForm] = useState({ repoUrl: '', branch: '' });
  const [testResult, setTestResult] = useState<TestCredentialResponse | null>(null);

  const [showCreateLink, setShowCreateLink] = useState(false);
  const [linkForm, setLinkForm] = useState<RepoLinkForm>(emptyLinkForm);
  const [deleteLink, setDeleteLink] = useState<RepoLink | null>(null);

  const { data: credData, isLoading: credLoading } = useQuery({
    queryKey: ['git-credentials', orgId],
    queryFn: () => clients.gitOps.listCredentials({ orgId }),
    enabled: !!orgId,
  });
  const { data: linkData, isLoading: linkLoading } = useQuery({
    queryKey: ['repo-links', orgId],
    queryFn: () => clients.gitOps.listRepoLinks({ orgId }),
    enabled: !!orgId,
  });
  const { data: collectorData } = useQuery({
    queryKey: ['collectors', orgId],
    queryFn: () => clients.fleet.listCollectors({ orgId }),
    enabled: !!orgId,
  });

  const credentials = credData?.items ?? [];
  const repoLinks = linkData?.items ?? [];
  const collectors = collectorData?.items ?? [];

  const invalidateCreds = () => qc.invalidateQueries({ queryKey: ['git-credentials', orgId] });
  const invalidateLinks = () => qc.invalidateQueries({ queryKey: ['repo-links', orgId] });

  const createCredMut = useMutation({
    mutationFn: () => {
      const f = credForm;
      const providerConfig =
        f.kind === 'github_app'
          ? {
              app_id: f.appId,
              installation_id: f.installationId,
              ...(f.apiBaseUrl ? { api_base_url: f.apiBaseUrl } : {}),
            }
          : undefined;
      return clients.gitOps.createCredential({
        orgId,
        name: f.name,
        kind: f.kind,
        username: f.username,
        adoOrgUrl: f.adoOrgUrl,
        entraTenantId: f.entraTenantId,
        clientId: f.clientId,
        providerConfig,
        clientSecret: f.clientSecret,
        secret2: f.secret2,
        sshKnownHosts: f.sshKnownHosts,
        caCert: f.caCert,
        tlsInsecureSkipVerify: f.tlsInsecureSkipVerify,
      });
    },
    onSuccess: () => {
      toast.success('Credential created');
      invalidateCreds();
      setShowCreateCred(false);
      setCredForm(emptyCredentialForm);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to create credential'),
  });

  const deleteCredMut = useMutation({
    mutationFn: (id: string) => clients.gitOps.deleteCredential({ orgId, id }),
    onSuccess: () => {
      toast.success('Credential deleted');
      invalidateCreds();
      setDeleteCred(null);
    },
    onError: (e) => {
      toast.error(toApiError(e).message || 'Failed to delete credential');
      setDeleteCred(null);
    },
  });

  const testCredMut = useMutation({
    mutationFn: () =>
      clients.gitOps.testCredential({
        orgId,
        id: testCred?.id ?? '',
        repoUrl: testForm.repoUrl,
        branch: testForm.branch,
      }),
    onSuccess: (res) => setTestResult(res),
    onError: (e) => toast.error(toApiError(e).message || 'Test failed to run'),
  });

  const createLinkMut = useMutation({
    mutationFn: () =>
      clients.gitOps.createRepoLink({
        orgId,
        collectorId: linkForm.collectorId,
        credentialId: linkForm.credentialId,
        repoUrl: linkForm.repoUrl,
        branch: linkForm.branch,
        path: linkForm.path,
      }),
    onSuccess: () => {
      toast.success('Repository link created');
      invalidateLinks();
      setShowCreateLink(false);
      setLinkForm(emptyLinkForm);
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to create repository link'),
  });

  const deleteLinkMut = useMutation({
    mutationFn: (id: string) => clients.gitOps.deleteRepoLink({ orgId, id }),
    onSuccess: () => {
      toast.success('Repository link deleted');
      invalidateLinks();
      setDeleteLink(null);
    },
    onError: (e) => {
      toast.error(toApiError(e).message || 'Failed to delete repository link');
      setDeleteLink(null);
    },
  });

  function openTest(c: GitCredential) {
    setTestCred(c);
    setTestForm({ repoUrl: '', branch: '' });
    setTestResult(null);
  }

  function collectorLabel(id: string): string {
    const c = collectors.find((x) => x.id === id);
    return c ? `${c.cluster} / ${c.role}` : id;
  }

  function credentialLabel(id: string): string {
    const c = credentials.find((x) => x.id === id);
    return c ? c.name : id;
  }

  if (!orgId) {
    return (
      <div className='space-y-4'>
        <h1 className='text-xl font-semibold'>Git sync</h1>
        <p className='text-sm text-muted'>No organisation context.</p>
      </div>
    );
  }

  return (
    <div className='space-y-8'>
      <div>
        <h1 className='text-xl font-semibold'>Git sync</h1>
        <p className='mt-1 text-sm text-muted'>
          Connect a git repository to sync pipelines onto a collector automatically.
        </p>
      </div>

      {/* Credentials */}
      <section className='space-y-3'>
        <div className='flex items-center justify-between'>
          <h2 className='text-sm font-semibold text-zinc-200'>Credentials</h2>
          <button
            type='button'
            onClick={() => setShowCreateCred(true)}
            className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500'
          >
            <Plus size={14} /> New credential
          </button>
        </div>

        {credLoading ? (
          <p className='text-sm text-muted'>Loading…</p>
        ) : credentials.length === 0 ? (
          <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
            <p className='text-sm text-muted'>No credentials configured.</p>
            <button
              type='button'
              onClick={() => setShowCreateCred(true)}
              className='mt-3 text-xs text-indigo-400 hover:text-indigo-300'
            >
              Add your first credential
            </button>
          </div>
        ) : (
          <div className='rounded-lg border border-border overflow-hidden'>
            <table className='w-full text-sm'>
              <thead className='bg-card text-muted'>
                <tr>
                  <th className='px-4 py-3 text-left font-medium'>Name</th>
                  <th className='px-4 py-3 text-left font-medium'>Kind</th>
                  <th className='px-4 py-3 text-left font-medium'>Details</th>
                  <th className='px-4 py-3' />
                </tr>
              </thead>
              <tbody>
                {credentials.map((c) => (
                  <tr key={c.id} className='border-t border-border hover:bg-card/60'>
                    <td className='px-4 py-2.5 font-medium'>{c.name}</td>
                    <td className='px-4 py-2.5 text-muted'>
                      {KIND_LABELS[c.kind as CredentialKind] ?? c.kind}
                    </td>
                    <td className='px-4 py-2.5 font-mono text-xs text-muted'>
                      {c.kind === 'ado_sp' ? c.adoOrgUrl : c.username || '—'}
                    </td>
                    <td className='px-4 py-2.5 text-right'>
                      <div className='flex justify-end gap-3'>
                        <button
                          type='button'
                          onClick={() => openTest(c)}
                          className='text-muted-3 transition-colors hover:text-indigo-400'
                          aria-label={`Test ${c.name}`}
                          title='Test connectivity'
                        >
                          <PlugZap size={14} />
                        </button>
                        <button
                          type='button'
                          onClick={() => setDeleteCred(c)}
                          className='text-muted-3 transition-colors hover:text-red-400'
                          aria-label={`Delete ${c.name}`}
                        >
                          <Trash2 size={14} />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* Repository links */}
      <section className='space-y-3'>
        <div className='flex items-center justify-between'>
          <h2 className='text-sm font-semibold text-zinc-200'>Repository links</h2>
          <button
            type='button'
            onClick={() => setShowCreateLink(true)}
            disabled={credentials.length === 0}
            title={credentials.length === 0 ? 'Add a credential first' : undefined}
            className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:cursor-not-allowed disabled:opacity-50'
          >
            <Plus size={14} /> New repository link
          </button>
        </div>

        {linkLoading ? (
          <p className='text-sm text-muted'>Loading…</p>
        ) : repoLinks.length === 0 ? (
          <div className='rounded-lg border border-border bg-card/40 p-8 text-center'>
            <p className='text-sm text-muted'>No repository links configured.</p>
            <p className='mt-1 text-xs text-muted-3'>
              {credentials.length === 0
                ? 'Add a credential above, then link a repository to a collector.'
                : 'Link a repository to start syncing pipelines onto a collector.'}
            </p>
          </div>
        ) : (
          <div className='rounded-lg border border-border overflow-hidden'>
            <table className='w-full text-sm'>
              <thead className='bg-card text-muted'>
                <tr>
                  <th className='px-4 py-3 text-left font-medium'>Repository</th>
                  <th className='px-4 py-3 text-left font-medium'>Branch</th>
                  <th className='px-4 py-3 text-left font-medium'>Target collector</th>
                  <th className='px-4 py-3 text-left font-medium'>Credential</th>
                  <th className='px-4 py-3 text-left font-medium'>Status</th>
                  <th className='px-4 py-3 text-left font-medium'>Last synced</th>
                  <th className='px-4 py-3' />
                </tr>
              </thead>
              <tbody>
                {repoLinks.map((rl) => (
                  <tr key={rl.id} className='border-t border-border hover:bg-card/60'>
                    <td className='px-4 py-2.5 font-mono text-xs'>
                      {rl.repoUrl}
                      <span className='ml-1 text-muted-3'>{rl.path}</span>
                    </td>
                    <td className='px-4 py-2.5 text-muted'>{rl.branch}</td>
                    <td className='px-4 py-2.5 text-muted'>{collectorLabel(rl.collectorId)}</td>
                    <td className='px-4 py-2.5 text-muted'>{credentialLabel(rl.credentialId)}</td>
                    <td className='px-4 py-2.5'>
                      <span
                        data-testid={rl.syncStatus === 'error' ? 'sync-status-error' : undefined}
                        className={`text-xs px-1.5 py-0.5 rounded ${
                          rl.syncStatus === 'error'
                            ? 'text-red-400 bg-red-400/10'
                            : 'text-emerald-400 bg-emerald-400/10'
                        }`}
                      >
                        {rl.syncStatus || 'pending'}
                      </span>
                    </td>
                    <td className='px-4 py-2.5 text-muted text-xs'>
                      {rl.lastSyncedAt ? timestampDate(rl.lastSyncedAt).toLocaleString() : '—'}
                    </td>
                    <td className='px-4 py-2.5 text-right'>
                      <button
                        type='button'
                        onClick={() => setDeleteLink(rl)}
                        className='text-muted-3 transition-colors hover:text-red-400'
                        aria-label='Delete repository link'
                      >
                        <Trash2 size={14} />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* Create credential */}
      {showCreateCred && (
        <AdminModal title='New credential' onClose={() => setShowCreateCred(false)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createCredMut.mutate();
            }}
            className='space-y-4 max-h-[70vh] overflow-y-auto pr-1'
          >
            <label className={labelClass}>
              Name
              <input
                value={credForm.name}
                onChange={(e) => setCredForm((f) => ({ ...f, name: e.target.value }))}
                required
                className={textInputClass}
                placeholder='prod-gitea'
              />
            </label>
            <label className={labelClass}>
              Kind
              <select
                value={credForm.kind}
                onChange={(e) =>
                  setCredForm((f) => ({ ...f, kind: e.target.value as CredentialKind }))
                }
                className={textInputClass}
              >
                {CREDENTIAL_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {KIND_LABELS[k]}
                  </option>
                ))}
              </select>
            </label>

            {(credForm.kind === 'basic' || credForm.kind === 'pat' || credForm.kind === 'ssh') && (
              <label className={labelClass}>
                Username{' '}
                {credForm.kind === 'ssh' && <span className='text-muted-3'>(default "git")</span>}
                <input
                  value={credForm.username}
                  onChange={(e) => setCredForm((f) => ({ ...f, username: e.target.value }))}
                  className={inputClass}
                  placeholder={
                    credForm.kind === 'pat' ? 'oauth2' : credForm.kind === 'ssh' ? 'git' : ''
                  }
                />
              </label>
            )}

            {credForm.kind === 'ado_sp' && (
              <>
                <label className={labelClass}>
                  Azure DevOps org URL
                  <input
                    value={credForm.adoOrgUrl}
                    onChange={(e) => setCredForm((f) => ({ ...f, adoOrgUrl: e.target.value }))}
                    required
                    className={inputClass}
                    placeholder='https://dev.azure.com/acme'
                  />
                </label>
                <label className={labelClass}>
                  Entra tenant ID
                  <input
                    value={credForm.entraTenantId}
                    onChange={(e) => setCredForm((f) => ({ ...f, entraTenantId: e.target.value }))}
                    required
                    className={inputClass}
                  />
                </label>
                <label className={labelClass}>
                  Client ID
                  <input
                    value={credForm.clientId}
                    onChange={(e) => setCredForm((f) => ({ ...f, clientId: e.target.value }))}
                    required
                    className={inputClass}
                  />
                </label>
              </>
            )}

            {credForm.kind === 'github_app' && (
              <>
                <label className={labelClass}>
                  App ID
                  <input
                    value={credForm.appId}
                    onChange={(e) => setCredForm((f) => ({ ...f, appId: e.target.value }))}
                    required
                    className={inputClass}
                  />
                </label>
                <label className={labelClass}>
                  Installation ID
                  <input
                    value={credForm.installationId}
                    onChange={(e) => setCredForm((f) => ({ ...f, installationId: e.target.value }))}
                    required
                    className={inputClass}
                  />
                </label>
                <label className={labelClass}>
                  API base URL <span className='text-muted-3'>(GitHub Enterprise Server only)</span>
                  <input
                    value={credForm.apiBaseUrl}
                    onChange={(e) => setCredForm((f) => ({ ...f, apiBaseUrl: e.target.value }))}
                    className={inputClass}
                    placeholder='https://api.github.com'
                  />
                </label>
              </>
            )}

            {credForm.kind === 'ssh' && (
              <label className={labelClass}>
                Known hosts{' '}
                <span className='text-muted-3'>(required — no accept-any-host-key mode)</span>
                <textarea
                  value={credForm.sshKnownHosts}
                  onChange={(e) => setCredForm((f) => ({ ...f, sshKnownHosts: e.target.value }))}
                  required
                  rows={2}
                  className={inputClass}
                  placeholder='gitea.internal ssh-ed25519 AAAA...'
                />
              </label>
            )}

            {credForm.kind !== 'none' && (
              <label className={labelClass}>
                {credForm.kind === 'ssh'
                  ? 'Private key (PEM)'
                  : credForm.kind === 'github_app'
                    ? 'Private key (PEM)'
                    : credForm.kind === 'ado_sp'
                      ? 'Client secret'
                      : credForm.kind === 'pat'
                        ? 'Token'
                        : 'Password'}
                <textarea
                  value={credForm.clientSecret}
                  onChange={(e) => setCredForm((f) => ({ ...f, clientSecret: e.target.value }))}
                  required
                  rows={credForm.kind === 'ssh' || credForm.kind === 'github_app' ? 4 : 1}
                  className={inputClass}
                />
              </label>
            )}

            {credForm.kind === 'ssh' && (
              <label className={labelClass}>
                Private key passphrase <span className='text-muted-3'>(optional)</span>
                <input
                  type='password'
                  value={credForm.secret2}
                  onChange={(e) => setCredForm((f) => ({ ...f, secret2: e.target.value }))}
                  className={inputClass}
                />
              </label>
            )}

            {credForm.kind !== 'ssh' && credForm.kind !== 'none' && (
              <details className='rounded-md border border-border p-3'>
                <summary className='cursor-pointer text-xs font-medium text-muted'>
                  Advanced: private CA / TLS
                </summary>
                <div className='mt-3 space-y-3'>
                  <label className={labelClass}>
                    CA bundle (PEM) <span className='text-muted-3'>(optional)</span>
                    <textarea
                      value={credForm.caCert}
                      onChange={(e) => setCredForm((f) => ({ ...f, caCert: e.target.value }))}
                      rows={3}
                      className={inputClass}
                    />
                  </label>
                  <label className='flex items-center gap-2 text-xs text-red-400'>
                    <input
                      type='checkbox'
                      checked={credForm.tlsInsecureSkipVerify}
                      onChange={(e) =>
                        setCredForm((f) => ({ ...f, tlsInsecureSkipVerify: e.target.checked }))
                      }
                    />
                    Skip TLS certificate verification (unsafe)
                  </label>
                </div>
              </details>
            )}

            <AdminModalActions
              onCancel={() => setShowCreateCred(false)}
              submitLabel='Create'
              pendingLabel='Creating…'
              pending={createCredMut.isPending}
            />
          </form>
        </AdminModal>
      )}

      {/* Test credential */}
      {testCred && (
        <AdminModal
          title={`Test "${testCred.name}"`}
          onClose={() => {
            setTestCred(null);
            setTestResult(null);
          }}
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setTestResult(null);
              testCredMut.mutate();
            }}
            className='space-y-4'
          >
            <label className={labelClass}>
              Repository URL
              <input
                value={testForm.repoUrl}
                onChange={(e) => setTestForm((f) => ({ ...f, repoUrl: e.target.value }))}
                required
                className={inputClass}
                placeholder='https://gitea.internal/team/configs.git'
              />
            </label>
            <label className={labelClass}>
              Branch <span className='text-muted-3'>(default "main")</span>
              <input
                value={testForm.branch}
                onChange={(e) => setTestForm((f) => ({ ...f, branch: e.target.value }))}
                className={inputClass}
                placeholder='main'
              />
            </label>

            {testResult && (
              <div
                data-testid='test-credential-result'
                className={`rounded-md border p-3 text-xs ${
                  testResult.reachable
                    ? 'border-emerald-400/30 bg-emerald-400/10 text-emerald-300'
                    : 'border-red-400/30 bg-red-400/10 text-red-300'
                }`}
              >
                <p className='font-medium'>
                  {testResult.reachable ? 'Reachable' : 'Unreachable'}
                  {testResult.tokenExchangeRequired && (
                    <span className='ml-2 font-normal text-muted-3'>
                      · token exchange {testResult.tokenExchangeOk ? 'succeeded' : 'failed'}
                    </span>
                  )}
                </p>
                {testResult.error && (
                  <p className='mt-1 font-mono text-muted'>{testResult.error}</p>
                )}
              </div>
            )}

            <AdminModalActions
              onCancel={() => {
                setTestCred(null);
                setTestResult(null);
              }}
              submitLabel='Run test'
              pendingLabel='Testing…'
              pending={testCredMut.isPending}
            />
          </form>
        </AdminModal>
      )}

      {deleteCred && (
        <AdminConfirmDialog
          title={`Delete "${deleteCred.name}"?`}
          body='Repository links using this credential will fail to sync until they are pointed at a different credential.'
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={deleteCredMut.isPending}
          onConfirm={() => deleteCredMut.mutate(deleteCred.id)}
          onCancel={() => setDeleteCred(null)}
        />
      )}

      {/* Create repo link */}
      {showCreateLink && (
        <AdminModal title='New repository link' onClose={() => setShowCreateLink(false)}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              createLinkMut.mutate();
            }}
            className='space-y-4'
          >
            <label className={labelClass}>
              Clone URL
              <input
                value={linkForm.repoUrl}
                onChange={(e) => setLinkForm((f) => ({ ...f, repoUrl: e.target.value }))}
                required
                className={inputClass}
                placeholder='https://gitea.internal/team/configs.git'
              />
            </label>
            <label className={labelClass}>
              Branch
              <input
                value={linkForm.branch}
                onChange={(e) => setLinkForm((f) => ({ ...f, branch: e.target.value }))}
                className={inputClass}
                placeholder='main'
              />
            </label>
            <label className={labelClass}>
              Path <span className='text-muted-3'>(subdirectory to scan for *.alloy files)</span>
              <input
                value={linkForm.path}
                onChange={(e) => setLinkForm((f) => ({ ...f, path: e.target.value }))}
                className={inputClass}
                placeholder='/'
              />
            </label>
            <label className={labelClass}>
              Target collector
              <select
                value={linkForm.collectorId}
                onChange={(e) => setLinkForm((f) => ({ ...f, collectorId: e.target.value }))}
                required
                className={textInputClass}
              >
                <option value='' disabled>
                  Select a collector…
                </option>
                {collectors.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.cluster} / {c.role}
                  </option>
                ))}
              </select>
            </label>
            <label className={labelClass}>
              Credential
              <select
                value={linkForm.credentialId}
                onChange={(e) => setLinkForm((f) => ({ ...f, credentialId: e.target.value }))}
                required
                className={textInputClass}
              >
                <option value='' disabled>
                  Select a credential…
                </option>
                {credentials.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} ({KIND_LABELS[c.kind as CredentialKind] ?? c.kind})
                  </option>
                ))}
              </select>
            </label>
            <AdminModalActions
              onCancel={() => setShowCreateLink(false)}
              submitLabel='Create'
              pendingLabel='Creating…'
              pending={createLinkMut.isPending}
            />
          </form>
        </AdminModal>
      )}

      {deleteLink && (
        <AdminConfirmDialog
          title='Delete repository link?'
          body='The collector this link targets will stop receiving updates from this repository. Pipelines already synced from it are left as-is.'
          confirmLabel='Delete'
          pendingLabel='Deleting…'
          pending={deleteLinkMut.isPending}
          onConfirm={() => deleteLinkMut.mutate(deleteLink.id)}
          onCancel={() => setDeleteLink(null)}
        />
      )}
    </div>
  );
}
