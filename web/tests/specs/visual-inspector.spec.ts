// visual-inspector.spec.ts — 7.6.3 (partial, no Code tab yet)
import { expect } from '@playwright/test';
import { settledBox } from '../fixtures/canvas';
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

  test('fan-in reorder control (W5-08): two wires into a list port can be swapped', async ({
    page,
  }) => {
    // Same pipeline shape as visual-linking's "second wire into list input
    // adds a second edge": two discovery.kubernetes sources wired into one
    // discovery.relabel's `targets` (a real, schema-declared
    // cardinality:'list' accepts port — internal/schema/artifacts/alloy-*.json).
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.relabel"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(3);

    const source1 = page
      .locator('.react-flow__node')
      .nth(0)
      .locator('.react-flow__handle.source')
      .first();
    const source2 = page
      .locator('.react-flow__node')
      .nth(1)
      .locator('.react-flow__handle.source')
      .first();
    const target = page
      .locator('.react-flow__node')
      .nth(2)
      .locator('.react-flow__handle.target')
      .first();
    await source1.waitFor({ timeout: 5_000 });
    await source2.waitFor({ timeout: 5_000 });
    await target.waitFor({ timeout: 5_000 });

    const drag = async (
      from: { x: number; y: number; width: number; height: number },
      to: { x: number; y: number; width: number; height: number },
    ) => {
      await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
      await page.mouse.down();
      await page.waitForTimeout(100);
      const steps = 20;
      for (let i = 1; i <= steps; i++) {
        await page.mouse.move(
          from.x + from.width / 2 + ((to.x + to.width / 2 - (from.x + from.width / 2)) * i) / steps,
          from.y +
            from.height / 2 +
            ((to.y + to.height / 2 - (from.y + from.height / 2)) * i) / steps,
        );
        await page.waitForTimeout(15);
      }
      await page.mouse.up();
      await page.waitForTimeout(400);
    };
    await drag(await settledBox(source1), await settledBox(target));
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);
    const target2 = page
      .locator('.react-flow__node')
      .nth(2)
      .locator('.react-flow__handle.target')
      .first();
    await drag(await settledBox(source2), await settledBox(target2));
    await expect(page.locator('.react-flow__edge')).toHaveCount(2);

    // Select the relabel node to see its inspector, with the fan-in list.
    const relabelId = await page
      .locator('[data-testid="pipeline-node"]')
      .nth(2)
      .getAttribute('data-node-id');
    await page.click(`[data-node-id="${relabelId}"]`, { force: true });
    await expect(page.locator('[data-testid="inspector"]')).toContainText('discovery.relabel', {
      timeout: 5_000,
    });

    const orderList = page.locator('[data-testid="attr-wire-order-targets"]');
    await expect(orderList).toBeVisible();
    await expect(page.locator('[data-testid="attr-wire-up-targets-0"]')).toBeDisabled();
    await expect(page.locator('[data-testid="attr-wire-down-targets-1"]')).toBeDisabled();

    await page.click('[data-testid="attr-wire-down-targets-0"]');
    // After the swap, what was row 0 is now disabled at the bottom (row 1).
    await expect(page.locator('[data-testid="attr-wire-down-targets-1"]')).toBeDisabled();
    await expect(page.locator('[data-testid="attr-wire-up-targets-0"]')).toBeDisabled();
  });

  test('Code tab (W5-10): the render is debounced, not recomputed on every keystroke', async ({
    page,
  }) => {
    // discovery.kubernetes' `role` is a required, top-level attribute
    // (internal/schema/artifacts/alloy-*.json, overlaid with an enum of
    // values) — visible without expanding "Show optional attributes" first,
    // and absent from the render until set, so `role = "pod"` appearing is
    // an unambiguous marker of "the edit reached the render".
    await page.click('[data-component="discovery.kubernetes"]');
    await page.waitForSelector('[data-testid="pipeline-node"]', { timeout: 5_000 });
    await page.click('[data-testid="pipeline-node"]', { force: true });
    await expect(page.locator('[data-testid="inspector"]')).toContainText('discovery.kubernetes', {
      timeout: 5_000,
    });

    await page.click('[data-testid="drawer-toggle"]');
    await page.click('[data-testid="drawer-tab-code"]');
    const codeContent = page.locator('[data-testid="code-tab-content"]');
    await expect(codeContent).toBeVisible({ timeout: 3_000 });
    await expect(codeContent).not.toContainText('role = "pod"');

    await page.selectOption('[data-testid="attr-select-role"]', 'pod');
    // Immediately after the edit (well under the 300ms debounce), the Code
    // tab must still show the OLD render — the value hasn't been committed
    // to it yet. This is the assertion that is false today (renderTS runs
    // on every keystroke, synchronously): it must go red before the fix.
    await page.waitForTimeout(60);
    await expect(codeContent).not.toContainText('role = "pod"');
    // Once the debounce elapses, the render catches up.
    await expect(codeContent).toContainText('role = "pod"', { timeout: 2_000 });
  });

  test('placing two nodes and selecting second shows correct component', async ({ page }) => {
    await page.click('[data-component="prometheus.scrape"]');
    await page.click('[data-component="prometheus.remote_write"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);
    // Two nodes placed this far apart (task item 7's fix: staggered spacing
    // wide enough that PipelineNode boxes never overlap) can land the second
    // one outside the pane's default 1:1 viewport — the same "zoom out
    // before you can reach it" gap the review measured operators hitting.
    // The canvas's own fit-view control (`.react-flow__controls-fitview`) is
    // the one-click recovery for exactly that; use it before targeting the
    // second node so `force: true` below clicks its real, visible position
    // rather than an off-pane one `overflow-hidden` would clip anyway.
    await page.locator('.react-flow__controls-fitview').click();
    await page.waitForTimeout(300);
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
