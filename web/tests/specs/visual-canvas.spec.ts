import { expect, type Page } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

test.describe('visual canvas', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  });

  test('palette search narrows — "rela" shows relabel, hides scrape', async ({ page }) => {
    await page.fill('[data-testid="palette-search"]', 'rela');
    await expect(page.locator('[data-component="discovery.relabel"]')).toBeVisible();
    await expect(page.locator('[data-component="prometheus.scrape"]')).not.toBeVisible();
  });

  test('click-to-place adds a node to the canvas', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(1);
  });

  test('node inline-rename updates label on Enter', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    const label = page.locator('[data-testid="node-label"]').first();
    await label.dispatchEvent('dblclick');
    const input = page.locator('[data-testid="node-label-input"]').first();
    await expect(input).toBeVisible({ timeout: 3_000 });
    await input.fill('my-scrape');
    await input.press('Enter');
    await expect(page.locator('[data-testid="node-label"]').first()).toContainText('my-scrape');
  });

  async function paste(page: Page) {
    await page.click('[data-testid="pipeline-node"]', { force: true });
    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Control+A');
    await page.keyboard.press('Control+C');
    await page.keyboard.press('Control+V');
  }

  test('copy/paste produces two nodes with distinct data-node-id', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.waitForSelector('[data-testid="pipeline-node"]');
    await paste(page);
    const nodes = page.locator('[data-testid="pipeline-node"]');
    await expect(nodes).toHaveCount(2, { timeout: 5_000 });
    const ids = await nodes.evaluateAll((els: Element[]) => els.map((el) => el.getAttribute('data-node-id')));
    expect(new Set(ids).size).toBe(2);
  });

  test('undo removes the pasted node atomically', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.waitForSelector('[data-testid="pipeline-node"]');
    await paste(page);
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2, { timeout: 5_000 });
    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Control+Z');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(1, { timeout: 5_000 });
  });
});
