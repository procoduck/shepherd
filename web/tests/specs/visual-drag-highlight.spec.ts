// visual-drag-highlight.spec.ts — A2/A3: connection-drag affordance states,
// driven by real mouse events and asserted via the data-drop-state attribute
// (not raw classes, per D2) that PipelineNode's root exposes for exactly
// this purpose.
import { expect, type Locator, type Page } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

async function center(locator: Locator) {
  const box = await locator.boundingBox();
  if (!box) throw new Error('locator has no bounding box');
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

async function dropStates(page: Page) {
  return page.locator('[data-testid="pipeline-node"]').evaluateAll((els) =>
    els.map((el) => ({
      component: el.querySelector('[data-testid="node-category-icon"]')?.nextSibling?.textContent,
      dropState: el.getAttribute('data-drop-state'),
    })),
  );
}

test.describe('visual drag highlight (A2/A3)', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });

    // discovery.kubernetes: source, output type `targets`.
    // discovery.relabel: compatible target, input type `targets`.
    // loki.write: incompatible target, input type `loki.logs` only.
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.relabel"]');
    await page.click('[data-component="loki.write"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(3);
  });

  test('idle before a drag starts', async ({ page }) => {
    const states = await dropStates(page);
    for (const s of states) expect(s.dropState).toBe('idle');
  });

  test('compatible node goes valid and incompatible node dims mid-drag; both revert to idle on drop', async ({
    page,
  }) => {
    const sourceHandle = page
      .locator('.react-flow__node')
      .first()
      .locator('.react-flow__handle.source')
      .first();
    const relabelNode = page.locator('[data-testid="pipeline-node"]').nth(1);
    const lokiNode = page.locator('[data-testid="pipeline-node"]').nth(2);
    await sourceHandle.waitFor({ timeout: 5_000 });

    const from = await center(sourceHandle);
    await page.mouse.move(from.x, from.y);
    await page.mouse.down();
    await page.waitForTimeout(100);
    // Move a little, away from any handle, so we're not accidentally "snapped".
    await page.mouse.move(from.x + 40, from.y + 10);
    await page.waitForTimeout(100);

    await expect(relabelNode).toHaveAttribute('data-drop-state', 'valid', { timeout: 3_000 });
    await expect(lokiNode).toHaveAttribute('data-drop-state', 'dimmed', { timeout: 3_000 });

    await page.mouse.up();
    await page.waitForTimeout(300);

    await expect(relabelNode).toHaveAttribute('data-drop-state', 'idle');
    await expect(lokiNode).toHaveAttribute('data-drop-state', 'idle');
  });

  test('hovering a valid handle within connectionRadius snaps the wire', async ({ page }) => {
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
    const relabelNode = page.locator('[data-testid="pipeline-node"]').nth(1);
    await sourceHandle.waitFor({ timeout: 5_000 });
    await targetHandle.waitFor({ timeout: 5_000 });

    const from = await center(sourceHandle);
    const to = await center(targetHandle);
    await page.mouse.move(from.x, from.y);
    await page.mouse.down();
    await page.waitForTimeout(100);
    // Step toward the target handle so React Flow's connectionRadius picks it up.
    const steps = 10;
    for (let i = 1; i <= steps; i++) {
      await page.mouse.move(
        from.x + ((to.x - from.x) * i) / steps,
        from.y + ((to.y - from.y) * i) / steps,
      );
      await page.waitForTimeout(20);
    }

    await expect(relabelNode).toHaveAttribute('data-drop-state', 'snapped', { timeout: 3_000 });

    await page.mouse.up();
    await page.waitForTimeout(300);
    await expect(relabelNode).toHaveAttribute('data-drop-state', 'idle');
    // The wire itself connected — connection count increases, confirming the
    // snap-to-handle behavior (connectionRadius) actually completed the edge.
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);
  });
});
