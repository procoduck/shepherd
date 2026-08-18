// visual-graph-view.spec.ts — §7.7.3 equivalent (mocked suite)
// Read-only graph view for text pipelines: no palette, no save, recreate button.
import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const mockGraph = {
  kind: 'alloy-graph/v1',
  schema_version: 'alloy-v1.18.1',
  nodes: [
    {
      id: 'n_scrape',
      component: 'prometheus.scrape',
      label: 'app',
      position: { x: 0, y: 0 },
      props: { job_name: '"myapp"' },
      disabled: false,
      notes: '',
    },
    {
      id: 'n_write',
      component: 'prometheus.remote_write',
      label: 'sink',
      position: { x: 280, y: 0 },
      props: {},
      disabled: false,
      notes: '',
    },
  ],
  edges: [
    {
      id: 'e1',
      from: { node: 'n_scrape', port: 'metrics' },
      to: { node: 'n_write', port: 'receiver' },
    },
  ],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-parser' },
};

test.describe('visual graph view', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [s.pipelines[0]],
      graphViewResult: { graph: mockGraph, opaque: false, warning: '' },
    });
    await page.goto(`/pipelines/${s.pipelines[0].id}/graph`);
    await page.waitForSelector('[data-testid="graph-view"]', { timeout: 10_000 });
  });

  test('renders nodes from the parsed graph', async ({ page }) => {
    // Both nodes from mockGraph should appear as pipeline-node elements
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2, {
      timeout: 5_000,
    });
  });

  test('palette is not present in DOM', async ({ page }) => {
    // Graph view is read-only — no palette
    await expect(page.locator('[data-testid="palette"]')).not.toBeVisible();
  });

  test('no Save button is present', async ({ page }) => {
    await expect(page.locator('[data-testid="toolbar-save"]')).not.toBeVisible();
  });

  test('shows "Recreate as visual pipeline" button', async ({ page }) => {
    await expect(page.locator('[data-testid="recreate-as-visual-btn"]')).toBeVisible();
  });

  test('Recreate button shows a confirm dialog with lossy warning', async ({ page }) => {
    await page.click('[data-testid="recreate-as-visual-btn"]');
    // Confirm dialog is visible — check the confirm button specifically
    await expect(page.locator('[data-testid="recreate-confirm-btn"]')).toBeVisible({
      timeout: 3_000,
    });
    // The dialog mentions the lossy nature of the conversion
    await expect(page.getByText('This creates a')).toBeVisible({ timeout: 3_000 });
  });

  test('opaque warning shown when graph has unparseable content', async ({ page, api }) => {
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [s.pipelines[0]],
      graphViewResult: {
        graph: { ...mockGraph, nodes: [] },
        opaque: true,
        warning: 'some expressions could not be mapped',
      },
    });
    await page.goto(`/pipelines/${s.pipelines[0].id}/graph`);
    await page.waitForSelector('[data-testid="graph-view"]', { timeout: 10_000 });
    await expect(page.getByText(/Partial/i)).toBeVisible({ timeout: 3_000 });
  });
});
