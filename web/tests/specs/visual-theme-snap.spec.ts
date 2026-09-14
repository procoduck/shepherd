import type { Locator } from '@playwright/test';
import { settledBox } from '../fixtures/canvas';
import { basicScenario } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { expect, test } from '../fixtures/test';

// Canvas drag interaction for the light-mode work (F2): lives in a visual-*
// spec because pointer-driven snapping needs the short waitForTimeout pauses
// wait-budget.spec.ts reserves for visual specs.

function backgroundAlpha(color: string): number {
  const slash = color.match(/\/\s*([\d.]+)\s*\)\s*$/);
  if (slash) return Number(slash[1]);
  const legacyRgba = color.match(/^rgba\(\s*[\d.]+%?,\s*[\d.]+%?,\s*[\d.]+%?,\s*([\d.]+)\s*\)$/);
  if (legacyRgba) return Number(legacyRgba[1]);
  return 1;
}

async function center(locator: Locator) {
  const box = await settledBox(locator);
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

test.describe('snapped-drop tint stays opaque', () => {
  test("a snapped node's background is opaque, tinted, in both dark and light mode", async ({
    page,
    api,
  }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });

    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });

    // discovery.kubernetes: source, output type `targets`.
    // discovery.relabel: compatible target, input type `targets`.
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.relabel"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);

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

    const idleBg = await relabelNode.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(backgroundAlpha(idleBg)).toBe(1);

    async function snapAndReadBackground(): Promise<string> {
      const from = await center(sourceHandle);
      const to = await center(targetHandle);
      await page.mouse.move(from.x, from.y);
      await page.mouse.down();
      // Interpolated by Playwright: each step fires a pointermove the canvas
      // sees, without a timed pause between them.
      await page.mouse.move(to.x, to.y, { steps: 10 });
      await expect(relabelNode).toHaveAttribute('data-drop-state', 'snapped', { timeout: 3_000 });
      const bg = await relabelNode.evaluate((el) => getComputedStyle(el).backgroundColor);
      // Move away from the handle before releasing so no edge is actually
      // created — this same node pair is reused for the light-mode pass.
      await page.mouse.move(from.x + 10, from.y - 60, { steps: 5 });
      await page.mouse.up();
      await expect(relabelNode).toHaveAttribute('data-drop-state', 'idle');
      await expect(page.locator('.react-flow__edge')).toHaveCount(0);
      return bg;
    }

    const darkSnappedBg = await snapAndReadBackground();
    expect(backgroundAlpha(darkSnappedBg)).toBe(1);
    expect(darkSnappedBg).not.toBe(idleBg);

    const themeBtn = page.getByRole('button', { name: /toggle theme/i });
    await themeBtn.click();
    await expect(page.locator('html')).toHaveClass(/light/);
    // bg-card itself is theme-flipped (index.css), so the idle background
    // changes on toggle even though this node received no new props.
    await expect
      .poll(async () => relabelNode.evaluate((el) => getComputedStyle(el).backgroundColor))
      .not.toBe(idleBg);
    const lightIdleBg = await relabelNode.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(backgroundAlpha(lightIdleBg)).toBe(1);

    const lightSnappedBg = await snapAndReadBackground();
    expect(backgroundAlpha(lightSnappedBg)).toBe(1);
    expect(lightSnappedBg).not.toBe(lightIdleBg);
  });
});
