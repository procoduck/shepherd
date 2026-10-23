// strict-tenants.spec.ts — strict tenant mode: an org's destinations,
// bindings, tenant routes and pipelines may only use tenants it owns. An app
// admin turns it on in Admin → Organisations → Edit (SetOrgStrictTenants,
// refused with the preflight's list until nothing breaks the rule); the
// destination form then offers only the org's tenants, and the pipeline
// editor shows the tenant lint (an error for an unowned literal, a warning
// that does not block Save for a tenant it cannot read).
import { expect } from '@playwright/test';
import { OrgTenantKind } from '@/gen/shepherd/mgmt/v1/common_pb';
import { basicScenario, destination, pipeline } from '../fixtures/factories';
import { appAdmin, orgAdmin, orgEditor } from '../fixtures/personas';
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

test('an app admin is refused turning strict mode on until the preflight is clean, then turns it on', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [
      {
        ...org,
        tenants: [{ tenant: 'acme', kind: 'exact', is_default: true }],
        strict_violations: [
          {
            kind: 'destination',
            name: 'mimir-free',
            tenant: 'free',
            message:
              'destination "mimir-free" uses tenant "free", which the organisation does not own',
          },
        ],
      },
    ],
  });
  await page.goto('/admin/orgs');
  await expect(page.getByTestId('org-strict-prod-org')).toHaveText('off');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();

  const strict = page.getByTestId('org-strict-tenants');
  await strict.getByTestId('org-strict-tenants-toggle').click();
  await expect(strict.getByTestId('org-strict-tenants-violation')).toHaveText(
    'destination "mimir-free" uses tenant "free", which the organisation does not own',
  );
  await expect(strict.getByTestId('org-strict-tenants-toggle')).not.toBeChecked();
  expect(api.calls('AdminService/TenantPreflight').map((c) => c.body)).toEqual([
    expect.objectContaining({ orgId: 'org-0001' }),
  ]);

  // The admin fixes the destination; the preflight is now clean.
  api.seed({
    orgs: [{ ...org, tenants: [{ tenant: 'acme', kind: 'exact', is_default: true }] }],
  });
  await strict.getByTestId('org-strict-tenants-toggle').click();
  await expect(strict.getByTestId('org-strict-tenants-toggle')).toBeChecked();
  await expect(strict.getByTestId('org-strict-tenants-error')).toHaveCount(0);
  expect(api.calls('AdminService/SetOrgStrictTenants').at(-1)?.body).toEqual(
    expect.objectContaining({ orgId: 'org-0001', strictTenants: true }),
  );
});

test('an org without tenants is told it needs one first', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org, tenants: [] }] });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const strict = page.getByTestId('org-strict-tenants');
  await strict.getByTestId('org-strict-tenants-toggle').click();
  await expect(strict.getByTestId('org-strict-tenants-violation')).toContainText('owns no tenant');
});

test('the destination form offers only the org’s tenants in strict mode', async ({ page, api }) => {
  await api.loginAs({
    ...orgAdmin,
    orgs: orgAdmin.orgs.map((o) => ({
      ...o,
      tenantsRequired: true,
      tenants: [
        { tenant: 'acme', kind: OrgTenantKind.EXACT, isDefault: true },
        { tenant: 'acme-', kind: OrgTenantKind.PREFIX },
      ],
    })),
  });
  const s = basicScenario();
  api.seed({
    orgs: [
      {
        ...s.org,
        strict_tenants: true,
        tenants: [
          { tenant: 'acme', kind: 'exact', is_default: true },
          { tenant: 'acme-', kind: 'prefix' },
        ],
      },
    ],
    destinations: [],
  });
  await page.goto('/destinations');
  await page.getByRole('button', { name: /new|create|add destination/i }).click();
  const dialog = page.getByRole('dialog', { name: 'New destination' });
  await expect(dialog.getByTestId('destination-strict-tenants')).toContainText('acme, acme-…');
  await expect(dialog.locator('#destination-owned-tenants option')).toHaveCount(1);

  await dialog.getByLabel('Name', { exact: true }).fill('mimir-other');
  await dialog.getByLabel('URL', { exact: true }).fill('https://mimir.example.com/api/v1/push');
  await dialog.getByLabel(/Tenant ID/).fill('globex');
  await dialog.getByRole('button', { name: /create/i }).click();
  await expect(dialog.getByText(/Strict tenant mode: use one of this organisation/)).toBeVisible();
  expect(api.calls('DestinationService/CreateDestination')).toHaveLength(0);

  await dialog.getByLabel(/Tenant ID/).fill('acme-eu');
  await dialog.getByRole('button', { name: /create/i }).click();
  await expect(page.getByRole('cell', { name: 'acme-eu', exact: true })).toBeVisible();
});

test('the pipeline editor shows a tenant-lint warning without blocking Save', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-strict',
    org_id: s.org.id,
    name: 'writer',
    contents: 'prometheus.remote_write "out" {}',
  });
  api.seed({
    orgs: [s.org],
    pipelines: [p],
    validateResult: {
      valid: true,
      diagnostics: [
        {
          line: 1,
          col: 1,
          message: 'tenant set from a non-literal expression — cannot be verified in strict mode',
          stage: 1,
          warning: true,
        },
      ],
    },
  });
  await page.goto('/pipelines/pip-strict');
  await expect(page.getByTestId('editor-warning')).toContainText(
    'warning: tenant set from a non-literal expression',
  );
  await expect(page.locator('.cm-lint-marker-warning')).toHaveCount(1);
  await expect(page.getByRole('button', { name: 'Save' })).toBeEnabled();
});

test('a tenant-lint warning in the wizard preview does not block Create', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [destination({ id: 'dst-prom', name: 'prom-prod', type: 'prometheus' })],
  });
  api.override('POST', '/shepherd.mgmt.v1.WizardService/RenderWizard', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        contents: 'prometheus.scrape "app" {}\n',
        matchers: ['role="metrics"'],
        valid: true,
        diagnostics: [
          {
            line: 1,
            col: 1,
            message: 'tenant set from a non-literal expression — cannot be verified in strict mode',
            stage: 1,
            warning: true,
          },
        ],
        matchedCollectors: [],
      }),
    });
  });

  await page.goto('/wizards');
  await page.getByRole('link', { name: /app observability|start|begin/i }).click();
  await page.getByLabel('Metrics endpoint URL').fill('http://myapp:9090/metrics');
  await page.getByLabel('Job label').fill('my-app');
  await page.getByRole('button', { name: /next|continue/i }).click();
  await page.getByRole('button', { name: /next|continue/i }).click();
  await page.getByLabel('Metrics destination').selectOption('prom-prod');
  await page.getByRole('button', { name: /next|continue/i }).click();
  await page.getByRole('button', { name: /next|continue/i }).click();
  await page.getByLabel('Pipeline name').fill('strict-wizard');

  await expect(page.getByText('No problems')).toBeVisible();
  await expect(page.getByRole('button', { name: /create pipeline/i })).toBeEnabled();
});
