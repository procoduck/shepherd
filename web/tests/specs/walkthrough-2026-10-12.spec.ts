// walkthrough-2026-10-12.spec.ts — findings from the 2026-10-12 v0.16 kind
// walkthrough that are not layout (layout-1024.spec.ts holds the toolbar).
import type { Route } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { basicScenario, collector, destination, pipeline } from '../fixtures/factories';
import { appAdmin, orgAdmin, orgEditor, viewer } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { expect, test } from '../fixtures/test';

const fulfillJSON = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

const graph = {
  kind: 'alloy-graph/v1',
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    {
      id: 'n1',
      component: 'prometheus.exporter.self',
      label: 'self',
      position: { x: 100, y: 100 },
      props: {},
      disabled: false,
      notes: '',
    },
  ],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-visual-builder' },
};

function seedVisual(api: { seed: (p: Record<string, unknown>) => void }) {
  const s = basicScenario();
  const p = {
    ...pipeline({ id: 'pip-vis', name: 'viewed', source: 'visual' }),
    wizard_state: graph,
  };
  api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [p] });
  return p;
}

// M2: the #297 banner read the collector's load error from ListCollectors,
// which never carried it, and ended in an empty "Error:".
test('the rejection banner never ends in an empty "Error:"', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: s.pipelines,
    collectors: [
      collector({
        id: 'col-rej',
        cluster: 'prod-eu-1',
        role: 'metrics',
        remote_config_status: 'FAILED',
      }),
    ],
    reconciliation: {
      findings: [
        {
          kind: 'served_config_rejected',
          sources: ['served', 'observed'],
          summary:
            'Collector rejected in pipeline "ui-enabled" (line 5 of its text, line 40 of the merged config); the collector is still running its previous config.',
          pipeline_name: 'ui-enabled',
          stale: false,
        },
      ],
    },
  });
  api.override('POST', '/shepherd.mgmt.v1.PipelineService/PreviewMatches', (route) =>
    fulfillJSON(route, { collectors: [{ id: 'col-rej', cluster: 'prod-eu-1', role: 'metrics' }] }),
  );
  await page.goto('/pipelines/pip-0001');
  const banner = page.getByTestId('pipeline-rejected-by');
  await expect(banner).toContainText('line 5 of its text');
  await expect(banner).not.toContainText('Error:');
});

test('a viewer denied a wizard does not ask for its schema', async ({ page, api }) => {
  await api.loginAs(viewer);
  api.seed({ orgs: [basicScenario().org] });
  await page.goto('/wizards/app-observability');
  await expect(page.getByTestId('route-denied')).toBeVisible();
  await api.idle();
  expect(api.calls('WizardService/GetWizardSchema')).toHaveLength(0);
});

test.describe('the New organisation dialog', () => {
  const inUse =
    "tenant_id is in use by another organisation's destination; an org's tenant identity must not be one another org already writes under";

  test('shows a refusal as a sentence, clears it on edit, and Cancel resets the form', async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    api.seed({ orgs: [basicScenario().org] });
    api.override('POST', '/shepherd.mgmt.v1.AdminService/CreateOrg', (route) =>
      fulfillJSON(route, { code: 'failed_precondition', message: inUse }, 400),
    );
    await page.goto('/admin/orgs');
    await page.getByRole('button', { name: 'New organisation' }).click();
    const dialog = page.getByRole('dialog', { name: 'New organisation' });
    await dialog.getByLabel('Name', { exact: true }).pressSequentially('acme-org');
    await dialog.getByLabel('Display name').pressSequentially('Acme');
    await dialog.getByLabel('Admin group ID').pressSequentially('acme-admins');
    await dialog.getByLabel(/Default tenant/).pressSequentially('acme');
    await dialog.getByRole('button', { name: 'Create' }).click();

    const error = dialog.getByTestId('form-error');
    await expect(error).toContainText('Tenant ID is in use by another organisation');
    await expect(error).not.toContainText('Tenant_id');

    await dialog.getByLabel(/Default tenant/).pressSequentially('-2');
    await expect(error).toHaveCount(0);

    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await page.getByRole('button', { name: 'New organisation' }).click();
    await expect(dialog.getByLabel('Name', { exact: true })).toHaveValue('');
    await expect(dialog.getByLabel(/Default tenant/)).toHaveValue('');
  });
});

