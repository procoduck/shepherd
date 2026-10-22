/**
 * #310 (2026-10-08 pre-release walkthrough, accessible-name nits). Each
 * control below was announced by a screen viewer as something that did not
 * say which thing it acts on — "Edit", "Delete destination", six "Start"
 * links, an edge between internal node ids — or by nothing useful at all.
 * These specs find every control by the accessible name it should have, so a
 * regression fails on a missing name rather than on a moved testid.
 */
import { expect } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { waitForViewportSettled } from '../fixtures/canvas';
import { basicScenario, destination, pipeline } from '../fixtures/factories';
import { appAdmin, orgAdmin, orgEditor, viewer } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';
import { json } from '../mocks/router';

test('users table actions name the user they act on', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/users');

  const row = page.getByTestId('user-row-alice');
  await expect(row.getByRole('button', { name: 'Edit alice', exact: true })).toBeVisible();
  await expect(
    row.getByRole('button', { name: 'Reset password for alice', exact: true }),
  ).toBeVisible();
  await expect(row.getByRole('button', { name: 'Delete alice', exact: true })).toBeVisible();
});

test('delete destination names the destination', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [
      destination({ id: 'dst-a', name: 'prom-prod' }),
      destination({ id: 'dst-b', name: 'loki-eu' }),
    ],
  });
  await page.goto('/destinations');

  await expect(page.getByRole('button', { name: 'Delete prom-prod', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Delete loki-eu', exact: true })).toBeVisible();
});

test('each wizard catalog Start link names its wizard', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  api.seed({ orgs: [basicScenario().org] });
  api.override('POST', '/shepherd.mgmt.v1.WizardService/ListWizards', (route) =>
    json(route, 200, {
      items: [
        { kind: 'database', title: 'Database', description: 'db', steps: [] },
        { kind: 'pod-logs', title: 'Pod logs', description: 'logs', steps: [] },
      ],
      total: 2,
    }),
  );
  await page.goto('/wizards');

  await expect(page.getByRole('link', { name: 'Start Database', exact: true })).toHaveAttribute(
    'href',
    '/wizards/database',
  );
  await expect(page.getByRole('link', { name: 'Start Pod logs', exact: true })).toHaveAttribute(
    'href',
    '/wizards/pod-logs',
  );
});

test('the pipeline name field is labelled "Name", not by its placeholder', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  api.seed({ orgs: [basicScenario().org] });
  await page.goto('/pipelines/new');
  await expect(page.getByRole('textbox', { name: 'Name', exact: true })).toBeVisible();
});

test('a viewer sees the matcher remove buttons labelled, and disabled', async ({ page, api }) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [pipeline({ id: 'pip-ro', name: 'ro', org_id: s.org.id, matchers: ['env="prod"'] })],
  });
  await page.goto('/pipelines/pip-ro');
  await expect(
    page.getByRole('button', { name: 'Remove matcher env="prod"', exact: true }),
  ).toBeDisabled();
});

const graph = {
  kind: 'alloy-graph/v1',
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    {
      id: 'n3',
      component: 'prometheus.scrape',
      label: 'app',
      position: { x: 0, y: 0 },
      props: {},
      disabled: false,
      notes: '',
    },
    {
      id: 'n2',
      component: 'prometheus.remote_write',
      label: 'sink',
      position: { x: 400, y: 0 },
      props: {},
      disabled: false,
      notes: '',
    },
  ],
  edges: [
    {
      id: 'e1',
      from: { node: 'n3', port: 'forward_to' },
      to: { node: 'n2', port: 'receiver' },
    },
  ],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-visual-builder' },
};

test.describe('visual builder', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [
        { ...pipeline({ id: 'pip-vb', name: 'vb', source: 'visual' }), wizard_state: graph },
      ],
    });
    await page.goto('/pipelines/pip-vb/visual');
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('.react-flow__node')).toHaveCount(2);
    await waitForViewportSettled(page);
  });

  test('a wire is announced by its node labels, in dataflow order', async ({ page }) => {
    const wire = page.locator('[aria-roledescription="edge"]');
    await expect(wire).toHaveCount(1);
    await expect(wire).toHaveAccessibleName('Edge from app to sink');
  });

  test('palette groups are named', async ({ page }) => {
    const palette = page.getByTestId('palette');
    await expect(palette.getByRole('group', { name: 'Sources', exact: true })).toBeVisible();
    await expect(palette.getByRole('group', { name: 'Destinations', exact: true })).toBeVisible();
  });

  test('the Simulate dropdown is a menu', async ({ page }) => {
    const trigger = page.getByTestId('simulate-menu-trigger');
    await expect(trigger).toHaveAttribute('aria-haspopup', 'menu');
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');
    await trigger.click();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    const menu = page.getByRole('menu', { name: 'Simulate' });
    const item = menu.getByRole('menuitem', { name: /Sandbox run/ });
    await expect(item).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(menu).toHaveCount(0);
    await expect(trigger).toBeFocused();

    // ArrowDown on the trigger opens it too; tabbing out of it closes it.
    await page.keyboard.press('ArrowDown');
    await expect(menu.getByRole('menuitem', { name: /Sandbox run/ })).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(menu).toHaveCount(0);
  });
});
