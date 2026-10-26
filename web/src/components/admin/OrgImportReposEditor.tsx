import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Trash2 } from 'lucide-react';
import { type KeyboardEvent, useState } from 'react';
import { clients } from '@/api/transport';
import { Field, Input, Select } from '@/components/ui/Field';
import type { Org, OrgImportRepository } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { errorText } from '@/lib/formError';
import { importRepositoryCredential, importRepositoryError } from '@/lib/importRepos';

/**
 * Admin → Organisations → Edit: the repositories the org's pipelines may
 * import.git from in strict tenant mode, pinned to a full commit SHA.
 *
 * Like the tenants editor, each change is its own app-admin RPC
 * (AddOrgImportRepository, RemoveOrgImportRepository) and takes effect at
 * once, independent of the dialog's Save. The server refuses an ambiguous
 * URL, a second spelling of an entry, and a removal while an enabled
 * pipeline imports from it; its message is shown here.
 */
export function OrgImportReposEditor({ org }: { org: Org }) {
  const qc = useQueryClient();
  const key = ['admin', 'orgImportRepositories', org.id];
  const [repository, setRepository] = useState('');
  const [credentialId, setCredentialId] = useState('');
  const [localError, setLocalError] = useState('');

  const listQ = useQuery({
    queryKey: key,
    queryFn: () => clients.admin.listOrgImportRepositories({ orgId: org.id }),
  });
  const credsQ = useQuery({
    queryKey: ['git-credentials', org.id],
    queryFn: () => clients.gitOps.listCredentials({ orgId: org.id }),
  });

  const refresh = () => qc.invalidateQueries({ queryKey: key });
  const addMut = useMutation({
    mutationFn: () =>
      clients.admin.addOrgImportRepository({
        orgId: org.id,
        repository: repository.trim(),
        credentialId,
      }),
    onSuccess: () => {
      setRepository('');
      setCredentialId('');
      return refresh();
    },
  });
  const removeMut = useMutation({
    mutationFn: (r: OrgImportRepository) =>
      clients.admin.removeOrgImportRepository({ orgId: org.id, id: r.id }),
    onSuccess: refresh,
  });
  const resetErrors = () => {
    setLocalError('');
    addMut.reset();
    removeMut.reset();
  };

  const add = () => {
    resetErrors();
    const err = importRepositoryError(repository.trim());
    if (err) {
      setLocalError(err);
      return;
    }
    addMut.mutate();
  };
  // Inside the org dialog's form: Enter adds instead of submitting it.
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      add();
    }
  };

  const error =
    localError ||
    (addMut.error && errorText(addMut.error, 'Failed to add the repository')) ||
    (removeMut.error && errorText(removeMut.error, 'Failed to remove the repository')) ||
    (listQ.error && errorText(listQ.error, 'Failed to load the allowed repositories')) ||
    '';
  const busy = addMut.isPending || removeMut.isPending;
  const items = listQ.data?.items ?? [];
  const creds = credsQ.data?.items ?? [];

  return (
    <fieldset className='space-y-2' data-testid='org-import-repos'>
      <legend className='text-xs font-medium text-muted'>Allowed import repositories</legend>
      <p className='text-xs text-muted-2'>
        Strict tenant mode lets this organisation&rsquo;s pipelines use import.git only from these
        repositories, pinned to a full 40-character commit SHA; Shepherd fetches each module and
        checks its tenants.
      </p>
      {listQ.isLoading ? (
        <p className='text-xs text-muted-3'>Loading…</p>
      ) : items.length === 0 ? (
        <p className='text-xs italic text-muted-3' data-testid='org-import-repos-empty'>
          None &mdash; in strict tenant mode the org&rsquo;s pipelines cannot use import.git.
        </p>
      ) : (
        <ul className='divide-y divide-border rounded-md border border-border'>
          {items.map((r) => (
            <li
              key={r.id}
              className='flex items-center justify-between gap-2 px-3 py-1.5 text-xs'
              data-testid={`org-import-repo-row-${r.normalized}`}
            >
              <span className='min-w-0'>
                <span className='block truncate font-mono' title={r.repository}>
                  {r.repository}
                </span>
                {r.normalized !== r.repository && (
                  <span className='block truncate font-mono text-muted-3' title={r.normalized}>
                    matches {r.normalized}
                  </span>
                )}
              </span>
              <span className='flex shrink-0 items-center gap-3'>
                <span className='text-muted-2' data-testid='org-import-repo-credential'>
                  {importRepositoryCredential(r)}
                </span>
                {r.createdBy && <span className='text-muted-3'>by {r.createdBy}</span>}
                <button
                  type='button'
                  disabled={busy}
                  onClick={() => {
                    resetErrors();
                    removeMut.mutate(r);
                  }}
                  className='text-muted-3 hover:text-red-400'
                  aria-label={`Remove ${r.repository}`}
                  data-testid={`org-import-repo-remove-${r.normalized}`}
                >
                  <Trash2 size={13} />
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}
      <div className='flex items-end gap-2'>
        <Field label='Add repository' className='flex-1'>
          <Input
            value={repository}
            onChange={(e) => {
              resetErrors();
              setRepository(e.target.value);
            }}
            onKeyDown={onKeyDown}
            mono
            placeholder='https://github.com/acme/alloy-modules.git'
            aria-invalid={!!error}
            data-testid='org-import-repo-new'
          />
        </Field>
        <Select
          value={credentialId}
          onChange={(e) => {
            resetErrors();
            setCredentialId(e.target.value);
          }}
          className='w-40'
          aria-label='Credential'
          data-testid='org-import-repo-new-credential'
        >
          <option value=''>None (public)</option>
          {creds.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </Select>
        <button
          type='button'
          onClick={add}
          disabled={busy}
          className='rounded-md border border-border px-3 py-1.5 text-xs hover:border-indigo-500'
          data-testid='org-import-repo-add'
        >
          Add
        </button>
      </div>
      {error && (
        <p className='text-xs text-red-400' role='alert' data-testid='org-import-repo-error'>
          {error}
        </p>
      )}
    </fieldset>
  );
}
