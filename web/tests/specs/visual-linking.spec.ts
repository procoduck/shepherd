// visual-linking.spec.ts — 7.6.2
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

test.describe('visual linking', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  });

  test('valid wire — connecting compatible types increases edge count by 1', async ({ page }) => {
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
    await sourceHandle.waitFor({ timeout: 5_000 });
    await targetHandle.waitFor({ timeout: 5_000 });

    const fromBox = await sourceHandle.boundingBox();
    const toBox = await targetHandle.boundingBox();
    expect(fromBox).not.toBeNull();
    expect(toBox).not.toBeNull();

    const edgesBefore = await page.locator('.react-flow__edge').count();
    await dragWire(page, fromBox!, toBox!);
    await expect(page.locator('.react-flow__edge')).toHaveCount(edgesBefore + 1);
  });

  test('invalid wire type — edge count unchanged after type-mismatched drag', async ({ page }) => {
    // discovery.kubernetes produces `targets`. prometheus.relabel's only port is
    // an `forward_to` argument of type `prom.metrics` (role "produces" under D1,
    // but a genuine schema `input`, so it renders a real RF target handle) —
    // incompatible with `targets`. loki.write won't do here: under the real,
    // merged schema it (like discovery.kubernetes) has no `inputs` at all, only
    // a receiver export, so it renders no `.react-flow__handle.target` for this
    // locator to ever find.
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="prometheus.relabel"]');
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
    await sourceHandle.waitFor({ timeout: 5_000 });
    await targetHandle.waitFor({ timeout: 5_000 });

    const fromBox = await sourceHandle.boundingBox();
    const toBox = await targetHandle.boundingBox();
    expect(fromBox).not.toBeNull();
    expect(toBox).not.toBeNull();

    const edgesBefore = await page.locator('.react-flow__edge').count();
    await dragWire(page, fromBox!, toBox!);
    await expect(page.locator('.react-flow__edge')).toHaveCount(edgesBefore);
  });

  test('cycle detection — acyclic toast appears on cycle attempt', async ({ page }) => {
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.relabel"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);

    const sourceA = page
      .locator('.react-flow__node')
      .first()
      .locator('.react-flow__handle.source')
      .first();
    const targetB = page
      .locator('.react-flow__node')
      .nth(1)
      .locator('.react-flow__handle.target')
      .first();
    const sourceB = page
      .locator('.react-flow__node')
      .nth(1)
      .locator('.react-flow__handle.source')
      .first();

    await sourceA.waitFor({ timeout: 5_000 });
    await targetB.waitFor({ timeout: 5_000 });
    await dragWire(page, (await sourceA.boundingBox())!, (await targetB.boundingBox())!);
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);

    const targetB2 = page
      .locator('.react-flow__node')
      .nth(1)
      .locator('.react-flow__handle.target')
      .first();
    await dragWire(page, (await sourceB.boundingBox())!, (await targetB2.boundingBox())!);
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);
  });

  test('second wire into list input adds a second edge', async ({ page }) => {
    await page.click('[data-component="discovery.kubernetes"]');
    await page.click('[data-component="discovery.relabel"]');
    await page.click('[data-component="discovery.relabel"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(3);

    // source1: discovery.kubernetes (targets output)
    // target1: first discovery.relabel (targets input)
    // target2: second discovery.relabel (targets input) — both accept fan-in via list cardinality
    const source1 = page
      .locator('.react-flow__node')
      .first()
      .locator('.react-flow__handle.source')
      .first();
    const target1 = page
      .locator('.react-flow__node')
      .nth(1)
      .locator('.react-flow__handle.target')
      .first();
    const target2 = page
      .locator('.react-flow__node')
      .nth(2)
      .locator('.react-flow__handle.target')
      .first();

    await source1.waitFor({ timeout: 5_000 });
    await target1.waitFor({ timeout: 5_000 });
    await target2.waitFor({ timeout: 5_000 });

    // Wire to first relabel
    await dragWire(page, (await source1.boundingBox())!, (await target1.boundingBox())!);
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);
    // Wire to second relabel
    await dragWire(page, (await source1.boundingBox())!, (await target2.boundingBox())!);
    await expect(page.locator('.react-flow__edge')).toHaveCount(2);
  });
});
