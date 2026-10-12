import { SSO_CALLBACK_PATH, type SsoFormState } from '@/components/admin/ssoForm';
import { Field, Input, Select, Textarea } from '@/components/ui/Field';
import { Section } from '@/components/ui/Section';
import type { OidcProviderPreset } from '@/gen/shepherd/mgmt/v1/admin_pb';

/** Admin → Single sign-on → "Provider" section (S9c). */
export function SsoProviderSection({
  form,
  onChange,
  presets,
  preset,
  disabled,
  onSelectProvider,
  clientSecretSet,
  readOnly = false,
}: {
  form: SsoFormState;
  onChange: (updater: (f: SsoFormState) => SsoFormState) => void;
  presets: OidcProviderPreset[];
  preset: OidcProviderPreset | undefined;
  disabled: boolean;
  onSelectProvider: (preset: OidcProviderPreset) => void;
  clientSecretSet: boolean;
  /** Chart-managed: nothing here can be saved, so no "leave blank to keep
   *  it" advice about a secret nobody can enter — only whether the chart set
   *  one, never its value (#309). */
  readOnly?: boolean;
}) {
  // Advisory only. The server deliberately does not constrain the redirect
  // host -- it cannot reliably know its own external hostname -- so this is
  // the place a typo gets caught, as a warning rather than a refusal.
  const redirectHostMismatch = (() => {
    if (!form.redirectUrl.trim()) return false;
    try {
      return new URL(form.redirectUrl).host !== window.location.host;
    } catch {
      return false;
    }
  })();

  return (
    <Section title='Provider'>
      <Field label='Identity provider' hint={preset?.issuerHint}>
        <Select
          data-testid='sso-provider'
          disabled={disabled}
          value={form.provider}
          onChange={(e) => {
            const next = presets.find((p) => p.key === e.target.value);
            if (next) onSelectProvider(next);
          }}
        >
          {presets.map((p) => (
            <option key={p.key} value={p.key}>
              {p.displayName}
            </option>
          ))}
        </Select>
      </Field>

      <Field label='Sign-in button label'>
        <Input
          data-testid='sso-display-name'
          disabled={disabled}
          value={form.displayName}
          onChange={(e) => onChange((f) => ({ ...f, displayName: e.target.value }))}
          placeholder={preset?.displayName}
        />
      </Field>

      <Field label='Issuer URL' hint={preset ? `Example: ${preset.issuerTemplate}` : undefined}>
        <Input
          data-testid='sso-issuer'
          disabled={disabled}
          value={form.issuer}
          onChange={(e) => onChange((f) => ({ ...f, issuer: e.target.value }))}
          placeholder={preset?.issuerTemplate}
        />
      </Field>

      <Field label='Client ID'>
        <Input
          data-testid='sso-client-id'
          disabled={disabled}
          value={form.clientId}
          onChange={(e) => onChange((f) => ({ ...f, clientId: e.target.value }))}
        />
      </Field>

      <Field
        label='Client secret'
        hint={
          readOnly
            ? clientSecretSet
              ? 'A client secret is set by the chart.'
              : 'No client secret is set by the chart.'
            : clientSecretSet
              ? 'A secret is stored. Leave blank to keep it; enter a value to replace it.'
              : 'Stored encrypted. It is never shown again after saving.'
        }
      >
        <Input
          data-testid='sso-client-secret'
          type='password'
          autoComplete='new-password'
          disabled={disabled}
          value={form.clientSecret}
          onChange={(e) => onChange((f) => ({ ...f, clientSecret: e.target.value }))}
          placeholder={clientSecretSet ? '••••••••  (unchanged)' : ''}
        />
      </Field>

      <Field
        label='Redirect URL'
        hint={`Register this exact URL with your provider. Its path must be exactly ${SSO_CALLBACK_PATH}.`}
      >
        <Input
          data-testid='sso-redirect-url'
          disabled={disabled}
          value={form.redirectUrl}
          onChange={(e) => onChange((f) => ({ ...f, redirectUrl: e.target.value }))}
          placeholder={`${window.location.origin}${SSO_CALLBACK_PATH}`}
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
        <Textarea
          data-testid='sso-scopes'
          disabled={disabled}
          rows={3}
          value={form.scopes}
          onChange={(e) => onChange((f) => ({ ...f, scopes: e.target.value }))}
        />
      </Field>
    </Section>
  );
}
