// admin-org-tenants.spec.ts — an organisation can own several tenants and
// tenant prefixes. Admin → Organisations lists them, and the Edit dialog's
// tenants editor adds, removes and switches the default, each through its own
// app-admin RPC (AddOrgTenant / RemoveOrgTenant / SetDefaultOrgTenant),
// independent of Save. A removal the server refuses shows what still uses it.
import { expect } from '@playwright/test';
import { TenantRouteApplyStatus, TenantRouteStatus } from '@/gen/shepherd/mgmt/v1/tenant_route_pb';
import { appAdmin } from '../fixtures/personas';
import { test } from '../fixtures/test';

const org = {
  id: 'org-0001',
  name: 'prod-org',
  display_name: 'Production Org',
  admin_group_id: 'admins',
  viewer_group_id: '',
  created_at: '2026-09-01T09:00:00Z',
  updated_at: '2026-09-01T09:00:00Z',
};

test('lists each org’s tenants and prefixes, with its default', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [
      {
        ...org,
        tenants: [
          { tenant: 'acme', kind: 'exact', is_default: true },
          { tenant: 'acme-team-', kind: 'prefix' },
        ],
      },
      { ...org, id: 'org-0002', name: 'bare-org', tenants: [] },
    ],
  });
  await page.goto('/admin/orgs');

  const cell = page.getByTestId('org-tenants-prod-org');
  await expect(cell).toContainText('acme (default)');
  await expect(cell).toContainText('acme-team-*');
  await expect(page.getByText('none — no routes possible')).toBeVisible();
});

test('an app admin adds an exact tenant and a prefix, and switches the default', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org, tenants: [] }] }); // a copy: the mock mutates what it is seeded with
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-tenants');
  await expect(editor.getByTestId('org-tenants-empty')).toBeVisible();

  // The first exact tenant is the default unless unticked.
  await expect(editor.getByTestId('org-tenant-new-default')).toBeChecked();
  await editor.getByTestId('org-tenant-new').pressSequentially('acme');
  await editor.getByTestId('org-tenant-add').click();
  await expect(editor.getByTestId('org-tenant-row-acme')).toContainText('default');

  await editor.getByTestId('org-tenant-new').pressSequentially('acme-eu');
  await editor.getByTestId('org-tenant-new').press('Enter'); // adds, does not submit the dialog
  await expect(editor.getByTestId('org-tenant-row-acme-eu')).toBeVisible();
  await expect(page.getByRole('dialog')).toBeVisible();

  await editor.getByTestId('org-tenant-new-kind').selectOption({ label: 'Prefix' });
  await expect(editor.getByTestId('org-tenant-new-default')).toHaveCount(0);
  await expect(editor.getByTestId('org-tenant-prefix-hint')).toContainText('pay-');
  await editor.getByTestId('org-tenant-new').pressSequentially('acme-team-');
  await editor.getByTestId('org-tenant-add').click();
  await expect(editor.getByTestId('org-tenant-row-acme-team-')).toContainText('acme-team-*');

  await editor.getByTestId('org-tenant-default-acme-eu').click();
  await expect(editor.getByTestId('org-tenant-row-acme-eu')).toContainText('default');
  await expect(editor.getByTestId('org-tenant-default-acme')).toBeVisible();

  const adds = api.calls('AdminService/AddOrgTenant');
  expect(adds.map((c) => c.body)).toEqual([
    expect.objectContaining({ tenant: 'acme', kind: 'ORG_TENANT_KIND_EXACT', makeDefault: true }),
    expect.objectContaining({ tenant: 'acme-eu', kind: 'ORG_TENANT_KIND_EXACT' }),
    expect.objectContaining({ tenant: 'acme-team-', kind: 'ORG_TENANT_KIND_PREFIX' }),
  ]);
  expect(api.calls('AdminService/SetDefaultOrgTenant')[0].body).toMatchObject({
    orgId: 'org-0001',
    tenant: 'acme-eu',
  });
  // Tenant changes are not part of Save.
  expect(api.calls('AdminService/UpdateOrg')).toHaveLength(0);
});

test('an invalid tenant is refused in the form, an overlap by the server', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [
      { ...org, tenants: [] },
      {
        ...org,
        id: 'org-0002',
        name: 'globex-org',
        tenants: [{ tenant: 'globex-', kind: 'prefix' }],
      },
    ],
  });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-tenants');

  await editor.getByTestId('org-tenant-new').pressSequentially('bad tenant/id');
  await editor.getByTestId('org-tenant-add').click();
  await expect(editor.getByTestId('org-tenant-error')).toContainText('no slashes or spaces');
  expect(api.calls('AdminService/AddOrgTenant')).toHaveLength(0);

  await editor.getByTestId('org-tenant-new').fill('');
  await editor.getByTestId('org-tenant-new').pressSequentially('globex-eu');
  await editor.getByTestId('org-tenant-add').click();
  await expect(editor.getByTestId('org-tenant-error')).toContainText('globex-org');
  await expect(editor.getByTestId('org-tenants-empty')).toBeVisible();
});

test('removing a tenant still in use shows what uses it; an unused one goes', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [
      {
        ...org,
        tenants: [
          { tenant: 'acme', kind: 'exact', is_default: true },
          { tenant: 'acme-eu', kind: 'exact' },
          { tenant: 'acme-team-', kind: 'prefix' },
        ],
      },
    ],
    tenantRoutes: [
      {
        id: 'tr-0001',
        org_id: 'org-0001',
        tenant_id: 'acme-team-pay',
        kind: 'otlp',
        segment: 'acme-team-pay-x1',
        status: TenantRouteStatus.ACTIVE,
        apply_status: TenantRouteApplyStatus.APPLIED,
        gateway_mode: 'managed',
        gateway_name: 'gw',
        created_at: '2026-09-17T09:00:00Z',
        updated_at: '2026-09-17T09:00:00Z',
      },
    ],
  });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-tenants');

  await editor.getByTestId('org-tenant-remove-acme-team-').click();
  await expect(editor.getByTestId('org-tenant-error')).toContainText('still in use');
  await expect(editor.getByTestId('org-tenant-error')).toContainText('acme-team-pay-x1');
  await expect(editor.getByTestId('org-tenant-row-acme-team-')).toBeVisible();

  await editor.getByTestId('org-tenant-remove-acme-eu').click();
  await expect(editor.getByTestId('org-tenant-row-acme-eu')).toHaveCount(0);
  await expect(editor.getByTestId('org-tenant-error')).toHaveCount(0);
  expect(api.calls('AdminService/RemoveOrgTenant')).toHaveLength(2);
});

test('a new org can start with a default tenant', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [] });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'New organisation' }).click();
  await page.getByLabel('Name', { exact: true }).pressSequentially('acme-org');
  await page.getByLabel('Display name').pressSequentially('Acme');
  await page.getByLabel('Admin group ID').pressSequentially('acme-admins');
  await page.getByLabel('Default tenant').pressSequentially('acme');
  await page.getByRole('dialog').getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page.getByTestId('org-tenants-acme-org')).toContainText('acme (default)');
  expect(api.calls('AdminService/CreateOrg')[0].body).toMatchObject({ tenantId: 'acme' });
});
