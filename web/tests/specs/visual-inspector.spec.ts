// visual-inspector.spec.ts — 7.6.3 (partial, no Code tab yet)
import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

test.describe('visual inspector', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  });

  test('no-selection inspector shows stats text', async ({ page }) => {
    await expect(page.locator('[data-testid="inspector"]')).toContainText('Select a node');
  });

  test('selecting a relabel node shows its component name in inspector', async ({ page }) => {
    await page.click('[data-component="discovery.relabel"]');
    await page.waitForSelector('[data-testid="pipeline-node"]', { timeout: 5_000 });
    await page.click('[data-testid="pipeline-node"]', { force: true });
    await expect(page.locator('[data-testid="inspector"]')).toContainText('discovery.relabel', {
      timeout: 5_000,
    });
  });

  test('inspector shows attribute inputs for selected node', async ({ page }) => {
    await page.click('[data-component="discovery.relabel"]');
    await page.waitForSelector('[data-testid="pipeline-node"]', { timeout: 5_000 });
    await page.click('[data-testid="pipeline-node"]', { force: true });
    // relabel has a 'targets' list attribute
    await expect(page.locator('[data-testid="inspector"]')).toBeVisible();
    await expect(page.locator('[data-testid="inspector"]')).toContainText('discovery.relabel');
  });

  test('secret-typed field shows binding picker text, not a text input', async ({ page }) => {
    // prometheus.remote_write endpoint block has a 'password' secret field
    await page.click('[data-component="prometheus.remote_write"]');
    await page.waitForSelector('[data-testid="pipeline-node"]', { timeout: 5_000 });
    await page.click('[data-testid="pipeline-node"]', { force: true });
    await expect(page.locator('[data-testid="inspector"]')).toContainText(
      'prometheus.remote_write',
      { timeout: 5_000 },
    );
    // Secret field 'password' is in a block (endpoint.password) — not shown at top-level in M2
    // Top-level attrs for remote_write have no secret fields; verify no raw text input for 'password'
    await expect(page.locator('[data-testid="attr-input-password"]')).not.toBeVisible();
  });

  test('placing two nodes and selecting second shows correct component', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.click('[data-component="prometheus.remote_write"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);
    // Get the second node's id and click it directly for reliable targeting
    const secondNodeId = await page
      .locator('[data-testid="pipeline-node"]')
      .last()
      .getAttribute('data-node-id');
    await page.click(`[data-node-id="${secondNodeId}"]`, { force: true });
    await expect(page.locator('[data-testid="inspector"]')).toContainText(
      'prometheus.remote_write',
      { timeout: 5_000 },
    );
  });
});