test.describe('the destination dialog and table', () => {
  test('a refused URL is focused, so its error is on screen', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    api.seed({ orgs: [basicScenario().org], destinations: [] });
    await page.goto('/destinations');
    await page.getByRole('button', { name: /new|create|add destination/i }).click();
    const dialog = page.getByRole('dialog', { name: 'New destination' });
    await dialog.getByLabel('Name', { exact: true }).pressSequentially('mimir-plain');
    await dialog
      .getByLabel('URL', { exact: true })
      .pressSequentially('http://mimir.example.com/api/v1/push');
    await dialog.getByRole('button', { name: /TLS options/ }).click();
    await dialog.getByLabel(/Server name/).pressSequentially('mimir.internal.example');
    await dialog.getByRole('button', { name: /create/i }).click();

    // The field's accessible name gains its error text, hence the prefix.
    await expect(dialog.getByRole('textbox', { name: /^URL/ })).toBeFocused();
    await expect(dialog.getByText('TLS options need an https:// URL')).toBeInViewport();
  });

  test('the table says which destinations have TLS options', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    api.seed({
      orgs: [basicScenario().org],
      destinations: [
        destination({
          id: 'dst-tls',
          name: 'mimir-mtls',
          url: 'https://mimir.example.com/api/v1/push',
          extra: { tls: { server_name: 'mimir.internal.example' } },
        } as Record<string, unknown>),
        destination({ id: 'dst-plain', name: 'prom-plain' }),
      ],
    });
    await page.goto('/destinations');
    const badges = page.getByTestId('destination-tls-badge');
    await expect(badges).toHaveCount(1);
    await expect(
      page.getByRole('row', { name: /mimir-mtls/ }).getByTestId('destination-tls-badge'),
    ).toBeVisible();
  });
});

test('a disabled Continue says which required fields are missing', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  api.seed({ orgs: [basicScenario().org] });
  await page.goto('/wizards/app-observability');
  const blocked = page.getByTestId('wizard-continue-blocked');
  await expect(page.getByRole('button', { name: 'Continue' })).toBeDisabled();
  await expect(blocked).toContainText('Metrics endpoint URL');
  await expect(blocked).toContainText('Job label');
  await page.getByLabel(/Metrics endpoint URL/).fill('http://app:9090/metrics');
  await expect(blocked).not.toContainText('Metrics endpoint URL');
  await page.getByLabel(/Job label/).fill('app');
  await expect(blocked).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Continue' })).toBeEnabled();
});

test.describe('a hand edit to a wizard pipeline', () => {
  test('asks first, then saves and drops the wizard landing banner', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      pipelines: [
        pipeline({
          id: 'pip-wz',
          name: 'wz',
          org_id: s.org.id,
          source: 'wizard',
          contents: '// generated',
        }),
      ],
    });
    await page.goto('/pipelines/pip-wz?from=wizard');
    await expect(page.getByTestId('editor-landing-banner')).toBeVisible();
    await page.locator('.cm-content').click();
    await page.keyboard.press('End');
    await page.keyboard.type(' // mine');

    await page.getByRole('button', { name: 'Save', exact: true }).click();
    const confirm = page.getByRole('dialog', { name: 'Save a hand edit?' });
    await expect(confirm).toContainText('stops it following destination changes');
    await confirm.getByRole('button', { name: 'Cancel' }).click();
    await expect(confirm).toHaveCount(0);
    expect(api.calls('PipelineService/UpdatePipeline')).toHaveLength(0);

    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await confirm.getByRole('button', { name: 'Save hand edit' }).click();
    await expect.poll(() => api.calls('PipelineService/UpdatePipeline').length).toBe(1);
    await expect(page.getByTestId('editor-landing-banner')).toHaveCount(0);
  });

  test('a save that changes no text is not asked about', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      pipelines: [
        pipeline({
          id: 'pip-wz',
          name: 'wz',
          org_id: s.org.id,
          source: 'wizard',
          contents: '// generated',
        }),
      ],
    });
    await page.goto('/pipelines/pip-wz');
    await expect(page.locator('.cm-content')).toContainText('generated');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect.poll(() => api.calls('PipelineService/UpdatePipeline').length).toBe(1);
    await expect(page.getByRole('dialog', { name: 'Save a hand edit?' })).toHaveCount(0);
  });
});

