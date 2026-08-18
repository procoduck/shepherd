// visual-disable.spec.ts — 7.6.5
import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

test.describe('visual disable', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
    // Place a node
    await page.click('[data-component="prometheus.scrape"]');
    await page.waitForSelector('[data-testid="pipeline-node"]', { timeout: 5_000 });
    // Select the node so inspector shows
    await page.click('[data-testid="pipeline-node"]', { force: true });
    await page.waitForSelector('[data-testid="node-disable-toggle"]', { timeout: 5_000 });
  });

  test('disabling a node adds opacity-50 class', async ({ page }) => {
    await page.locator('[data-testid="node-disable-toggle"]').check({ force: true });
    const node = page.locator('[data-testid="pipeline-node"]').first();
    await expect(node).toHaveClass(/opacity-50/, { timeout: 3_000 });
  });

  test('re-enabling removes opacity-50 class', async ({ page }) => {
    const toggle = page.locator('[data-testid="node-disable-toggle"]');
    await toggle.check({ force: true });
    await expect(page.locator('[data-testid="pipeline-node"]').first()).toHaveClass(/opacity-50/);
    await toggle.uncheck({ force: true });
    await expect(page.locator('[data-testid="pipeline-node"]').first()).not.toHaveClass(
      /opacity-50/,
    );
  });

  test('disabled node shows border-dashed class', async ({ page }) => {
    await page.locator('[data-testid="node-disable-toggle"]').check({ force: true });
    const node = page.locator('[data-testid="pipeline-node"]').first();
    await expect(node).toHaveClass(/border-dashed/, { timeout: 3_000 });
  });

  test('disabling updates node visual state', async ({ page }) => {
    const toggle = page.locator('[data-testid="node-disable-toggle"]');
    await toggle.check({ force: true });
    const node = page.locator('[data-testid="pipeline-node"]').first();
    // Both opacity and dashed border applied
    await expect(node).toHaveClass(/opacity-50/);
    await expect(node).toHaveClass(/border-dashed/);
  });
});
