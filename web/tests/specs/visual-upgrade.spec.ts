import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const oldGraph = {
  kind: 'alloy-graph/v1' as const,
  schema_version: 'alloy-v1.12.0',
  nodes: [],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-vb/1.0' },
};

/**
 * Navigate to /pipelines/visual/new with an old-schema graph pre-loaded via sessionStorage.
 * The VisualBuilderPage reads 'vb:import-graph' from sessionStorage on mount when pipelineId==='new'.
 * addInitScript runs before any page script, guaranteeing the key is present at mount time.
 */
async function gotoWithOldGraph(page: import('@playwright/test').Page) {
  await page.addInitScript((graph) => {
    sessionStorage.setItem('vb:import-graph', JSON.stringify(graph));
  }, oldGraph);
  await page.goto('/pipelines/visual/new');
  await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
}

test.describe('visual upgrade', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
  });

  test('7.6.6.1 — no banner when schema_version matches current', async ({ page }) => {
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('upgrade-banner')).not.toBeVisible();
  });

  test('7.6.6.2 — banner renders when schema_version is old', async ({ page }) => {
    await gotoWithOldGraph(page);
    await expect(page.getByTestId('upgrade-banner')).toBeVisible();
  });

  test('7.6.6.3 — review opens on click', async ({ page, api }) => {
    api.seed({
      upgradeCheckResult: {
        old_version: 'alloy-v1.12.0',
        new_version: 'alloy-v1.18.1',
        items: [
          {
            node_id: 'n1',
            node_label: 'my-scrape',
            component: 'test.component',
            class: 'component_removed',
            detail: '',
          },
        ],
        needs_upgrade: true,
      },
    });
    await gotoWithOldGraph(page);
    await page.getByTestId('upgrade-review-open').click();
    await expect(page.getByTestId('upgrade-review')).toBeVisible();
  });

  test('7.6.6.4 — all diff classes render in review', async ({ page, api }) => {
    const classes = [
      'component_removed',
      'attr_removed',
      'attr_added_required',
      'enum_value_removed',
      'port_type_changed',
      'stability_changed',
      'migration_available',
    ];
    api.seed({
      upgradeCheckResult: {
        old_version: 'alloy-v1.12.0',
        new_version: 'alloy-v1.18.1',
        items: classes.map((className, i) => ({
          node_id: `n${i}`,
          node_label: `node-${i}`,
          component: 'test.component',
          class: className,
          detail: 'detail',
        })),
        needs_upgrade: true,
      },
    });
    await gotoWithOldGraph(page);
    await page.getByTestId('upgrade-review-open').click();
    await expect(page.getByTestId('upgrade-review')).toBeVisible();
    for (const className of classes) {
      await expect(page.getByTestId(`upgrade-item-${className}`)).toBeVisible();
    }
    await expect(page.getByTestId('upgrade-discard-attr')).toBeVisible();
  });

  test('7.6.6.5 — Accept stamps new version and closes review', async ({ page, api }) => {
    api.seed({
      upgradeCheckResult: {
        old_version: 'alloy-v1.12.0',
        new_version: 'alloy-v1.18.1',
        items: [],
        needs_upgrade: true,
      },
    });
    await gotoWithOldGraph(page);
    await page.getByTestId('upgrade-review-open').click();
    await expect(page.getByTestId('upgrade-review')).toBeVisible();
    await page.getByTestId('upgrade-accept').click();
    await expect(page.getByTestId('upgrade-review')).not.toBeVisible();
  });
});
