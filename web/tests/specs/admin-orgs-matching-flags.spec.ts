// admin-orgs-matching-flags.spec.ts — #139/#144 regression: UpdateOrg replaces
// every field from the request, so editing an org on the Organisations page
// (which has no control for the two matching flags yet) must send their
// current values back. Before the fix, renaming an org silently switched both
// allow_label_matching and allow_local_attribute_matching off.
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
  allow_label_matching: true,
  allow_local_attribute_matching: true,
};

test('editing an org keeps its attribute-matching flags on', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [org] });
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const displayName = page.getByLabel('Display name');
  await displayName.fill('Production Org (renamed)');
  await page.getByRole('button', { name: 'Save' }).click();

  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'updated' })).toBeVisible({
    timeout: 5_000,
  });

  const calls = api.calls('AdminService/UpdateOrg');
  expect(calls).toHaveLength(1);
  const body = calls[0].body as Record<string, unknown>;
  expect(body.displayName).toBe('Production Org (renamed)');
  expect(body.allowLabelMatching).toBe(true);
  expect(body.allowLocalAttributeMatching).toBe(true);
});
