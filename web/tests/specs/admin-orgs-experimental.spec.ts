// admin-orgs-experimental.spec.ts — #114: an app admin can toggle an org's
// "Allow experimental components" setting from the Organisations page, and it
// rides through on UpdateOrg.
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
  allow_experimental_components: false,
};

test('an app admin toggles experimental components on for an org', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [org] });
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: 'Edit prod-org' }).click();

  const toggle = page.getByTestId('org-allow-experimental');
  await expect(toggle).not.toBeChecked();
  await toggle.check();
  await page.getByRole('button', { name: 'Save' }).click();

  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'updated' })).toBeVisible({
    timeout: 5_000,
  });

  const calls = api.calls('AdminService/UpdateOrg');
  expect(calls).toHaveLength(1);
  const body = calls[0].body as Record<string, unknown>;
  expect(body.orgId).toBe('org-0001');
  expect(body.allowExperimentalComponents).toBe(true);
});
