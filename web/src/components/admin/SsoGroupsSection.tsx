import { SsoBanner } from '@/components/admin/SsoBanner';
import type { SsoFormState } from '@/components/admin/ssoForm';
import { Field, Input, Textarea } from '@/components/ui/Field';
import { Section } from '@/components/ui/Section';
import type { OidcProviderPreset } from '@/gen/shepherd/mgmt/v1/admin_pb';

/** Admin → Single sign-on → "Groups and administrators" section (S9c). */
export function SsoGroupsSection({
  form,
  onChange,
  preset,
  disabled,
  readOnly,
}: {
  form: SsoFormState;
  onChange: (updater: (f: SsoFormState) => SsoFormState) => void;
  preset: OidcProviderPreset | undefined;
  disabled: boolean;
  readOnly: boolean;
}) {
  return (
    <Section title='Groups and administrators'>
      {preset?.groupsNote && (
        <SsoBanner tone='info' testId='sso-groups-note'>
          {preset.groupsNote}
        </SsoBanner>
      )}

      <Field
        label='Groups claim'
        hint='The ID token claim carrying group membership. Its values are what you enter below and in each organisation&apos;s admin/editor/viewer group.'
      >
        <Input
          data-testid='sso-groups-claim'
          disabled={disabled}
          value={form.groupsClaim}
          onChange={(e) => onChange((f) => ({ ...f, groupsClaim: e.target.value }))}
        />
      </Field>

      <Field
        label='App admin groups'
        hint='One per line. A user in any of these gets full app-admin access. Leave empty and nobody can administer Shepherd through single sign-on.'
      >
        <Textarea
          data-testid='sso-app-admin-groups'
          disabled={disabled}
          rows={3}
          value={form.appAdminGroups}
          onChange={(e) => onChange((f) => ({ ...f, appAdminGroups: e.target.value }))}
        />
      </Field>

      {form.appAdminGroups.trim() === '' && !readOnly && (
        <SsoBanner tone='warn' testId='sso-no-admin-groups'>
          No app admin groups are set. Keep your local admin account enabled, or you will have no
          way to administer Shepherd after signing in through this provider.
        </SsoBanner>
      )}

      {preset?.supportsGraphGroups && (
        <>
          <label className='flex items-start gap-2 text-sm'>
            <input
              data-testid='sso-use-graph'
              type='checkbox'
              disabled={disabled}
              checked={form.useGraphGroups}
              onChange={(e) => onChange((f) => ({ ...f, useGraphGroups: e.target.checked }))}
              className='mt-0.5'
            />
            <span>
              Resolve groups through Microsoft Graph
              <span className='block text-xs text-muted-2'>
                Recommended. Entra omits the groups claim entirely once a user is in more than ~200
                groups; Graph keeps working. Needs the GroupMember.Read.All delegated scope.
              </span>
            </span>
          </label>
          {form.useGraphGroups && (
            <Field label='Microsoft Graph base URL'>
              <Input
                data-testid='sso-graph-base-url'
                disabled={disabled}
                value={form.graphBaseUrl}
                onChange={(e) => onChange((f) => ({ ...f, graphBaseUrl: e.target.value }))}
                placeholder='https://graph.microsoft.com'
              />
            </Field>
          )}
        </>
      )}
    </Section>
  );
}
