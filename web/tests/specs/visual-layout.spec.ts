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

  // KNOWN ISSUE — the minimap renders as an empty rectangle, never showing the
  // nodes. Kept as `fixme` rather than deleted because the cause is understood
  // and the fix is entangled with something load-bearing:
  //
  // React Flow runs controlled here, so anything it derives from the node
  // objects we hand it (rather than from the DOM) sees only the fields we set.
  // `nodeHasDimensions` gates MiniMapNodes and rejects a node carrying no
  // measured/width/initialWidth, and `rfNodes` sets none — hence nothing drawn.
  //
  // The obvious fix, round-tripping React Flow's measured dimensions back onto
  // the nodes, breaks connection dragging. `parseHandles` returns `undefined`
  // for a node with no `measured`, which resets its cached handle bounds and
  // forces a fresh DOM measurement on every rebuild of the array — and the wire
  // gestures currently depend on that accidental re-measure. Supplying
  // `measured` preserves stale bounds instead, and drops land nowhere: three
  // visual-linking specs fail. `initialWidth`/`initialHeight` avoid that path
  // but get applied as inline width/height, which would pin the node's rendered
  // size — wrong here, since node height varies with port count.
  //
  // Doing this properly means giving `rfNodes` stable object identity so React
  // Flow stops re-measuring every node on every rebuild, then feeding measured
  // sizes back. That is the same controlled-mode round-tripping gap the review
  // recorded for `selected`, and it belongs in that work, not a spot fix.
  test.fixme('the minimap shows the nodes that are on the canvas', async ({ page }) => {
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.relabel"]');
    await expect(page.getByTestId('pipeline-node')).toHaveCount(2);

    await expect(page.locator('.react-flow__minimap')).toBeVisible();
    await expect(page.locator('.react-flow__minimap-node')).toHaveCount(2);
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
