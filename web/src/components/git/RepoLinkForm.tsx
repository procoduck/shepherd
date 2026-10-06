import {
  type CredentialFormState,
  KIND_LABELS,
  type RepoLinkFormState,
} from '@/components/git/gitForms';
import { Field, Input, Select } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';
import type { GitCredential } from '@/gen/shepherd/mgmt/v1/gitops_pb';

interface CollectorOption {
  id: string;
  cluster: string;
  role: string;
}

/** The "New repository link" modal (S9a). */
export function RepoLinkForm({
  value,
  onChange,
  onSubmit,
  pending,
  error,
  onCancel,
  collectors,
  credentials,
}: {
  value: RepoLinkFormState;
  onChange: (updater: (f: RepoLinkFormState) => RepoLinkFormState) => void;
  onSubmit: () => void;
  pending: boolean;
  /** The server's refusal, shown above the buttons (#249). */
  error?: string | null;
  onCancel: () => void;
  collectors: CollectorOption[];
  credentials: Pick<GitCredential, 'id' | 'name' | 'kind'>[];
}) {
  return (
    <Modal title='New repository link' onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit();
        }}
        className='space-y-4'
      >
        <Field label='Clone URL'>
          <Input
            mono
            value={value.repoUrl}
            onChange={(e) => onChange((f) => ({ ...f, repoUrl: e.target.value }))}
            required
            placeholder='https://gitea.internal/team/configs.git'
          />
        </Field>
        <Field label='Branch'>
          <Input
            mono
            value={value.branch}
            onChange={(e) => onChange((f) => ({ ...f, branch: e.target.value }))}
            placeholder='main'
          />
        </Field>
        <Field label='Path' hint='Subdirectory to scan for *.alloy files'>
          <Input
            mono
            value={value.path}
            onChange={(e) => onChange((f) => ({ ...f, path: e.target.value }))}
            placeholder='/'
          />
        </Field>
        <Field label='Target collector'>
          <Select
            value={value.collectorId}
            onChange={(e) => onChange((f) => ({ ...f, collectorId: e.target.value }))}
            required
          >
            <option value='' disabled>
              Select a collector…
            </option>
            {collectors.map((c) => (
              <option key={c.id} value={c.id}>
                {c.cluster} / {c.role}
              </option>
            ))}
          </Select>
        </Field>
        <Field label='Credential'>
          <Select
            value={value.credentialId}
            onChange={(e) => onChange((f) => ({ ...f, credentialId: e.target.value }))}
            required
          >
            <option value='' disabled>
              Select a credential…
            </option>
            {credentials.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name} ({KIND_LABELS[c.kind as CredentialFormState['kind']] ?? c.kind})
              </option>
            ))}
          </Select>
        </Field>
        <ModalActions
          onCancel={onCancel}
          submitLabel='Create'
          pendingLabel='Creating…'
          pending={pending}
          error={error}
        />
      </form>
    </Modal>
  );
}
