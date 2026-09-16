import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Loader2, Plug, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { CollectorBindingsSection } from '@/components/admin/CollectorBindingsSection';
import { SsoBanner } from '@/components/admin/SsoBanner';
import { SsoClaimsSection } from '@/components/admin/SsoClaimsSection';
import { SsoGroupsSection } from '@/components/admin/SsoGroupsSection';
import { SsoProviderSection } from '@/components/admin/SsoProviderSection';
import { SsoTestReport } from '@/components/admin/SsoTestReport';
import {
  EMPTY_SSO_FORM,
  fromLines,
  SSO_CALLBACK_PATH,
  type SsoFormState,
  toLines,
} from '@/components/admin/ssoForm';
import { QueryError } from '@/components/QueryError';
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

export function AdminAuthPage() {
  const { data: me } = useMe();
  const isAppAdmin = !!me?.isAppAdmin;
  const qc = useQueryClient();

  const [form, setForm] = useState<SsoFormState>(EMPTY_SSO_FORM);
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
      redirectUrl: settings.redirectUrl || `${window.location.origin}${SSO_CALLBACK_PATH}`,
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
      setForm(EMPTY_SSO_FORM);
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
      <SsoBanner tone='warn' testId='sso-forbidden'>
        Single sign-on configuration is restricted to app admins.
      </SsoBanner>
    );
  }
  if (loadError) {
    return (
      <QueryError
        error={settingsQuery.error}
        noun='single sign-on settings'
        testId='sso-load-error'
      />
    );
  }

  const disabled = readOnly || saveMut.isPending;

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
        <SsoBanner
          tone={settings.enabled && !settings.active ? 'error' : readOnly ? 'info' : 'warn'}
          testId='sso-status'
        >
          {settings.statusMessage}
        </SsoBanner>
      )}
      {settings?.active && (
        <SsoBanner tone='ok' testId='sso-active'>
          Sign-in through {settings.displayName || 'this provider'} is live.
        </SsoBanner>
      )}

      <SsoProviderSection
        form={form}
        onChange={setForm}
        presets={presets}
        preset={preset}
        disabled={disabled}
        onSelectProvider={applyPreset}
        clientSecretSet={!!settings?.clientSecretSet}
      />

      <SsoGroupsSection
        form={form}
        onChange={setForm}
        preset={preset}
        disabled={disabled}
        readOnly={readOnly}
      />

      <SsoClaimsSection form={form} onChange={setForm} disabled={disabled} />

      {testResult && <SsoTestReport result={testResult} requested={fromLines(form.scopes)} />}

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

      <hr className='border-border' />
      <CollectorBindingsSection isAppAdmin={isAppAdmin} />

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
