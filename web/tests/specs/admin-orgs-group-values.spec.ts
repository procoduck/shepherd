// admin-orgs-group-values.spec.ts — M6 (2026-10-08 walkthrough): the org
// form's group fields carried GUID placeholders, reading as "only a GUID
// works". Group values are whatever the IdP puts in the groups claim — a GUID
// on Entra, usually a name or path elsewhere (docs/spec.md §7.1a) — and the
// server accepts any non-empty value, so a group name must save.
import { expect } from '@playwright/test';
import { appAdmin } from '../fixtures/personas';
import { test } from '../fixtures/test';

test('the org form accepts a group name and does not suggest a GUID only', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [] });
  await page.goto('/admin/orgs');

  await page
    .getByRole('button', { name: /new organisation/i })
    .first()
    .click();
  const dialog = page.getByRole('dialog');
  for (const field of [/admin group id/i, /editor group id/i, /viewer group id/i]) {
    await expect(dialog.getByLabel(field)).not.toHaveAttribute('placeholder', /^[0-9a-f-]{36}$/);
  }
  await expect(dialog).toContainText('usually its name or path on other providers');

  await dialog.getByLabel('Name', { exact: true }).fill('prod-org');
  await dialog.getByLabel('Display name').fill('Production Org');
  await dialog.getByLabel(/admin group id/i).fill('platform-admins');
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'created' })).toBeVisible({
    timeout: 5_000,
  });

  const calls = api.calls('AdminService/CreateOrg');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ adminGroupId: 'platform-admins' });
});
