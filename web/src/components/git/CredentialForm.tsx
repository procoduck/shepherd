import { CREDENTIAL_KINDS, type CredentialFormState, KIND_LABELS } from '@/components/git/gitForms';
import { Field, Input, Select, Textarea } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';

/** The "New credential" modal (S9a). Fields shown depend on `value.kind`. */
export function CredentialForm({
  value,
  onChange,
  onSubmit,
  pending,
  error,
  onCancel,
}: {
  value: CredentialFormState;
  onChange: (updater: (f: CredentialFormState) => CredentialFormState) => void;
  onSubmit: () => void;
  pending: boolean;
  /** The server's refusal, shown above the buttons (#249). */
  error?: string | null;
  onCancel: () => void;
}) {
  return (
    <Modal title='New credential' onClose={onCancel}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit();
        }}
        className='space-y-4 max-h-[70vh] overflow-y-auto pr-1'
      >
        <Field label='Name'>
          <Input
            value={value.name}
            onChange={(e) => onChange((f) => ({ ...f, name: e.target.value }))}
            required
            placeholder='prod-gitea'
          />
        </Field>
        <Field label='Kind'>
          <Select
            value={value.kind}
            onChange={(e) =>
              onChange((f) => ({ ...f, kind: e.target.value as CredentialFormState['kind'] }))
            }
          >
            {CREDENTIAL_KINDS.map((k) => (
              <option key={k} value={k}>
                {KIND_LABELS[k]}
              </option>
            ))}
          </Select>
        </Field>

        {(value.kind === 'basic' || value.kind === 'pat' || value.kind === 'ssh') && (
          <Field
            label={
              <>
                Username{' '}
                {value.kind === 'ssh' && <span className='text-muted-3'>(default "git")</span>}
              </>
            }
          >
            <Input
              mono
              value={value.username}
              onChange={(e) => onChange((f) => ({ ...f, username: e.target.value }))}
              placeholder={value.kind === 'pat' ? 'oauth2' : value.kind === 'ssh' ? 'git' : ''}
            />
          </Field>
        )}

        {value.kind === 'ado_sp' && (
          <>
            <Field label='Azure DevOps org URL'>
              <Input
                mono
                value={value.adoOrgUrl}
                onChange={(e) => onChange((f) => ({ ...f, adoOrgUrl: e.target.value }))}
                required
                placeholder='https://dev.azure.com/acme'
              />
            </Field>
            <Field label='Entra tenant ID'>
              <Input
                mono
                value={value.entraTenantId}
                onChange={(e) => onChange((f) => ({ ...f, entraTenantId: e.target.value }))}
                required
              />
            </Field>
            <Field label='Client ID'>
              <Input
                mono
                value={value.clientId}
                onChange={(e) => onChange((f) => ({ ...f, clientId: e.target.value }))}
                required
              />
            </Field>
          </>
        )}

        {value.kind === 'github_app' && (
          <>
            <Field label='App ID'>
              <Input
                mono
                value={value.appId}
                onChange={(e) => onChange((f) => ({ ...f, appId: e.target.value }))}
                required
              />
            </Field>
            <Field label='Installation ID'>
              <Input
                mono
                value={value.installationId}
                onChange={(e) => onChange((f) => ({ ...f, installationId: e.target.value }))}
                required
              />
            </Field>
            <Field
              label={
                <>
                  API base URL <span className='text-muted-3'>(GitHub Enterprise Server only)</span>
                </>
              }
            >
              <Input
                mono
                value={value.apiBaseUrl}
                onChange={(e) => onChange((f) => ({ ...f, apiBaseUrl: e.target.value }))}
                placeholder='https://api.github.com'
              />
            </Field>
          </>
        )}

        {value.kind === 'ssh' && (
          <Field
            label={
              <>
                Known hosts{' '}
                <span className='text-muted-3'>(required — no accept-any-host-key mode)</span>
              </>
            }
          >
            <Textarea
              mono
              value={value.sshKnownHosts}
              onChange={(e) => onChange((f) => ({ ...f, sshKnownHosts: e.target.value }))}
              required
              rows={2}
              placeholder='gitea.internal ssh-ed25519 AAAA...'
            />
          </Field>
        )}

        {value.kind !== 'none' && (
          <Field
            label={
              value.kind === 'ssh'
                ? 'Private key (PEM)'
                : value.kind === 'github_app'
                  ? 'Private key (PEM)'
                  : value.kind === 'ado_sp'
                    ? 'Client secret'
                    : value.kind === 'pat'
                      ? 'Token'
                      : 'Password'
            }
          >
            <Textarea
              mono
              value={value.clientSecret}
              onChange={(e) => onChange((f) => ({ ...f, clientSecret: e.target.value }))}
              required
              rows={value.kind === 'ssh' || value.kind === 'github_app' ? 4 : 1}
            />
          </Field>
        )}

        {value.kind === 'ssh' && (
          <Field label='Private key passphrase' optional>
            <Input
              mono
              type='password'
              value={value.secret2}
              onChange={(e) => onChange((f) => ({ ...f, secret2: e.target.value }))}
            />
          </Field>
        )}

        {value.kind !== 'ssh' && value.kind !== 'none' && (
          <details className='rounded-md border border-border p-3'>
            <summary className='cursor-pointer text-xs font-medium text-muted'>
              Advanced: private CA / TLS
            </summary>
            <div className='mt-3 space-y-3'>
              <Field label='CA bundle (PEM)' optional>
                <Textarea
                  mono
                  value={value.caCert}
                  onChange={(e) => onChange((f) => ({ ...f, caCert: e.target.value }))}
                  rows={3}
                />
              </Field>
              <label className='flex items-center gap-2 text-xs text-red-400'>
                <input
                  type='checkbox'
                  checked={value.tlsInsecureSkipVerify}
                  onChange={(e) =>
                    onChange((f) => ({ ...f, tlsInsecureSkipVerify: e.target.checked }))
                  }
                />
                Skip TLS certificate verification (unsafe)
              </label>
            </div>
          </details>
        )}

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
