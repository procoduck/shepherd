import { Field, Input } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';
import type { GitCredential, TestCredentialResponse } from '@/gen/shepherd/mgmt/v1/gitops_pb';

/** The "Test connectivity" modal for one credential (S9a). */
export function TestCredentialDialog({
  credential,
  value,
  onChange,
  result,
  onSubmit,
  pending,
  error,
  onClose,
}: {
  credential: GitCredential;
  value: { repoUrl: string; branch: string };
  onChange: (
    updater: (f: { repoUrl: string; branch: string }) => {
      repoUrl: string;
      branch: string;
    },
  ) => void;
  result: TestCredentialResponse | null;
  onSubmit: () => void;
  pending: boolean;
  /** Why the test could not run at all (#249); a failed test is `result`. */
  error?: string | null;
  onClose: () => void;
}) {
  return (
    <Modal title={`Test "${credential.name}"`} onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit();
        }}
        className='space-y-4'
      >
        <Field label='Repository URL'>
          <Input
            mono
            value={value.repoUrl}
            onChange={(e) => onChange((f) => ({ ...f, repoUrl: e.target.value }))}
            required
            placeholder='https://gitea.internal/team/configs.git'
          />
        </Field>
        <Field label='Branch' hint='Default "main"'>
          <Input
            mono
            value={value.branch}
            onChange={(e) => onChange((f) => ({ ...f, branch: e.target.value }))}
            placeholder='main'
          />
        </Field>

        {result && (
          <div
            data-testid='test-credential-result'
            className={`rounded-md border p-3 text-xs ${
              result.reachable
                ? 'border-emerald-400/30 bg-emerald-400/10 text-emerald-300'
                : 'border-red-400/30 bg-red-400/10 text-red-300'
            }`}
          >
            <p className='font-medium'>
              {result.reachable ? 'Reachable' : 'Unreachable'}
              {result.tokenExchangeRequired && (
                <span className='ml-2 font-normal text-muted-3'>
                  · token exchange {result.tokenExchangeOk ? 'succeeded' : 'failed'}
                </span>
              )}
            </p>
            {result.error && <p className='mt-1 font-mono text-muted'>{result.error}</p>}
          </div>
        )}

        <ModalActions
          onCancel={onClose}
          submitLabel='Run test'
          pendingLabel='Testing…'
          pending={pending}
          error={error}
        />
      </form>
    </Modal>
  );
}
