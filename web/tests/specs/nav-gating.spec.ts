/**
 * #206: the UI offered what a role cannot use.
 *
 * - Nav links to pages a role is denied (editor: Git, Service accounts,
 *   Audit; viewer: also Wizards) used to be shown, and following one silently
 *   redirected to Overview. Shell now hides them, from routeManifest's
 *   requiredRole — the same table RequireRole enforces.
 * - A direct visit to such a URL shows a "You don't have access" page at that
 *   URL instead of the silent redirect.
 * - The visual builder was fully interactive for a viewer and every action
 *   failed with `auth: forbidden`; it is now read-only, like the text editor.
 *
 * Every absence assertion is preceded by a positive control, so a page that
 * failed to render cannot make it pass vacuously.
 */
import { expect } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgAdmin, orgEditor, reader } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const RESTRICTED_FOR_VIEWER = ['Git', 'Service accounts', 'Audit', 'Wizards'];

test('a viewer sees no nav links to Git, Service accounts, Audit or Wizards', async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  const nav = page.getByTestId('app-sidebar');
  await expect(nav.getByRole('link', { name: 'Pipelines' })).toBeVisible();
  await expect(nav.getByRole('link', { name: 'Teams' })).toBeVisible();
  for (const name of RESTRICTED_FOR_VIEWER) {
    await expect(nav.getByRole('link', { name, exact: true })).toHaveCount(0);
  }
});

test('an editor sees Wizards but not Git, Service accounts or Audit', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  const nav = page.getByTestId('app-sidebar');
  await expect(nav.getByRole('link', { name: 'Wizards' })).toBeVisible();
  for (const name of ['Git', 'Service accounts', 'Audit']) {
    await expect(nav.getByRole('link', { name, exact: true })).toHaveCount(0);
  }
});

test('an org admin still sees Git, Service accounts, Audit and Wizards', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  const nav = page.getByTestId('app-sidebar');
  for (const name of RESTRICTED_FOR_VIEWER) {
    await expect(nav.getByRole('link', { name, exact: true })).toBeVisible();
  }
});

for (const route of ['/git', '/service-accounts', '/audit', '/wizards']) {
  test(`a viewer visiting ${route} is told they have no access, not redirected`, async ({
    page,
    api,
  }) => {
    await api.loginAs(reader);
    const s = basicScenario();
    api.seed({ orgs: [s.org] });
    await page.goto(route);
    const denied = page.getByTestId('route-denied');
    await expect(denied).toBeVisible();
    await expect(denied).toContainText("You don't have access to this page");
    // Stays on the URL that was asked for — the old guard bounced to '/'.
    expect(new URL(page.url()).pathname).toBe(route);
    await expect(denied.getByRole('link', { name: 'Back to Overview' })).toBeVisible();
  });
}

test('a viewer visiting the new visual pipeline URL is told they have no access', async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org], schema: schemaFixture });
  await page.goto('/pipelines/visual/new');
  await expect(page.getByTestId('route-denied')).toBeVisible();
  await expect(page.getByTestId('visual-builder')).toHaveCount(0);
});

const savedGraph = {
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

test("a viewer's visual builder is read-only: no Save, Simulate, palette or name edits", async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const s = basicScenario();
  const visualPipe = {
    ...pipeline({ id: 'pip-view', name: 'viewed', source: 'visual' }),
    wizard_state: savedGraph,
  };
  api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [visualPipe] });

  await page.goto(`/pipelines/${visualPipe.id}/visual`);
  await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
  // Positive control: the saved graph is on screen for the viewer to look at.
  await expect(page.locator('.react-flow__node')).toHaveCount(1);
  await expect(page.getByTestId('toolbar-read-only')).toBeVisible();

  await expect(page.getByTestId('toolbar-save')).toBeDisabled();
  await expect(page.getByTestId('simulate-menu-trigger')).toBeDisabled();
  await expect(page.getByTestId('toolbar-name')).toBeDisabled();
  await expect(page.getByTestId('toolbar-name')).toHaveValue('viewed');
  await expect(page.getByTestId('matcher-input')).toBeDisabled();

  // The palette still lists components, but placing one does nothing.
  await expect(page.getByTestId('palette-read-only')).toBeVisible();
  const item = page.getByTestId('palette-item-prometheus.remote_write');
  await expect(item).toHaveAttribute('aria-disabled', 'true');
  await item.click();
  await expect(page.locator('.react-flow__node')).toHaveCount(1);

  // Nothing the viewer did reached an editor-only RPC.
  expect(api.calls('VisualService/Render')).toHaveLength(0);
  expect(api.calls('PipelineService/UpdatePipeline')).toHaveLength(0);
});

test("an editor's visual builder keeps Save, Simulate and the palette", async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const visualPipe = {
    ...pipeline({ id: 'pip-edit', name: 'edited', source: 'visual' }),
    wizard_state: savedGraph,
  };
  api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [visualPipe] });

  await page.goto(`/pipelines/${visualPipe.id}/visual`);
  await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
  await expect(page.locator('.react-flow__node')).toHaveCount(1);
  await expect(page.getByTestId('toolbar-read-only')).toHaveCount(0);
  await expect(page.getByTestId('simulate-menu-trigger')).toBeEnabled();
  await expect(page.getByTestId('toolbar-name')).toBeEnabled();
  await page.getByTestId('palette-item-prometheus.remote_write').click();
  await expect(page.locator('.react-flow__node')).toHaveCount(2);
});
