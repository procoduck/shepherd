import type { JsonObject } from '@bufbuild/protobuf';
import { useState } from 'react';
import { Field, Input, Select } from '@/components/ui/Field';
import { Modal, ModalActions } from '@/components/ui/Modal';

/*
 * The destination create/edit dialog and the auth-mode helpers it shares with
 * DestinationsPage — split out of the page to keep it under pageSize.test.ts's
 * 500-line cap once the Secret key contract (#229) landed here.
 */

/**
 * Human labels for the auth modes the schema admits (0001_init: `none`,
 * `oauth2_secret`, `basic_secret`). An unknown value — possible, since the API
 * stores whatever it is sent — is shown verbatim rather than hidden.
 */
export const AUTH_MODE_LABELS: Record<string, string> = {
  none: 'None',
  basic_secret: 'Basic auth (Kubernetes Secret)',
  oauth2_secret: 'OAuth2 (Kubernetes Secret)',
};

export function authModeLabel(mode: string): string {
  return AUTH_MODE_LABELS[mode] ?? mode;
}

export function isSecretMode(mode: string): boolean {
  return mode === 'basic_secret' || mode === 'oauth2_secret';
}

/**
 * The Secret key contract per auth mode (#229) — mirrors wizard.SecretKeys in
 * internal/wizard/destination.go, which is what renders them. Change both
 * together.
 */
const SECRET_KEYS: Record<string, string[]> = {
  basic_secret: ['username', 'password'],
  oauth2_secret: ['client_id', 'client_secret', 'token_url'],
};

/** destinations.extra key holding an oauth2_secret destination's scopes. */
const SCOPES_KEY = 'oauth2_scopes';

export function scopesFromExtra(extra: JsonObject | undefined): string {
  const v = extra?.[SCOPES_KEY];
  return Array.isArray(v) ? v.filter((x) => typeof x === 'string').join(' ') : '';
}

/**
 * What a secret-based auth mode does: wizard pipelines that ship to this
 * destination read the named Secret on the spoke cluster at runtime
 * (`remote.kubernetes.secret`) and add the matching auth block to the writer.
 * The keys listed are the contract the Secret must satisfy.
 */
function SecretModeExplanation({ mode }: { mode: string }) {
  const kind = mode === 'basic_secret' ? 'HTTP basic auth' : 'OAuth2 client credentials';
  const keys = SECRET_KEYS[mode] ?? [];
  return (
    <div
      data-testid='auth-mode-explanation'
      className='rounded-md border border-border bg-card/40 px-3 py-2 text-2xs text-muted'
    >
      <p>
        For {kind} kept in a Kubernetes Secret that already exists on each spoke cluster. Shepherd
        stores only the Secret's namespace and name &mdash; never the credential itself.
      </p>
      <p className='mt-1'>
        The Secret must hold the keys{' '}
        {keys.map((k, i) => (
          <span key={k}>
            {i > 0 && ', '}
            <code className='font-mono text-muted'>{k}</code>
          </span>
        ))}
        . Wizard pipelines that ship here read it when the collector runs and add the auth block.
        The collector's service account needs <code className='font-mono'>get</code>,{' '}
        <code className='font-mono'>list</code> and <code className='font-mono'>watch</code> on
        Secrets in that namespace. Saving a change here regenerates every wizard pipeline that ships
        to this destination; if any of them would fail validation, nothing is saved.
      </p>
    </div>
  );
}

/** Where the URL a destination type writes to usually ends. */
const URL_PLACEHOLDERS: Record<string, string> = {
  prometheus: 'https://mimir.example.com/api/v1/push',
  loki: 'https://loki.example.com/loki/api/v1/push',
  otlp: 'https://tempo.example.com:4318',
};

export interface DestinationFormState {
  name: string;
  type: string;
  url: string;
  authMode: string;
  secretNamespace: string;
  secretName: string;
  /** Space-separated; stored as extra.oauth2_scopes (a list). */
  scopes: string;
}

export const EMPTY_FORM: DestinationFormState = {
  name: '',
  type: 'prometheus',
  url: '',
  authMode: 'none',
  secretNamespace: '',
  secretName: '',
  scopes: '',
};

/**
 * The extra to send: the stored one with oauth2_scopes replaced by the form's
 * scopes for an oauth2_secret destination, and removed otherwise, so a
 * destination switched away from OAuth2 does not keep stale scopes.
 */
