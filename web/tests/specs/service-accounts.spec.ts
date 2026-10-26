/**
 * Service accounts (/service-accounts).
 *
 * Machine-caller identities: org-scoped at a viewer, editor or admin tier;
 * editor and admin are capability-scoped (propose/apply), a viewer carries no
 * capability. Every procedure is org admin, so a viewer is denied the
 * route entirely. This drives create (with the one-time secret reveal) and
 * revoke over the real generated ServiceAccountService client and the mocks.
 */
import { basicScenario } from '../fixtures/factories';
import { appAdmin, orgAdmin, viewer } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('creates a service account, reveals the secret once, and revokes it', async ({
  page,
  api,
}) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], serviceAccounts: [] });
  await api.loginAs(appAdmin);
  await page.goto('/service-accounts');

  const panel = page.getByTestId('service-accounts');
  await expect(panel.getByRole('heading', { name: 'Service accounts' })).toBeVisible();
  await expect(panel.getByText('No service accounts yet.')).toBeVisible();

  await page.getByTestId('sa-new').click();
  await page.getByTestId('sa-name').fill('ci-deployer');
  await page.getByTestId('sa-capability').selectOption('apply');
  await page.getByTestId('sa-role').selectOption({ label: 'Admin' });
  await page.getByRole('button', { name: 'Create', exact: true }).click();

  // The one-time secret dialog shows the secret; the account lands in the table.
  await expect(page.getByTestId('sa-secret')).toContainText('sa-secret-');
  expect(api.calls('/shepherd.mgmt.v1.ServiceAccountService/CreateServiceAccount').length).toBe(1);
  await page.getByRole('button', { name: 'Done' }).click();

  await expect(panel.getByText('ci-deployer')).toBeVisible();
  await expect(panel.getByText('apply')).toBeVisible();
  await expect(panel.getByText('Admin', { exact: true })).toBeVisible();

  // Revoke it.
  await page.getByTestId('sa-revoke-ci-deployer').click();
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click();
  await expect(panel.getByText('revoked')).toBeVisible();
  expect(api.calls('/shepherd.mgmt.v1.ServiceAccountService/RevokeServiceAccount').length).toBe(1);
});

// #362: a viewer-tier service account carries no capability. Picking Viewer
// hides the capability select, the request names none, and the table shows the
// role alone.
test('creates a viewer-tier service account with no capability', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], serviceAccounts: [] });
  await api.loginAs(orgAdmin);
  await page.goto('/service-accounts');

  await page.getByTestId('sa-new').click();
  await page.getByTestId('sa-name').pressSequentially('dashboards-reader');
  await expect(page.getByTestId('sa-capability')).toBeVisible();
  await page.getByTestId('sa-capability').selectOption('apply');
  await page.getByTestId('sa-role').selectOption({ label: 'Viewer' });
  await expect(page.getByTestId('sa-capability')).toHaveCount(0);
  await page.getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByTestId('sa-secret')).toContainText('sa-secret-');
  const [call] = api.calls('/shepherd.mgmt.v1.ServiceAccountService/CreateServiceAccount');
  const sent = call.body as Record<string, unknown>;
  expect(sent.role).toBe('ROLE_VIEWER');
  expect(sent.capability ?? '').toBe('');
  await page.getByRole('button', { name: 'Done' }).click();

  const grant = page.getByTestId('sa-grant-dashboards-reader');
  await expect(grant).toHaveText('Viewer');
  await expect(grant.locator('code')).toHaveCount(1);
});

test('a viewer is denied the route (org admin only)', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], serviceAccounts: [] });
  await api.loginAs(viewer);
  await page.goto('/service-accounts');

  // The route guard denies a non-admin before the page mounts.
  await expect(page.getByTestId('service-accounts')).toHaveCount(0);
});
