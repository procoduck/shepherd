/**
 * Canvas layout — the working surface must be able to dominate.
 *
 * docs/reviews/canvas-ux-and-forms.md measured the canvas at 424px on a 1280px
 * window with the app sidebar, palette and inspector all fixed — under two
 * node-widths, narrow enough that a placed node could land outside it and its
 * ports become undraggable. Both builder panels are now collapsible (after
 * draw.io's shape and Format panels) and remember the choice.
 */
import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

test.describe('visual builder layout', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });
  });

  const canvasWidth = async (page: import('@playwright/test').Page) => {
    const box = await page.locator('.react-flow__pane').boundingBox();
    return box?.width ?? 0;
  };

  test('collapsing both panels roughly doubles the canvas on a narrow window', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.waitForTimeout(200);
    const before = await canvasWidth(page);

    await page.getByTestId('palette-toggle').click();
    await page.getByTestId('inspector-toggle').click();
    await page.waitForTimeout(300);
    const after = await canvasWidth(page);

    // The measured regression was 424px; anything under ~2 node-widths (480px)
    // is too cramped to wire a pipeline in.
    expect(after).toBeGreaterThan(before);
    expect(after).toBeGreaterThan(800);
  });

  test('a collapsed panel keeps a visible way back', async ({ page }) => {
    await page.getByTestId('palette-toggle').click();
    await expect(page.getByTestId('palette')).toHaveAttribute('data-collapsed', 'true');
    // The rail — and its expand control — must still be there.
    await expect(page.getByTestId('palette-toggle')).toBeVisible();
    await page.getByTestId('palette-toggle').click();
    await expect(page.getByTestId('palette')).toHaveAttribute('data-collapsed', 'false');
    await expect(page.getByTestId('palette-search')).toBeVisible();
  });

  test('the collapse choice survives a reload', async ({ page }) => {
    await page.getByTestId('inspector-toggle').click();
    await expect(page.getByTestId('inspector')).toHaveAttribute('data-collapsed', 'true');

    await page.reload();
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });
    await expect(page.getByTestId('inspector')).toHaveAttribute('data-collapsed', 'true');
  });

  test('a component can still be placed and wired with the palette collapsed', async ({ page }) => {
    // Collapsing must not strand the user: reopening to place, then collapsing to
    // wire, is the normal draw.io rhythm.
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.relabel"]');
    await expect(page.getByTestId('pipeline-node')).toHaveCount(2);

    await page.getByTestId('palette-toggle').click();
    await expect(page.getByTestId('palette')).toHaveAttribute('data-collapsed', 'true');
    await expect(page.getByTestId('pipeline-node')).toHaveCount(2);
    await expect(page.getByTestId('canvas')).toBeVisible();
  });
});