export function extraWithScopes(
  stored: JsonObject | undefined,
  form: DestinationFormState,
): JsonObject {
  const { [SCOPES_KEY]: _stale, ...rest } = stored ?? {};
  const scopes = form.scopes.split(/\s+/).filter(Boolean);
  return form.authMode === 'oauth2_secret' && scopes.length > 0
    ? { ...rest, [SCOPES_KEY]: scopes }
    : rest;
}

function validateUrl(url: string): string {
  try {
    const parsed = new URL(url);
    // new URL() accepts javascript: and data: quite happily, so parsing is
    // not validation. Only http(s) is a destination Shepherd can ship to.
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return 'Only http:// and https:// destinations are supported';
    }
    return '';
  } catch {
    return 'Enter a valid URL (e.g. http://prometheus:9090)';
  }
}

/**
 * The create and edit dialog share one form. The secret reference fields only
 * appear for a secret-based auth mode; the parent decides what to send for
 * them when the mode is `none`.
 */
export function DestinationFormDialog({
  title,
  initial,
  submitLabel,
  pendingLabel,
  pending,
  onCancel,
  onSubmit,
}: {
  title: string;
  initial: DestinationFormState;
  submitLabel: string;
  pendingLabel: string;
  pending: boolean;
  onCancel: () => void;
  onSubmit: (form: DestinationFormState) => void;
}) {
  const [form, setForm] = useState(initial);
  const [urlError, setUrlError] = useState('');
  const secretMode = isSecretMode(form.authMode);

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const err = validateUrl(form.url);
    setUrlError(err);
    if (!err) onSubmit(form);
  }

  return (
    <Modal title={title} onClose={onCancel}>
      <form onSubmit={handleSubmit} className='space-y-4'>
        <Field label='Name'>
          <Input
            value={form.name}
            onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            required
            placeholder='prom-prod'
          />
        </Field>
        <Field label='Type'>
          <Select
            value={form.type}
            onChange={(e) => setForm((f) => ({ ...f, type: e.target.value }))}
          >
            <option value='prometheus'>Prometheus</option>
            <option value='loki'>Loki</option>
            <option value='otlp'>Tempo (OTLP)</option>
          </Select>
        </Field>
        <Field label='URL' error={urlError}>
          <Input
            value={form.url}
            onChange={(e) => {
              setForm((f) => ({ ...f, url: e.target.value }));
              setUrlError('');
            }}
            onBlur={() => form.url && setUrlError(validateUrl(form.url))}
            required
            mono
            placeholder={URL_PLACEHOLDERS[form.type] ?? 'https://'}
          />
        </Field>
        <p className='-mt-2 text-2xs text-muted-3'>
          The full endpoint URL the collector writes to &mdash; Shepherd adds no path.
        </p>
        <Field label='Auth mode'>
          <Select
            value={form.authMode}
            onChange={(e) => setForm((f) => ({ ...f, authMode: e.target.value }))}
          >
            {!(form.authMode in AUTH_MODE_LABELS) && (
              <option value={form.authMode}>{form.authMode}</option>
            )}
            <option value='none'>{AUTH_MODE_LABELS.none}</option>
            <option value='basic_secret'>{AUTH_MODE_LABELS.basic_secret}</option>
            <option value='oauth2_secret'>{AUTH_MODE_LABELS.oauth2_secret}</option>
          </Select>
        </Field>
        {secretMode && (
          <>
            <SecretModeExplanation mode={form.authMode} />
            <div className='flex gap-3'>
              <Field label='Secret namespace' className='flex-1'>
                <Input
                  value={form.secretNamespace}
                  onChange={(e) => setForm((f) => ({ ...f, secretNamespace: e.target.value }))}
                  required
                  mono
                  placeholder='monitoring'
                />
              </Field>
              <Field label='Secret name' className='flex-1'>
                <Input
                  value={form.secretName}
                  onChange={(e) => setForm((f) => ({ ...f, secretName: e.target.value }))}
                  required
                  mono
                  placeholder='mimir-credentials'
                />
              </Field>
            </div>
            {form.authMode === 'oauth2_secret' && (
              <Field label='OAuth2 scopes' optional>
                <Input
                  value={form.scopes}
                  onChange={(e) => setForm((f) => ({ ...f, scopes: e.target.value }))}
                  mono
                  placeholder='api://mimir/.default'
                />
              </Field>
            )}
          </>
        )}
        <ModalActions
          onCancel={onCancel}
          submitLabel={submitLabel}
          pendingLabel={pendingLabel}
          pending={pending}
        />
      </form>
    </Modal>
  );
}
