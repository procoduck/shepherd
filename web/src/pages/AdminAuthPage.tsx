import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, CheckCircle2, Info, Loader2, Plug, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import type { OidcProviderPreset, TestOidcSettingsResponse } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { useMe } from '@/hooks/useMe';

/**
 * Admin → Single sign-on.
 *
 * Lets an app admin point Shepherd at an identity provider when the Helm
 * chart did not already do it. Chart config wins, so when it is present this
 * page shows what the cluster is pointed at and disables every control rather
 * than hiding the values — an admin still needs to be able to READ the
 * configuration they cannot change from here.
 */

/** Local form state. Mirrors UpdateOidcSettingsRequest, with the list fields
 *  held as the newline-separated text the textareas actually edit. */
interface FormState {
  enabled: boolean;
  provider: string;
  displayName: string;
  issuer: string;
  clientId: string;
  clientSecret: string;
  redirectUrl: string;
  scopes: string;
  subjectClaim: string;
  emailClaim: string;
  nameClaim: string;
  groupsClaim: string;
  appAdminGroups: string;
  useGraphGroups: boolean;
  graphBaseUrl: string;
}

const EMPTY_FORM: FormState = {
  enabled: false,
  provider: 'generic',
  displayName: '',
  issuer: '',
  clientId: '',
  clientSecret: '',
  redirectUrl: '',
  scopes: '',
  subjectClaim: '',
  emailClaim: '',
  nameClaim: '',
  groupsClaim: '',
  appAdminGroups: '',
  useGraphGroups: false,
  graphBaseUrl: '',
};

const toLines = (values: string[]) => values.join('\n');
const fromLines = (text: string) =>
  text
    .split(/[\n,]/)
    .map((value) => value.trim())
    .filter(Boolean);

/** The only callback path the server serves (internal/auth.CallbackPath). */
const CALLBACK_PATH = '/auth/callback';

