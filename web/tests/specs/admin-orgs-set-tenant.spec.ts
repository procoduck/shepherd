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
  api.seed({ orgs: [{ ...org }] }); // a copy: the mock mutates what it is seeded with
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
  // Read-only, not disabled: it is there to be read, so it is not dimmed (#309).
  await expect(page.getByTestId('org-edit-tenant-set')).not.toBeEditable();
  await expect(page.getByTestId('org-edit-tenant-set')).toHaveCSS('opacity', '1');
  await page.getByLabel('Display name').fill('Renamed');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'updated' })).toBeVisible({
    timeout: 5_000,
  });
  expect(api.calls('AdminService/SetOrgTenantID')).toHaveLength(0);
});

// #302: the edit called UpdateOrg first and SetOrgTenantID second, so an
// invalid tenant ID was refused only AFTER the org fields — and their
// org.update audit row — had been written. The tenant step, the one that can
// refuse, now runs first: a refusal leaves the org untouched.
test('a refused tenant ID writes nothing: UpdateOrg is never called', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org }] }); // a copy: the mock mutates what it is seeded with
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  await page.getByLabel('Display name').fill('Renamed');
  await page.getByTestId('org-edit-tenant').pressSequentially('bad tenant/id');
  await page.getByRole('button', { name: 'Save' }).click();

  await expect(page.getByText(/outside the allowed set/)).toBeVisible({ timeout: 5_000 });
  expect(api.calls('AdminService/SetOrgTenantID')).toHaveLength(1);
  expect(api.calls('AdminService/UpdateOrg')).toHaveLength(0);
});

test('a retried edit after the tenant step succeeded does not resend the tenant', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org }] }); // a copy: the mock mutates what it is seeded with
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  // UpdateOrg fails transiently after the tenant step succeeded.
  await page.getByLabel('Display name').fill('Renamed');
  await page.getByTestId('org-edit-tenant').pressSequentially('prod-tenant');
  let failed = false;
  api.override('POST', '/shepherd.mgmt.v1.AdminService/UpdateOrg', (route) => {
    if (!failed) {
      failed = true;
      return route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ code: 'unavailable', message: 'try again' }),
      });
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ id: 'org-0001', name: 'prod-org', displayName: 'Renamed' }),
    });
  });
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('alert').filter({ hasText: /try again/i })).toBeVisible({
    timeout: 5_000,
  });

  // The tenant is now fixed in the open dialog; Save again only updates.
  await expect(page.getByTestId('org-edit-tenant-set')).toHaveValue('prod-tenant');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'updated' })).toBeVisible({
    timeout: 5_000,
  });
  expect(api.calls('AdminService/SetOrgTenantID')).toHaveLength(1);
});

// Review of #327: the tenant step updates the open dialog's org when it
// lands. Closing the dialog while it is in flight must keep it closed.
test('closing the dialog mid-save does not reopen it when the tenant step lands', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org }] }); // a copy: the mock mutates what it is seeded with
  api.delay('POST', '/shepherd.mgmt.v1.AdminService/SetOrgTenantID', 800);
  // Hold UpdateOrg too, so the window between the tenant step landing and the
  // save finishing (when a reopened dialog would show) is observable.
  api.delay('POST', '/shepherd.mgmt.v1.AdminService/UpdateOrg', 1500);
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  await page.getByTestId('org-edit-tenant').pressSequentially('prod-tenant');
  await page.getByRole('button', { name: 'Save' }).click();
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);

  // The tenant step lands, and the save continues to UpdateOrg.
  await expect.poll(() => api.calls('AdminService/UpdateOrg').length).toBe(1);
  // Checked once, not retried: a retrying assertion would wait out the held
  // UpdateOrg, whose onSuccess closes the dialog again anyway.
  expect(await page.getByRole('dialog').count()).toBe(0);
  await expect.poll(() => page.getByText('Organisation updated').count()).toBeGreaterThan(0);
  expect(await page.getByRole('dialog').count()).toBe(0);
});
