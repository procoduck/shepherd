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

// 0.20 walkthrough: after a refused strict toggle, Save closed the dialog
// with a plain "Organisation updated" toast while strict stayed off. The
// toast now says strict mode was not enabled, and why.
test('Save after a refused strict toggle says strict mode was not enabled', async ({
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
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const strict = page.getByTestId('org-strict-tenants');
  await strict.getByTestId('org-strict-tenants-toggle').click();
  await expect(strict.getByTestId('org-strict-tenants-violation')).toHaveCount(1);
  await expect(strict.getByTestId('org-strict-tenants-toggle')).not.toBeChecked();

  await page.getByRole('button', { name: 'Save' }).click();
  await expect(
    page.getByText(
      'Organisation updated — strict tenant mode was not enabled: 1 item uses a tenant this organisation does not own',
    ),
  ).toBeVisible();
  await expect(page.getByText('Organisation updated', { exact: true })).toHaveCount(0);
  await expect(page.getByTestId('org-strict-prod-org')).toHaveText('off');
});

test('Save after a strict toggle that took reads as a plain success', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org, tenants: [{ tenant: 'acme', kind: 'exact', is_default: true }] }] });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  await page.getByTestId('org-strict-tenants-toggle').click();
  await expect(page.getByTestId('org-strict-tenants-toggle')).toBeChecked();
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Organisation updated', { exact: true })).toBeVisible();
  await expect(page.getByText(/strict tenant mode was not/)).toHaveCount(0);
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
  // The preview's verdict is the pipeline editor's: ValidatePipeline on the
  // rendered text, which reports the same tenant-lint warning.
  api.seed({
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

  // The preview counts the warning as the pipeline editor does (v0.20.0
  // follow-up): "No problems" beside a warning read as a clean bill.
  await expect(page.getByTestId('editor-warning-count')).toHaveText('1 warning');
  await expect(page.getByText('No problems')).toHaveCount(0);
  await expect(page.getByRole('button', { name: /create pipeline/i })).toBeEnabled();
});

// 0.21.0-rc.1 walkthrough: on a strict org the self-monitoring preview said
// "No problems" although the render returned a wizard note and
// ValidatePipeline on the same text returned a Stage-3 version warning. The
// render's own diagnostics were empty, and they were all the count read.
test("the wizard preview counts its own notes and the editor's Stage-3 warnings", async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  const note =
    'Log collection was requested but no logs destination was set, so it was left out — name a logs destination to include it.';
  const version =
    'a collector in this org runs Alloy "not-a-version", which is not a release version; checked against v1.21.2 only';
  api.seed({
    orgs: [s.org],
    destinations: [destination({ id: 'dst-prom', name: 'prom-prod', type: 'prometheus' })],
    validateResult: {
      valid: true,
      diagnostics: [{ line: 0, col: 0, message: version, stage: 3, warning: true }],
    },
  });
  api.override('POST', '/shepherd.mgmt.v1.WizardService/RenderWizard', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        contents: 'prometheus.scrape "app" {}\n',
        matchers: ['cluster="fake-eu-1"', 'role="metrics"'],
        valid: true,
        diagnostics: [],
        matchedCollectors: [],
        warnings: [note],
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

  await expect(page.getByTestId('editor-warning-count')).toHaveText('2 warnings');
  await expect(page.getByText('No problems')).toHaveCount(0);
  // Each warning's text, not just the count.
  await expect(page.getByTestId('wizard-warnings')).toContainText(note);
  await expect(page.getByTestId('editor-warning')).toHaveText(`warning: ${version}`);
  await expect(page.getByRole('button', { name: /create pipeline/i })).toBeEnabled();
});

test('adding a tenant shows the warnings the server returns about pipelines it cannot read', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org, tenants: [] }] });
  const warning =
    'org "other" has 1 enabled pipeline(s) setting tenants the check cannot read (computed values, external modules, or too large); it cannot tell whether they write to "acme"';
  api.override('POST', '/shepherd.mgmt.v1.AdminService/AddOrgTenant', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id: org.id,
        name: org.name,
        displayName: org.display_name,
        adminGroupId: org.admin_group_id,
        tenants: [{ tenant: 'acme', kind: 'ORG_TENANT_KIND_EXACT', isDefault: true }],
        warnings: [warning],
      }),
    });
  });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-tenants');
  await editor.getByTestId('org-tenant-new').pressSequentially('acme');
  await editor.getByTestId('org-tenant-add').click();
  await expect(editor.getByTestId('org-tenant-warning')).toHaveText(warning);
});
