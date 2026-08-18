import { org } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
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
