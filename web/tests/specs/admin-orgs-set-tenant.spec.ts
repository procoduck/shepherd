// admin-orgs-set-tenant.spec.ts — #204: an org created without a tenant ID
// could never be given one in the UI. The Edit dialog now offers the field
// while it is empty (set-once, through SetOrgTenantID) and shows it read-only
// once it is set.
import { expect } from '@playwright/test';
import { appAdmin } from '../fixtures/personas';
import { test } from '../fixtures/test';

const org = {
  id: 'org-0001',
  name: 'prod-org',
  display_name: 'Production Org',
  admin_group_id: 'admins',
  reader_group_id: '',
  created_at: '2026-09-01T09:00:00Z',
  updated_at: '2026-09-01T09:00:00Z',
};

test('an app admin sets the tenant ID of an org created without one', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [org] });
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  await expect(page.getByTestId('org-edit-tenant-set')).toHaveCount(0);
  await page.getByTestId('org-edit-tenant').fill('prod-tenant');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'updated' })).toBeVisible({
    timeout: 5_000,
  });

  const calls = api.calls('AdminService/SetOrgTenantID');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ orgId: 'org-0001', tenantId: 'prod-tenant' });

  // Reopened, the tenant is now fixed.
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  await expect(page.getByTestId('org-edit-tenant-set')).toHaveValue('prod-tenant');
  await expect(page.getByTestId('org-edit-tenant')).toHaveCount(0);
});

test('an org that already has a tenant ID shows it read-only and never resends it', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org, tenant_id: 'existing' }] });
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  await expect(page.getByTestId('org-edit-tenant-set')).toHaveValue('existing');
  await expect(page.getByTestId('org-edit-tenant-set')).toBeDisabled();
  await page.getByLabel('Display name').fill('Renamed');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'updated' })).toBeVisible({
    timeout: 5_000,
  });
  expect(api.calls('AdminService/SetOrgTenantID')).toHaveLength(0);
});
