// visual-selection-delete.spec.ts — canvas selection round-tripped to React Flow
// (`.react-flow__node.selected` / `.react-flow__edge.selected`) and Delete/Backspace
// actually removing the selection, including a node's cascaded edges, atomically
// and undoably.
import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

async function dragWire(
  page: import('@playwright/test').Page,
  fromBox: { x: number; y: number; width: number; height: number },
  toBox: { x: number; y: number; width: number; height: number },
) {
  const fx = fromBox.x + fromBox.width / 2;
  const fy = fromBox.y + fromBox.height / 2;
  const tx = toBox.x + toBox.width / 2;
  const ty = toBox.y + toBox.height / 2;
  await page.mouse.move(fx, fy);
  await page.mouse.down();
  await page.waitForTimeout(100);
  const steps = 20;
  for (let i = 1; i <= steps; i++) {
    await page.mouse.move(fx + ((tx - fx) * i) / steps, fy + ((ty - fy) * i) / steps);
    await page.waitForTimeout(15);
  }
  await page.mouse.up();
  await page.waitForTimeout(400);
}

// Click-to-place staggers successive nodes only 60px apart (Palette.tsx) —
// well inside each node's 240px width — so the two nodes start overlapping.
// Drag the second one clear so the wire between them runs through open canvas
// instead of over a node body, which matters for tests that click the edge itself.
async function dragNodeBy(
  page: import('@playwright/test').Page,
  nodeLocator: ReturnType<import('@playwright/test').Page['locator']>,
  dx: number,
  dy: number,
) {
  const box = await nodeLocator.boundingBox();
  const fx = box!.x + box!.width / 2;
  const fy = box!.y + 8; // header strip — clear of ports and handles
  await page.mouse.move(fx, fy);
  await page.mouse.down();
  await page.waitForTimeout(50);
  const steps = 10;
  for (let i = 1; i <= steps; i++) {
    await page.mouse.move(fx + (dx * i) / steps, fy + (dy * i) / steps);
    await page.waitForTimeout(15);
  }
  await page.mouse.up();
  await page.waitForTimeout(150);
}

// Places two compatible nodes (discovery.kubernetes -> discovery.relabel),
// separates them, and wires them, returning once exactly one edge exists.
async function placeWiredPair(page: import('@playwright/test').Page) {
  await page.click('[data-component="discovery.kubernetes"]');
  await page.click('[data-component="discovery.relabel"]');
  await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);

  await dragNodeBy(page, page.locator('.react-flow__node').nth(1), 250, 0);
  // The canvas pane is much narrower than the viewport (palette + inspector
  // eat most of it), so separating the nodes can pan one of them out of the
  // visible pane (autoPanOnNodeDrag). Re-fit so both are on-screen again
  // before anything below computes a click point from their bounding boxes.
  await page.click('.react-flow__controls-fitview');
  await page.waitForTimeout(300);

  const sourceHandle = page
    .locator('.react-flow__node')
    .first()
    .locator('.react-flow__handle.source')
    .first();
  const targetHandle = page
    .locator('.react-flow__node')
    .nth(1)
    .locator('.react-flow__handle.target')
    .first();
  await sourceHandle.waitFor({ timeout: 5_000 });
  await targetHandle.waitFor({ timeout: 5_000 });
  await dragWire(page, (await sourceHandle.boundingBox())!, (await targetHandle.boundingBox())!);
  await expect(page.locator('.react-flow__edge')).toHaveCount(1);
}

test.describe('visual selection and delete', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  });

  test('clicking a node marks it selected on the actual React Flow node', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.waitForSelector('[data-testid="pipeline-node"]');
    await page.click('[data-testid="pipeline-node"]', { force: true });
    // The regression this guards: RF's own `.selected` class (which its internal
    // hit-testing and keyboard handling read) used to never light up because
    // `selected` was never round-tripped onto the controlled node array.
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(1);
  });

  test('select a node then Delete removes it', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.waitForSelector('[data-testid="pipeline-node"]');
    await page.click('[data-testid="pipeline-node"]', { force: true });
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(1);

    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Delete');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(0);
  });

  test('select a node then Backspace removes it, and undo restores it', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.waitForSelector('[data-testid="pipeline-node"]');
    await page.click('[data-testid="pipeline-node"]', { force: true });

    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Backspace');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(0);

    await page.keyboard.press('Control+Z');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(1);
  });

  test('select an edge then Delete removes only the edge, not its nodes', async ({ page }) => {
    await placeWiredPair(page);

    await page.locator('.react-flow__edge').first().click({ force: true });
    await expect(page.locator('.react-flow__edge.selected')).toHaveCount(1, { timeout: 5_000 });

    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Delete');
    await expect(page.locator('.react-flow__edge')).toHaveCount(0);
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);
  });

  test('undo after edge delete restores the wire', async ({ page }) => {
    await placeWiredPair(page);

    await page.locator('.react-flow__edge').first().click({ force: true });
    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Delete');
    await expect(page.locator('.react-flow__edge')).toHaveCount(0);

    await page.keyboard.press('Control+Z');
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);
  });

  test('deleting a node cascades to its wire; a single undo restores both', async ({ page }) => {
    await placeWiredPair(page);

    // Select the source node itself (not the edge) and delete it.
    await page.locator('[data-testid="pipeline-node"]').first().click({ force: true });
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(1);

    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Delete');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(1);
    await expect(page.locator('.react-flow__edge')).toHaveCount(0);

    // One undo brings back both the node and the wire it cascaded away —
    // proving the cascade was a single atomic history entry, not two.
    await page.keyboard.press('Control+Z');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);
  });

  test('select-all (Ctrl+A) selects every node; Delete clears the canvas and undo restores it', async ({
    page,
  }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.click('[data-component="prometheus.remote_write"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);

    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Control+A');
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(2);

    await page.keyboard.press('Delete');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(0);

    await page.keyboard.press('Control+Z');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);
  });

  test('Backspace while renaming a node edits the label instead of deleting the node', async ({
    page,
  }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.waitForSelector('[data-testid="pipeline-node"]');
    await page.click('[data-testid="pipeline-node"]', { force: true });

    const label = page.locator('[data-testid="node-label"]').first();
    await label.dispatchEvent('dblclick');
    const input = page.locator('[data-testid="node-label-input"]').first();
    await expect(input).toBeVisible({ timeout: 3_000 });
    await input.press('Backspace');
    // The node must still exist — Backspace here erased a character, it did not
    // fall through to the canvas-level delete shortcut.
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(1);
  });
});
