/**
 * Service accounts (/service-accounts).
 *
 * Machine-caller identities: org-scoped, capability-scoped (propose/apply) at
 * an editor/admin tier. Every procedure is org-admin, so a viewer is denied the
 * route entirely. This drives create (with the one-time secret reveal) and
 * revoke over the real generated ServiceAccountService client and the mocks.
 */
import { basicScenario } from '../fixtures/factories';
import { appAdmin, reader } from '../fixtures/personas';
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
  await page.getByTestId('sa-role').selectOption('admin');
  await page.getByRole('button', { name: 'Create', exact: true }).click();

  // The one-time secret dialog shows the secret; the account lands in the table.
  await expect(page.getByTestId('sa-secret')).toContainText('sa-secret-');
  expect(api.calls('/shepherd.mgmt.v1.ServiceAccountService/CreateServiceAccount').length).toBe(1);
  await page.getByRole('button', { name: 'Done' }).click();

  await expect(panel.getByText('ci-deployer')).toBeVisible();
  await expect(panel.getByText('apply')).toBeVisible();
  await expect(panel.getByText('admin', { exact: true })).toBeVisible();

  // Revoke it.
  await page.getByTestId('sa-revoke-ci-deployer').click();
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click();
  await expect(panel.getByText('revoked')).toBeVisible();
  expect(api.calls('/shepherd.mgmt.v1.ServiceAccountService/RevokeServiceAccount').length).toBe(1);
});

test('a viewer is denied the route (org-admin only)', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], serviceAccounts: [] });
  await api.loginAs(reader);
  await page.goto('/service-accounts');

  // The route guard denies a non-admin before the page mounts.
  await expect(page.getByTestId('service-accounts')).toHaveCount(0);
});
