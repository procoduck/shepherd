/**
 * Canvas layout — the working surface must be able to dominate.
 *
 * docs/archive/reviews/canvas-ux-and-forms.md measured the canvas at 424px on a 1280px
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

  test('entering the builder collapses the app nav, and leaving restores it', async ({ page }) => {
    // draw.io gives the canvas the whole window; the nav rail is worth 184px of
    // canvas here. Leaving must put the nav back exactly as the user had it.
    await page.goto('/pipelines');
    await expect(page.getByTestId('app-sidebar')).toHaveAttribute('data-collapsed', 'false');

    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });
    await expect(page.getByTestId('app-sidebar')).toHaveAttribute('data-collapsed', 'true');

    await page.goto('/pipelines');
    await expect(page.getByTestId('app-sidebar')).toHaveAttribute('data-collapsed', 'false');
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

  // Regression guard for the controlled-mode contract (see CanvasPane's
  // "controlled-mode contract" comment). `nodeHasDimensions` gates MiniMapNodes
  // and rejects a node carrying no `measured`, and React Flow only ever writes
  // `measured` through the `dimensions` change that applyNodeChanges applies.
  // Drop that change type and the minimap silently draws nothing — which is
  // exactly what it did until the change stream was routed properly.
  test('the minimap shows the nodes that are on the canvas', async ({ page }) => {
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.relabel"]');
    await expect(page.getByTestId('pipeline-node')).toHaveCount(2);

    await expect(page.locator('.react-flow__minimap')).toBeVisible();
    await expect(page.locator('.react-flow__minimap-node')).toHaveCount(2);
  });

  test('React Flow owns selection — a clicked node carries its own selected class', async ({
    page,
  }) => {
    // The other half of the same contract. `selected` reaches React Flow only
    // through the `select` change; drop it and RF's internal selection stays
    // permanently empty, which is what made every node and wire undeletable
    // while our own store still believed something was selected. Asserting on
    // RF's class rather than our store is the point — the store agreeing with
    // itself is what masked this for so long.
    await page.click('[data-component="discovery.kubernetes"]');
    await expect(page.getByTestId('pipeline-node')).toHaveCount(1);

    await page.locator('.react-flow__node').first().click();
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(1);
  });

  test('a node drag records one undo step, not one per frame', async ({ page }) => {
    // Position is document state, but React Flow emits a change per pointer
    // move. Committing each one buried the history under hundreds of entries
    // per drag and churned the projection on every frame; only the drag-end
    // change (dragging: false) is written now. One ctrl+Z must undo the move.
    await page.click('[data-component="discovery.kubernetes"]');
    const node = page.locator('.react-flow__node').first();
    await expect(node).toHaveCount(1);

    const start = (await node.boundingBox())!;
    await page.mouse.move(start.x + start.width / 2, start.y + 10);
    await page.mouse.down();
    for (let i = 1; i <= 10; i++) {
      await page.mouse.move(start.x + start.width / 2 + i * 12, start.y + 10 + i * 6);
      await page.waitForTimeout(15);
    }
    await page.mouse.up();
    await page.waitForTimeout(200);

    const moved = (await node.boundingBox())!;
    expect(Math.abs(moved.x - start.x)).toBeGreaterThan(40);

    await page.getByTestId('canvas').click({ position: { x: 5, y: 5 } });
    await page.keyboard.press('ControlOrMeta+z');
    await page.waitForTimeout(300);

    const undone = (await node.boundingBox())!;
    expect(Math.abs(undone.x - start.x)).toBeLessThan(12);
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