test.describe('accessibility', () => {
  test("Simulate's disabled reason is reachable by keyboard", async ({ page, api }) => {
    await api.loginAs(viewer);
    const p = seedVisual(api);
    await page.goto(`/pipelines/${p.id}/visual`);
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    const trigger = page.getByTestId('simulate-menu-trigger');
    await expect(trigger).toBeDisabled();
    await trigger.focus();
    await expect(trigger).toBeFocused();
    await expect(trigger).toHaveAccessibleDescription(/Viewers can't change pipelines/);
    // Activating it does nothing.
    await page.keyboard.press('Enter');
    await expect(page.getByTestId('simulate-menu')).toHaveCount(0);

    await page.getByTestId('drawer-tab-simulate').click();
    const run = page.getByTestId('simulate-relabel-run');
    await run.focus();
    await expect(run).toBeFocused();
    await expect(run).toHaveAccessibleDescription(/Viewers can't change pipelines/);
  });

  test("each team's row actions name the team, and the header says who manages teams", async ({
    page,
    api,
  }) => {
    await api.loginAs(orgAdmin);
    api.seed({ orgs: [basicScenario().org] });
    await page.goto('/teams');
    await expect(page.getByRole('button', { name: 'Delete team local-squad' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Manage members of local-squad' })).toBeVisible();
    await expect(page.getByTestId('teams-intro')).toContainText(
      'Only organisation admins create and delete teams and manage their members.',
    );
  });

  test('the visual builder has a page heading and its palette items are buttons', async ({
    page,
    api,
  }) => {
    await api.loginAs(orgEditor);
    const p = seedVisual(api);
    await page.goto(`/pipelines/${p.id}/visual`);
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Visual builder: viewed');
    await expect(
      page.getByTestId('palette').getByRole('button', { name: /prometheus\.remote_write/ }),
    ).toBeVisible();
  });

  test('the text editor is a named textbox', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({ orgs: [s.org], pipelines: s.pipelines });
    await page.goto('/pipelines/pip-0001');
    await expect(page.getByRole('textbox', { name: 'Alloy configuration' })).toBeVisible();
  });
});

test('an audited team membership names the team and the member', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  api.seed({
    orgs: [basicScenario().org],
    auditRows: [
      {
        id: 1,
        at: '2026-10-12T09:00:00Z',
        actor: 'admin@example.com',
        actor_type: 'user',
        org_id: 'org-0001',
        action: 'team.member.add',
        resource_type: 'team',
        resource_id: 'team-2/user-2',
      },
    ],
  });
  await page.goto('/audit');
  await expect(page.getByTestId('audit-resource-name')).toHaveText('local-squad / Alice');
});

test('a taken-name message names the pipeline it collides with', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [pipeline({ id: 'pip-pl', name: 'Pod Logs' })] });
  api.override('POST', '/shepherd.mgmt.v1.WizardService/GetWizardSchema', (route) =>
    fulfillJSON(route, {
      kind: 'pod-logs',
      title: 'Pod Logs',
      steps: [
        {
          id: 'targets',
          title: 'Pod selection',
          fields: [{ name: 'namespace_pattern', label: 'Namespace pattern', type: 'text' }],
        },
      ],
    }),
  );
  await page.goto('/wizards/pod-logs');
  await page.getByRole('button', { name: 'Continue' }).click();
  const name = page.getByLabel('Pipeline name');
  await name.fill('pod-logs');
  await expect(page.getByTestId('wizard-create-blocked')).toContainText('“Pod Logs”');
});

test('the pipeline page shows a loading state, not an empty form', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  api.delay('POST', '/shepherd.mgmt.v1.PipelineService/GetPipeline', 1_500);
  await page.goto('/pipelines/pip-0001');
  await expect(page.getByTestId('pipeline-loading')).toBeVisible();
  await expect(page.locator('#pipeline-name')).toHaveCount(0);
  await expect(page.locator('#pipeline-name')).toHaveValue('ui-enabled', { timeout: 10_000 });
  await expect(page.getByTestId('pipeline-loading')).toHaveCount(0);
});

test('signing out forgets the selected organisation', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.override('GET', '/auth/logout', async (route, _params, state) => {
    state.me = null;
    await route.fulfill({ status: 200 });
  });
  await page.goto('/');
  const stored = () => page.evaluate(() => window.localStorage.getItem('shepherd.orgId'));
  await expect.poll(stored).toBe('org-0001');
  await page.getByRole('button', { name: /sign out/i }).click();
  await expect(page).toHaveURL(/\/login/);
  expect(await stored()).toBeNull();
});

test('Collectors offers no "Group by" when there is nothing to group by', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto('/collectors');
  await expect(page.getByText('prod-eu-1').first()).toBeVisible();
  await expect(page.getByLabel('Group by')).toHaveCount(0);
});

test('a viewer is refused at /pipelines/new/visual as at /pipelines/visual/new', async ({
  page,
  api,
}) => {
  await api.loginAs(viewer);
  api.seed({ orgs: [basicScenario().org], schema: schemaFixture });
  await page.goto('/pipelines/new/visual');
  await expect(page.getByTestId('route-denied')).toBeVisible();
});