export function AdminAuthPage() {
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const qc = useQueryClient();

  const [form, setForm] = useState<FormState>(EMPTY_FORM);
  const [testResult, setTestResult] = useState<TestOidcSettingsResponse | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);

  const settingsQuery = useQuery({
    queryKey: ['oidc-settings'],
    queryFn: () => clients.admin.getOidcSettings({}),
  });
  const presetsQuery = useQuery({
    queryKey: ['oidc-presets'],
    queryFn: () => clients.admin.listOidcProviderPresets({}),
    staleTime: Number.POSITIVE_INFINITY,
  });

  const settings = settingsQuery.data;
  const presets = presetsQuery.data?.items ?? [];
  const preset = presets.find((p) => p.key === form.provider);
  const readOnly = settings ? !settings.editable : true;

  // Seed the form from the server once the settings land. Keyed on updatedAt
  // so a save's response re-seeds (picking up server-side normalization the
  // admin should see) without clobbering edits in progress on every refetch.
  const seedKey = settings ? `${settings.source}:${settings.updatedAt?.seconds ?? 0}` : '';
  useEffect(() => {
    if (!settings) return;
    setForm({
      enabled: settings.enabled,
      provider: settings.provider || 'generic',
      displayName: settings.displayName,
      issuer: settings.issuer,
      clientId: settings.clientId,
      // Never seeded: the server does not return the secret. Blank means
      // "keep the stored one" on save.
      clientSecret: '',
      redirectUrl: settings.redirectUrl || `${window.location.origin}${CALLBACK_PATH}`,
      scopes: toLines(settings.scopes),
      subjectClaim: settings.subjectClaim,
      emailClaim: settings.emailClaim,
      nameClaim: settings.nameClaim,
      groupsClaim: settings.groupsClaim,
      appAdminGroups: toLines(settings.appAdminGroups),
      useGraphGroups: settings.useGraphGroups,
      graphBaseUrl: settings.graphBaseUrl,
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- re-seed on identity of the server state, not on every field
  }, [seedKey]);

  /** Applying a preset overwrites only the fields the preset speaks for, so a
   *  half-filled form does not lose the issuer or client ID on a provider
   *  switch — those are the values the admin typed, not defaults. */
  function applyPreset(next: OidcProviderPreset) {
    setTestResult(null);
    setForm((current) => ({
      ...current,
      provider: next.key,
      displayName: next.displayName,
      scopes: toLines(next.scopes),
      subjectClaim: next.subjectClaim,
      emailClaim: next.emailClaim,
      nameClaim: next.nameClaim,
      groupsClaim: next.groupsClaim,
      useGraphGroups: next.supportsGraphGroups,
      graphBaseUrl: current.graphBaseUrl || 'https://graph.microsoft.com',
    }));
  }

  const saveMut = useMutation({
    mutationFn: () =>
      clients.admin.updateOidcSettings({
        enabled: form.enabled,
        provider: form.provider,
        displayName: form.displayName,
        issuer: form.issuer,
        clientId: form.clientId,
        clientSecret: form.clientSecret,
        redirectUrl: form.redirectUrl,
        scopes: fromLines(form.scopes),
        subjectClaim: form.subjectClaim,
        emailClaim: form.emailClaim,
        nameClaim: form.nameClaim,
        groupsClaim: form.groupsClaim,
        appAdminGroups: fromLines(form.appAdminGroups),
        useGraphGroups: form.useGraphGroups,
        graphBaseUrl: form.graphBaseUrl,
      }),
    onSuccess: (resp) => {
      qc.setQueryData(['oidc-settings'], resp);
      qc.invalidateQueries({ queryKey: ['oidc-settings'] });
      // Drop the typed secret as soon as it is stored: the server will not
      // return it, so leaving the plaintext sitting in a DOM input for the
      // rest of the session buys nothing and is one screen-share away from
      // being a disclosure.
      setForm((current) => ({ ...current, clientSecret: '' }));
      toast.success(resp.active ? 'Single sign-on saved and active' : 'Single sign-on saved');
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to save single sign-on settings'),
  });

  const testMut = useMutation({
    mutationFn: () =>
      clients.admin.testOidcSettings({
        provider: form.provider,
        issuer: form.issuer,
        clientId: form.clientId,
        clientSecret: form.clientSecret,
        scopes: fromLines(form.scopes),
      }),
    onSuccess: (resp) => setTestResult(resp),
    onError: (e) => toast.error(toApiError(e).message || 'Test failed'),
  });

  const removeMut = useMutation({
    mutationFn: () => clients.admin.deleteOidcSettings({}),
    onSuccess: () => {
      setConfirmRemove(false);
      setTestResult(null);
      setForm(EMPTY_FORM);
      qc.invalidateQueries({ queryKey: ['oidc-settings'] });
      toast.success('Single sign-on configuration removed');
    },
    onError: (e) => toast.error(toApiError(e).message || 'Failed to remove configuration'),
  });

  if (settingsQuery.isLoading) {
    return <p className='text-sm text-muted'>Loading…</p>;
  }
  // Role check FIRST, and permission_denied treated as the same case. The
  // server refuses a non-app-admin at the authz interceptor, so ordering the
  // error branch ahead of this one made the friendly message unreachable in
  // production and showed a raw "auth: forbidden" instead.
  const loadError = settingsQuery.isError ? toApiError(settingsQuery.error) : null;
  if (!isAppAdmin || loadError?.code === 'permission_denied') {
    return (
      <Banner tone='warn' testId='sso-forbidden'>
        Single sign-on configuration is restricted to app admins.
      </Banner>
    );
  }
  if (loadError) {
    return (
      <Banner tone='error' testId='sso-load-error'>
        {loadError.message || 'Single sign-on settings are unavailable on this deployment.'}
      </Banner>
    );
  }

  const disabled = readOnly || saveMut.isPending;

  // Advisory only. The server deliberately does not constrain the redirect
  // host — it cannot reliably know its own external hostname — so this is the
  // place a typo gets caught, as a warning rather than a refusal.
  const redirectHostMismatch = (() => {
    if (!form.redirectUrl.trim()) return false;
    try {
      return new URL(form.redirectUrl).host !== window.location.host;
    } catch {
      return false;
    }
  })();

  return (
    <div className='space-y-5 max-w-3xl'>
      <div>
        <h1 className='text-xl font-semibold'>Single sign-on</h1>
        <p className='mt-1 text-sm text-muted'>
          Point Shepherd at your identity provider. Group values from the provider decide who is an
          app admin here and which organisations a user can reach.
        </p>
      </div>

      {settings?.statusMessage && (
        <Banner
          tone={settings.enabled && !settings.active ? 'error' : readOnly ? 'info' : 'warn'}
          testId='sso-status'
        >
          {settings.statusMessage}
        </Banner>
      )}
      {settings?.active && (
        <Banner tone='ok' testId='sso-active'>
          Sign-in through {settings.displayName || 'this provider'} is live.
        </Banner>
      )}

      <Section title='Provider'>
        <Field label='Identity provider' hint={preset?.issuerHint}>
          <select
            data-testid='sso-provider'
            disabled={disabled}
            value={form.provider}
            onChange={(e) => {
              const next = presets.find((p) => p.key === e.target.value);
              if (next) applyPreset(next);
            }}
            className={inputClass}
          >
            {presets.map((p) => (
              <option key={p.key} value={p.key}>
                {p.displayName}
              </option>
            ))}
          </select>
        </Field>

        <Field label='Sign-in button label'>
          <input
            data-testid='sso-display-name'
            disabled={disabled}
            value={form.displayName}
            onChange={(e) => setForm({ ...form, displayName: e.target.value })}
            placeholder={preset?.displayName}
            className={inputClass}
          />
        </Field>

        <Field label='Issuer URL' hint={preset ? `Example: ${preset.issuerTemplate}` : undefined}>
          <input
            data-testid='sso-issuer'
            disabled={disabled}
            value={form.issuer}
            onChange={(e) => setForm({ ...form, issuer: e.target.value })}
            placeholder={preset?.issuerTemplate}
            className={inputClass}
          />
        </Field>

        <Field label='Client ID'>
          <input
            data-testid='sso-client-id'
            disabled={disabled}
            value={form.clientId}
            onChange={(e) => setForm({ ...form, clientId: e.target.value })}
            className={inputClass}
          />
        </Field>

        <Field
          label='Client secret'
          hint={
            settings?.clientSecretSet
              ? 'A secret is stored. Leave blank to keep it; enter a value to replace it.'
              : 'Stored encrypted. It is never shown again after saving.'
          }
        >
          <input
            data-testid='sso-client-secret'
            type='password'
            autoComplete='new-password'
            disabled={disabled}
            value={form.clientSecret}
            onChange={(e) => setForm({ ...form, clientSecret: e.target.value })}
            placeholder={settings?.clientSecretSet ? '••••••••  (unchanged)' : ''}
            className={inputClass}
          />
        </Field>

        <Field
          label='Redirect URL'
          hint={`Register this exact URL with your provider. Its path must be exactly ${CALLBACK_PATH}.`}
        >
          <input
            data-testid='sso-redirect-url'
            disabled={disabled}
            value={form.redirectUrl}
            onChange={(e) => setForm({ ...form, redirectUrl: e.target.value })}
            placeholder={`${window.location.origin}${CALLBACK_PATH}`}
            className={inputClass}
          />
          {redirectHostMismatch && (
            <p data-testid='sso-redirect-host-warning' className='text-xs text-amber-400'>
              This points at a different host than the one you are using now ({window.location.host}
              ). That is legitimate behind a proxy or a different external hostname — but if it is a
              typo, sign-in will fail after the provider redirects.
            </p>
          )}
        </Field>

        <Field label='Scopes' hint='One per line. "openid" is required and added automatically.'>
          <textarea
            data-testid='sso-scopes'
            disabled={disabled}
            rows={3}
            value={form.scopes}
            onChange={(e) => setForm({ ...form, scopes: e.target.value })}
            className={inputClass}
          />
        </Field>
      </Section>

      <Section title='Groups and administrators'>
        {preset?.groupsNote && (
          <Banner tone='info' testId='sso-groups-note'>
            {preset.groupsNote}
          </Banner>
        )}

        <Field
          label='Groups claim'
          hint='The ID token claim carrying group membership. Its values are what you enter below and in each organisation&apos;s admin/reader group.'
        >
          <input
            data-testid='sso-groups-claim'
            disabled={disabled}
            value={form.groupsClaim}
            onChange={(e) => setForm({ ...form, groupsClaim: e.target.value })}
            className={inputClass}
          />
        </Field>

        <Field
          label='App admin groups'
          hint='One per line. A user in any of these gets full app-admin access. Leave empty and nobody can administer Shepherd through single sign-on.'
        >
          <textarea
            data-testid='sso-app-admin-groups'
            disabled={disabled}
            rows={3}
            value={form.appAdminGroups}
            onChange={(e) => setForm({ ...form, appAdminGroups: e.target.value })}
            className={inputClass}
          />
        </Field>

        {form.appAdminGroups.trim() === '' && !readOnly && (
          <Banner tone='warn' testId='sso-no-admin-groups'>
            No app admin groups are set. Keep your local admin account enabled, or you will have no
            way to administer Shepherd after signing in through this provider.
          </Banner>
        )}

        {preset?.supportsGraphGroups && (
          <>
            <label className='flex items-start gap-2 text-sm'>
              <input
                data-testid='sso-use-graph'
                type='checkbox'
                disabled={disabled}
                checked={form.useGraphGroups}
                onChange={(e) => setForm({ ...form, useGraphGroups: e.target.checked })}
                className='mt-0.5'
              />
              <span>
                Resolve groups through Microsoft Graph
                <span className='block text-xs text-muted-2'>
                  Recommended. Entra omits the groups claim entirely once a user is in more than
                  ~200 groups; Graph keeps working. Needs the GroupMember.Read.All delegated scope.
                </span>
              </span>
            </label>
            {form.useGraphGroups && (
              <Field label='Microsoft Graph base URL'>
                <input
                  data-testid='sso-graph-base-url'
                  disabled={disabled}
                  value={form.graphBaseUrl}
                  onChange={(e) => setForm({ ...form, graphBaseUrl: e.target.value })}
                  placeholder='https://graph.microsoft.com'
                  className={inputClass}
                />
              </Field>
            )}
          </>
        )}
      </Section>

      <Section title='Claim mapping'>
        <div className='grid gap-3 sm:grid-cols-3'>
          <Field label='Subject claim'>
            <input
              data-testid='sso-subject-claim'
              disabled={disabled}
              value={form.subjectClaim}
              onChange={(e) => setForm({ ...form, subjectClaim: e.target.value })}
              className={inputClass}
            />
          </Field>
          <Field label='Email claim'>
            <input
              data-testid='sso-email-claim'
              disabled={disabled}
              value={form.emailClaim}
              onChange={(e) => setForm({ ...form, emailClaim: e.target.value })}
              className={inputClass}
            />
          </Field>
          <Field label='Name claim'>
            <input
              data-testid='sso-name-claim'
              disabled={disabled}
              value={form.nameClaim}
              onChange={(e) => setForm({ ...form, nameClaim: e.target.value })}
              className={inputClass}
            />
          </Field>
        </div>
      </Section>

      {testResult && <TestReport result={testResult} requested={fromLines(form.scopes)} />}

      <div className='flex flex-wrap items-center gap-3 border-t border-border pt-4'>
        <label className='flex items-center gap-2 text-sm'>
          <input
            data-testid='sso-enabled'
            type='checkbox'
            disabled={disabled}
            checked={form.enabled}
            onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
          />
          Enable single sign-on
        </label>

        <div className='flex-1' />

        <button
          data-testid='sso-test'
          type='button'
          disabled={readOnly || testMut.isPending || !form.issuer}
          onClick={() => testMut.mutate()}
          className='flex items-center gap-1.5 rounded-md border border-border-strong px-3 py-1.5 text-xs font-medium hover:bg-border disabled:opacity-50'
        >
          {testMut.isPending ? <Loader2 size={14} className='animate-spin' /> : <Plug size={14} />}
          Test connection
        </button>

        {settings?.configured && settings.editable && (
          <button
            data-testid='sso-remove'
            type='button'
            onClick={() => setConfirmRemove(true)}
            className='flex items-center gap-1.5 rounded-md border border-red-500/40 px-3 py-1.5 text-xs font-medium text-red-400 hover:bg-red-500/10'
          >
            <Trash2 size={14} /> Remove
          </button>
        )}

        <button
          data-testid='sso-save'
          type='button'
          disabled={disabled}
          onClick={() => saveMut.mutate()}
          className='flex items-center gap-1.5 rounded-md bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:opacity-50'
        >
          {saveMut.isPending && <Loader2 size={14} className='animate-spin' />}
          Save
        </button>
      </div>

      {settings?.updatedBy && (
        <p className='text-xs text-muted-2'>Last changed by {settings.updatedBy}.</p>
      )}

      {confirmRemove && (
        <AdminConfirmDialog
          title='Remove single sign-on?'
          body={`Users will no longer be able to sign in through ${
            settings?.displayName || 'this provider'
          }. Make sure you can still get in another way before removing it.`}
          confirmLabel='Remove'
          pendingLabel='Removing…'
          pending={removeMut.isPending}
          onCancel={() => setConfirmRemove(false)}
          onConfirm={() => removeMut.mutate()}
        />
      )}
    </div>
  );
}

function TestReport({
  result,
  requested,
}: {
  result: TestOidcSettingsResponse;
  requested: string[];
}) {
  if (!result.ok) {
    return (
      <Banner tone='error' testId='sso-test-result'>
        {result.message}
      </Banner>
    );
  }
  return (
    <div
      data-testid='sso-test-result'
      className='rounded-md border border-emerald-500/30 bg-emerald-500/10 p-3 text-sm space-y-1'
    >
      <p className='flex items-center gap-1.5 font-medium text-emerald-400'>
        <CheckCircle2 size={14} /> Discovery succeeded
      </p>
      <p className='text-xs text-muted-2'>
        This checks the issuer only. The client ID, secret, and redirect URL are not exercised until
        someone actually signs in.
      </p>
      <dl className='grid grid-cols-[max-content_1fr] gap-x-3 gap-y-0.5 text-xs text-muted'>
        <dt>Issuer</dt>
        <dd className='break-all'>{result.issuer}</dd>
        <dt>Authorize</dt>
        <dd className='break-all'>{result.authorizationEndpoint}</dd>
        <dt>Token</dt>
        <dd className='break-all'>{result.tokenEndpoint}</dd>
        <dt>JWKS</dt>
        <dd className='break-all'>{result.jwksUri}</dd>
      </dl>
      {result.issuerMismatch && <p className='text-xs text-amber-400'>{result.issuerMismatch}</p>}
      {!result.supportsPkce && (
        <p className='text-xs text-amber-400'>
          This provider does not advertise PKCE (S256). Shepherd always sends a PKCE challenge, so
          sign-in may fail.
        </p>
      )}
      {result.missingScopes.length > 0 && (
        <p className='text-xs text-amber-400'>
          Not advertised as supported: {result.missingScopes.join(', ')}. Many providers
          under-report this, so it is worth checking rather than trusting.
        </p>
      )}
      {requested.length > 0 && result.supportedScopes.length === 0 && (
        <p className='text-xs text-muted-2'>
          This provider does not publish a scope list, so the requested scopes could not be checked.
        </p>
      )}
    </div>
  );
}

const inputClass =
  'w-full rounded-md border border-border-strong bg-border px-3 py-2 text-sm text-zinc-100 disabled:opacity-60';

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className='space-y-3 rounded-lg border border-border bg-card/40 p-4'>
      <h2 className='text-sm font-semibold text-zinc-200'>{title}</h2>
      {children}
    </section>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className='space-y-1'>
      <div className='text-xs font-medium text-muted'>{label}</div>
      {children}
      {hint && <p className='text-xs text-muted-2'>{hint}</p>}
    </div>
  );
}

function Banner({
  tone,
  testId,
  children,
}: {
  tone: 'ok' | 'info' | 'warn' | 'error';
  testId: string;
  children: React.ReactNode;
}) {
  const tones = {
    ok: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
    info: 'border-indigo-500/30 bg-indigo-500/10 text-indigo-300',
    warn: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
    error: 'border-red-500/30 bg-red-500/10 text-red-400',
  } as const;
  const Icon = tone === 'ok' ? CheckCircle2 : tone === 'error' ? AlertTriangle : Info;
  return (
    <div
      data-testid={testId}
      className={`flex items-start gap-2 rounded-md border p-3 text-sm ${tones[tone]}`}
    >
      <Icon size={15} className='mt-0.5 shrink-0' />
      <span>{children}</span>
    </div>
  );
}
