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

test('shows an alert, not the forbidden message, when settings fail to load for another reason', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.failNext('POST', '/shepherd.mgmt.v1.AdminService/GetOidcSettings', 503, 'unavailable');
  await page.goto('/admin/auth');

  await expect(page.getByTestId('sso-load-error')).toBeVisible({ timeout: 5000 });
  await expect(page.getByTestId('sso-forbidden')).toHaveCount(0);
});

test('a non-app-admin is refused', async ({ page, api }) => {
  // W6-S7: routeManifest's requiredRole ('app-admin' for admin/*) denies the
  // direct navigation before AdminAuthPage ever mounts, so its own
  // 'sso-forbidden' banner is unreachable now — the guard redirects to '/'
  // (and shows a route-denied element on the way) instead of letting the
  // page render and refuse. See route-guard.spec.ts for the full matrix.
  await api.loginAs(orgAdmin);
  await page.goto('/admin/auth');
  await expect(async () => {
    const onRoot = new URL(page.url()).pathname === '/';
    const hasDeniedBanner = await page
      .getByTestId('route-denied')
      .isVisible()
      .catch(() => false);
    expect(onRoot || hasDeniedBanner).toBe(true);
  }).toPass({ timeout: 5000 });
});

test('shows the collector-OIDC gate as off when no agent audience is set', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');

  await expect(
    page.getByRole('heading', { name: 'Collector authentication (OIDC)' }),
  ).toBeVisible();
  await expect(page.getByTestId('agent-oidc-off')).toBeVisible();
  await expect(page.getByTestId('agent-audience')).toHaveCount(0);
});

test('surfaces the collector-OIDC gate read-only when configured', async ({ page, api }) => {
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
      status_message: '',
      updated_by: '',
      agent_audience: 'api://shepherd-collectors',
      agent_required_role: 'Collector.Poll',
      agent_required_scope: '',
      agent_org_role_prefix: 'shepherd-org:',
    },
  });
  await page.goto('/admin/auth');

  await expect(page.getByTestId('agent-audience')).toHaveText('api://shepherd-collectors');
  const gate = page.getByTestId('collector-oidc-gate');
  await expect(gate).toContainText('Collector.Poll');
  // Empty required scope renders as "any", not blank.
  await expect(gate).toContainText('any');
  // Mode 2 row reflects the role-prefix opt-in.
  await expect(page.getByTestId('agent-org-mode')).toContainText('role prefix shepherd-org:');
  await expect(page.getByTestId('agent-oidc-off')).toHaveCount(0);
});
