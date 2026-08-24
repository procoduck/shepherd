/**
 * Admin → Single sign-on (/admin/auth).
 *
 * The states worth pinning are the ones an operator can get stuck in: a fresh
 * install with nothing configured, a chart-managed deployment where the form
 * must be readable but not writable, and the two branches of the discovery
 * probe.
 */
import { appAdmin, orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('opens on preset defaults when nothing is configured', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');

  await expect(page.getByRole('heading', { name: 'Single sign-on' })).toBeVisible();
  await expect(page.getByTestId('sso-provider')).toHaveValue('generic');
  await expect(page.getByTestId('sso-subject-claim')).toHaveValue('sub');
  // Nothing stored, so there is nothing to remove.
  await expect(page.getByTestId('sso-remove')).toHaveCount(0);
});

test('choosing a provider applies its claim and scope defaults', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');

  await page.getByTestId('sso-provider').selectOption('entra');
  // Entra's subject is the object ID, not the spec's "sub" — the single
  // most consequential per-provider difference in this form.
  await expect(page.getByTestId('sso-subject-claim')).toHaveValue('oid');
  await expect(page.getByTestId('sso-display-name')).toHaveValue('Microsoft');
  await expect(page.getByTestId('sso-use-graph')).toBeChecked();

  await page.getByTestId('sso-provider').selectOption('okta');
  await expect(page.getByTestId('sso-subject-claim')).toHaveValue('sub');
  // Graph is Entra's directory API; the option disappears entirely elsewhere.
  await expect(page.getByTestId('sso-use-graph')).toHaveCount(0);
});

test('warns while no app admin group is set', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');

  await expect(page.getByTestId('sso-no-admin-groups')).toBeVisible();
  await page.getByTestId('sso-app-admin-groups').fill('platform-admins');
  await expect(page.getByTestId('sso-no-admin-groups')).toHaveCount(0);
});

test('saving an enabled provider reports it live', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');

  await page.getByTestId('sso-provider').selectOption('okta');
  await page.getByTestId('sso-issuer').fill('https://acme.okta.com/oauth2/default');
  await page.getByTestId('sso-client-id').fill('client-id');
  await page.getByTestId('sso-client-secret').fill('client-secret');
  await page.getByTestId('sso-redirect-url').fill('https://shepherd.example/auth/callback');
  await page.getByTestId('sso-app-admin-groups').fill('platform-admins');
  await page.getByTestId('sso-enabled').check();
  await page.getByTestId('sso-save').click();

  await expect(page.getByTestId('sso-active')).toBeVisible();
  // The secret is write-only: after a save the field is empty and the hint
  // says the stored one is kept.
  await expect(page.getByTestId('sso-client-secret')).toHaveValue('');
  await expect(page.getByText(/Leave blank to keep it/i)).toBeVisible();
});

test('test connection reports discovery endpoints, and failures inline', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');
  await page.getByTestId('sso-issuer').fill('https://idp.example/realms/shepherd');

  await page.getByTestId('sso-test').click();
  await expect(page.getByTestId('sso-test-result')).toContainText('Discovery succeeded');
  await expect(page.getByTestId('sso-test-result')).toContainText('openid-connect/auth');

  api.seed({
    oidcTestResult: {
      ok: false,
      message: 'OIDC discovery against https://idp.example/wrong failed: 404 Not Found',
    },
  });
  await page.getByTestId('sso-test').click();
  await expect(page.getByTestId('sso-test-result')).toContainText('404 Not Found');
});

test('a chart-managed provider is readable but not editable', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    oidcSettings: {
      configured: true,
      enabled: true,
      active: true,
      source: 'helm',
      editable: false,
      provider: 'entra',
      display_name: 'Microsoft',
      issuer: 'https://login.microsoftonline.com/tenant/v2.0',
      client_id: 'chart-client',
      client_secret_set: true,
      redirect_url: 'https://shepherd.example/auth/callback',
      scopes: ['openid', 'profile', 'email'],
      subject_claim: 'oid',
      email_claim: 'email',
      name_claim: 'name',
      groups_claim: 'groups',
      app_admin_groups: ['chart-group'],
      use_graph_groups: true,
      graph_base_url: 'https://graph.microsoft.com',
      status_message: 'This provider is configured by the Helm chart (oidc.issuer).',
      updated_by: '',
    },
  });
  await page.goto('/admin/auth');

  // Readable: an admin still needs to see which provider the cluster trusts.
  await expect(page.getByTestId('sso-issuer')).toHaveValue(
    'https://login.microsoftonline.com/tenant/v2.0',
  );
  await expect(page.getByTestId('sso-status')).toContainText('Helm chart');
  await expect(page.getByTestId('sso-issuer')).toBeDisabled();
  await expect(page.getByTestId('sso-save')).toBeDisabled();
  await expect(page.getByTestId('sso-remove')).toHaveCount(0);
});

test('a non-app-admin is refused', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  await page.goto('/admin/auth');
  await expect(page.getByTestId('sso-forbidden')).toBeVisible();
});
