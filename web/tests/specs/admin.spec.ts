import { org } from '../fixtures/factories';
import { appAdmin, orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('admin orgs page shows orgs', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001', name: 'test-org', display_name: 'Test Org' });
  api.seed({ orgs: [o] });
  await page.goto('/admin/orgs');
  await expect(page.getByText('test-org')).toBeVisible();
});

test('admin clusters page shows clusters', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ clusters: [{ id: 'cl-0001', name: 'prod-eu-1', org_id: null }] });
  await page.goto('/admin/clusters');
  await expect(page.getByText('prod-eu-1')).toBeVisible();
});

test('admin tokens page shows tokens', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    agentTokens: [
      {
        id: 'tok-0001',
        name: 'e2e',
        status: 'active',
        created_by: 'admin',
        created_at: '2026-08-17T09:00:00Z',
      },
    ],
  });
  await page.goto('/admin/tokens');
  await expect(page.getByText('e2e')).toBeVisible();
  await expect(page.getByText('active')).toBeVisible();
});

test('app admin can create an org', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [] });
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: /new organisation/i }).click();
  await page.getByLabel('Name', { exact: true }).fill('new-org');
  await page.getByLabel('Display name').fill('New Org');
  await page.getByLabel(/admin group id/i).fill('11111111-1111-1111-1111-111111111111');
  await page.getByRole('dialog').getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText('new-org')).toBeVisible();
  await expect(page.getByRole('cell', { name: 'New Org' })).toBeVisible();
  const calls = api.calls('AdminService/CreateOrg');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ name: 'new-org', displayName: 'New Org' });
});

test('deleting a non-empty org surfaces the server error', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001', name: 'test-org', display_name: 'Test Org' });
  api.seed({
    orgs: [o],
    clusters: [{ id: 'cl-0001', name: 'prod-eu-1', org_id: 'org-0001' }],
  });
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: 'Delete test-org' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete', exact: true }).click();

  await expect(page.getByText(/cannot delete/i)).toBeVisible();
  // The org is still listed — delete did not go through.
  await expect(page.getByText('test-org')).toBeVisible();
});

test('non-app-admin cannot see org write affordances', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  api.seed({ orgs: [org({ id: 'org-0001', name: 'prod-org', display_name: 'Production Org' })] });
  await page.goto('/admin/orgs');

  await expect(page.getByText('prod-org')).toBeVisible();
  await expect(page.getByRole('button', { name: /new organisation/i })).toHaveCount(0);
});

test('claiming an unclaimed cluster assigns it to an org', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001', name: 'prod-org', display_name: 'Production Org' })],
    clusters: [{ id: 'cl-0001', name: 'prod-eu-1', org_id: null }],
  });
  await page.goto('/admin/clusters');

  await expect(page.getByText('prod-eu-1')).toBeVisible();
  await page.getByRole('button', { name: 'Claim' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Organisation').selectOption('org-0001');
  await dialog.getByRole('button', { name: 'Claim' }).click();

  await expect(page.getByText('Production Org')).toBeVisible();
  const calls = api.calls('AdminService/ClaimCluster');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ cluster: 'prod-eu-1', orgId: 'org-0001' });
});

test('claimed clusters are visible by default (unclaimed filter is opt-in)', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001', name: 'prod-org', display_name: 'Production Org' })],
    clusters: [{ id: 'cl-0001', name: 'prod-eu-1', org_id: 'org-0001' }],
  });
  await page.goto('/admin/clusters');

  await expect(page.getByText('prod-eu-1')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Unclaim' })).toBeVisible();

  await page.getByLabel('Unclaimed only').check();
  await expect(page.getByText('No unclaimed clusters.')).toBeVisible();
});

test('revoking a token updates its status and cannot be re-shown', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    agentTokens: [
      {
        id: 'tok-0001',
        name: 'e2e',
        status: 'active',
        created_by: 'admin',
        created_at: '2026-08-17T09:00:00Z',
      },
    ],
  });
  await page.goto('/admin/tokens');

  await page.getByRole('button', { name: 'Revoke' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click();

  await expect(page.getByText('revoked', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Revoke' })).toHaveCount(0);
  const calls = api.calls('AdminService/RevokeAgentToken');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ id: 'tok-0001' });
});

test('creating a token shows the one-time secret and does not log it', async ({ page, api }) => {
  const logs: string[] = [];
  page.on('console', (msg) => logs.push(msg.text()));

  await api.loginAs(appAdmin);
  api.seed({ agentTokens: [] });
  await page.goto('/admin/tokens');

  await page.getByRole('button', { name: /new token/i }).click();
  const createDialog = page.getByRole('dialog');
  await createDialog.getByLabel('Name').fill('prod-eu-1-agent');
  await createDialog.getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText(/only time/i)).toBeVisible();
  await expect(page.getByText('one-time-secret-value')).toBeVisible();

  await page.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByText('one-time-secret-value')).toHaveCount(0);

  expect(logs.some((l) => l.includes('one-time-secret-value'))).toBe(false);
});
