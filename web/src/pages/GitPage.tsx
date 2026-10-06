import { timestampDate } from '@bufbuild/protobuf/wkt';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { PlugZap, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { CredentialForm } from '@/components/git/CredentialForm';
import {
  type CredentialFormState,
  emptyCredentialForm,
  emptyLinkForm,
  KIND_LABELS,
  type RepoLinkFormState,
} from '@/components/git/gitForms';
import { RepoLinkForm } from '@/components/git/RepoLinkForm';
import { TestCredentialDialog } from '@/components/git/TestCredentialDialog';
import { QueryError } from '@/components/QueryError';
import { DataTable, type DataTableColumn } from '@/components/ui/DataTable';
import { toneClass } from '@/components/ui/statusTone';
import type {
  GitCredential,
  RepoLink,
  TestCredentialResponse,
} from '@/gen/shepherd/mgmt/v1/gitops_pb';
import { useOrgId } from '@/hooks/useOrg';

function credentialColumns(
  openTest: (c: GitCredential) => void,
  setDeleteCred: (c: GitCredential) => void,
): DataTableColumn<GitCredential>[] {
  return [
    {
      key: 'name',
      header: 'Name',
      cellClassName: 'px-4 py-2.5 font-medium',
      render: (c) => c.name,
    },
    {
      key: 'kind',
      header: 'Kind',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (c) => KIND_LABELS[c.kind as CredentialFormState['kind']] ?? c.kind,
    },
    {
      key: 'details',
      header: 'Details',
      cellClassName: 'px-4 py-2.5 font-mono text-xs text-muted',
      render: (c) => (c.kind === 'ado_sp' ? c.adoOrgUrl : c.username || '—'),
    },
    {
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (c) => (
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
      ),
    },
  ];
}

function repoLinkColumns(
  collectorLabel: (id: string) => string,
  credentialLabel: (id: string) => string,
  setDeleteLink: (l: RepoLink) => void,
): DataTableColumn<RepoLink>[] {
  return [
    {
      key: 'repo',
      header: 'Repository',
      cellClassName: 'px-4 py-2.5 font-mono text-xs',
      render: (rl) => {
        // The repo root ('/', '' or '.') is the default and says nothing; it
        // used to render as a stray " /" after the URL (#253). A subdirectory
        // gets its own labelled line instead of being glued onto the URL.
        const sub = rl.path === '.' ? '' : rl.path.replace(/^(\.?\/)+/, '').replace(/\/+$/, '');
        return (
          <>
            <span className='break-all'>{rl.repoUrl}</span>
            {sub && (
              <span className='block text-muted-2' data-testid='repo-link-path'>
                path: {sub}
              </span>
            )}
          </>
        );
      },
    },
    {
      key: 'branch',
      header: 'Branch',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (rl) => rl.branch,
    },
    {
      key: 'collector',
      header: 'Target collector',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (rl) => collectorLabel(rl.collectorId),
    },
    {
      key: 'credential',
      header: 'Credential',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (rl) => credentialLabel(rl.credentialId),
    },
    {
      key: 'status',
      header: 'Status',
      render: (rl) => (
        <span
          data-testid={rl.syncStatus === 'error' ? 'sync-status-error' : undefined}
          className={`text-xs px-1.5 py-0.5 rounded ${toneClass(
            rl.syncStatus === 'error' ? 'danger' : rl.syncStatus ? 'ok' : 'neutral',
          )}`}
        >
          {rl.syncStatus || 'pending'}
        </span>
      ),
    },
    {
      key: 'lastSynced',
      header: 'Last synced',
      cellClassName: 'px-4 py-2.5 text-muted text-xs',
      render: (rl) => (rl.lastSyncedAt ? timestampDate(rl.lastSyncedAt).toLocaleString() : '—'),
    },
    {
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right',
      render: (rl) => (
        <button
          type='button'
          onClick={() => setDeleteLink(rl)}
          className='text-muted-3 transition-colors hover:text-red-400'
          aria-label='Delete repository link'
        >
          <Trash2 size={14} />
        </button>
      ),
    },
  ];
}

export function GitPage() {
  const orgId = useOrgId();
  const qc = useQueryClient();

  const [showCreateCred, setShowCreateCred] = useState(false);
  const [credForm, setCredForm] = useState<CredentialFormState>(emptyCredentialForm);
  const [deleteCred, setDeleteCred] = useState<GitCredential | null>(null);
  const [testCred, setTestCred] = useState<GitCredential | null>(null);
  const [testForm, setTestForm] = useState({ repoUrl: '', branch: '' });
  const [testResult, setTestResult] = useState<TestCredentialResponse | null>(null);

  const [showCreateLink, setShowCreateLink] = useState(false);
  const [linkForm, setLinkForm] = useState<RepoLinkFormState>(emptyLinkForm);
  const [deleteLink, setDeleteLink] = useState<RepoLink | null>(null);

  const {
    data: credData,
    isLoading: credLoading,
    isError: credIsError,
    error: credError,
  } = useQuery({
    queryKey: ['git-credentials', orgId],
    queryFn: () => clients.gitOps.listCredentials({ orgId }),
    enabled: !!orgId,
  });
  const {
    data: linkData,
    isLoading: linkLoading,
    isError: linkIsError,
    error: linkError,
  } = useQuery({
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

        {credIsError ? (
          <QueryError error={credError} noun='git credentials' testId='git-credentials-error' />
        ) : credLoading ? (
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
          <DataTable
            columns={credentialColumns(openTest, setDeleteCred)}
            rows={credentials}
            rowKey={(c) => c.id}
          />
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

        {linkIsError ? (
          <QueryError error={linkError} noun='repository links' testId='git-repo-links-error' />
        ) : linkLoading ? (
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
          <DataTable
            columns={repoLinkColumns(collectorLabel, credentialLabel, setDeleteLink)}
            rows={repoLinks}
            rowKey={(rl) => rl.id}
          />
        )}
      </section>

      {showCreateCred && (
        <CredentialForm
          value={credForm}
          onChange={setCredForm}
          onSubmit={() => createCredMut.mutate()}
          pending={createCredMut.isPending}
          onCancel={() => setShowCreateCred(false)}
        />
      )}

      {testCred && (
        <TestCredentialDialog
          credential={testCred}
          value={testForm}
          onChange={setTestForm}
          result={testResult}
          onSubmit={() => {
            setTestResult(null);
            testCredMut.mutate();
          }}
          pending={testCredMut.isPending}
          onClose={() => {
            setTestCred(null);
            setTestResult(null);
          }}
        />
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

      {showCreateLink && (
        <RepoLinkForm
          value={linkForm}
          onChange={setLinkForm}
          onSubmit={() => createLinkMut.mutate()}
          pending={createLinkMut.isPending}
          onCancel={() => setShowCreateLink(false)}
          collectors={collectors}
          credentials={credentials}
        />
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
