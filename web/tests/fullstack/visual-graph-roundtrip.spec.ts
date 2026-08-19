/**
 * Fullstack: visual builder graph round-trip (F7 in
 * docs/reviews/graph-model-and-validation.md; D3 in
 * docs/visual-builder-design-VB1.md).
 *
 * Regression: save wrote `wizard_state`, but load ignored it and re-parsed
 * the generated Alloy text via VisualService.GraphView instead — which
 * regenerates node ids on a fixed grid and drops positions, labels, notes,
 * disabled flags, bindings and non-scalar props. A node dragged to an
 * arbitrary position and renamed must reappear exactly where it was left,
 * not snapped back to the text re-parser's grid origin, after a real
 * save → reload round trip against the live backend.
 */
import { expect, type Locator, test } from '@playwright/test';
import { loginAsAdmin } from './fixtures';

/** Reads a React Flow node's raw model position straight off its wrapper's
 * `transform: translate(Xpx, Ypx)` inline style — the graph coordinate, not
 * the on-screen pixel position, so it's unaffected by any pan/zoom the
 * canvas applies on load. */
async function translateOf(locator: Locator): Promise<{ x: number; y: number } | null> {
  return locator.evaluate((el) => {
    const m = /translate\(([-\d.]+)px,\s*([-\d.]+)px\)/.exec((el as HTMLElement).style.transform);
    return m ? { x: Number.parseFloat(m[1]), y: Number.parseFloat(m[2]) } : null;
  });
}

test('a visual pipeline’s positions and labels survive save → reload unchanged', async ({
  page,
}) => {
  await loginAsAdmin(page);
  await page.goto('/pipelines/visual/new');
  await page.waitForLoadState('networkidle');
  await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });

  await page.locator('[data-testid="palette-item-prometheus.remote_write"]').first().click();
  const flowNode = page.locator('.react-flow__node').first();
  await expect(flowNode).toBeVisible();

  // Drag the node to a position clearly distinct from both the
  // click-to-place default (80,80) and the text re-parser's regenerated
  // grid origin (0,0) — if load ever regresses to that fallback, this
  // position will NOT survive. Clamp the target to stay inside the canvas
  // pane (minus the node's own footprint) so it can't end up dragged
  // underneath the inspector panel, where it would still be visible but
  // unclickable.
  const canvasBox = await page.locator('[data-testid="canvas"]').boundingBox();
  const box = await flowNode.boundingBox();
  if (!box || !canvasBox) throw new Error('node or canvas has no bounding box');
  const grabOffset = { x: 20, y: 10 }; // header area, clear of the port handles
  const from = { x: box.x + grabOffset.x, y: box.y + grabOffset.y };
  const margin = 20;
  // Keep the target inside the canvas AND out of its bottom-left minimap
  // panel (react-flow__minimap), which would otherwise intercept the
  // later double-click on the node's label.
  const maxLeft = canvasBox.x + canvasBox.width - box.width - margin;
  const minTop = canvasBox.y + margin;
  const maxTop = canvasBox.y + canvasBox.height * 0.45;
  const targetLeft = Math.min(maxLeft, box.x + 200);
  const targetTop = Math.min(maxTop, Math.max(minTop, box.y + 120));
  const to = { x: targetLeft + grabOffset.x, y: targetTop + grabOffset.y };
  await page.mouse.move(from.x, from.y);
  await page.mouse.down();
  await page.mouse.move(to.x, to.y, { steps: 10 });
  await page.mouse.up();

  const posBefore = await translateOf(flowNode);
  expect(posBefore, 'dragged node should report a parsable transform').not.toBeNull();

  // Renderer sanitizes block labels to valid Alloy identifiers (dashes ->
  // underscores, see render.go/renderTS.ts) — stick to identifier-safe
  // characters so the label survives a save -> render -> reload round trip
  // unchanged, which is what this test actually wants to isolate.
  const uniqueLabel = `fs_roundtrip_node_${Date.now()}`;
  // dispatchEvent, not .dblclick(): a real double-click's mousedown also
  // starts a React Flow node drag, whose threshold/timing can eat the
  // second click — see visual-canvas.spec.ts's identical rename test.
  await page.locator('[data-testid="node-label"]').dispatchEvent('dblclick');
  await expect(page.locator('[data-testid="node-label-input"]')).toBeVisible({ timeout: 3_000 });
  await page.locator('[data-testid="node-label-input"]').fill(uniqueLabel);
  await page.locator('[data-testid="node-label-input"]').press('Enter');
  await expect(page.locator('[data-testid="node-label"]')).toContainText(uniqueLabel);

  await page.locator('[data-testid="toolbar-name"]').fill(`fs_roundtrip_pipeline_${Date.now()}`);
  const matcherInput = page.locator('[data-testid="matcher-input"]');
  await matcherInput.fill('cluster="prod-eu-1"');
  await matcherInput.press('Enter');
  await expect(page.locator('[data-testid="matcher-chip"]')).toHaveCount(1);

  await page.locator('[data-testid="toolbar-save"]').click();
  await expect(page).toHaveURL(/\/pipelines\/[^/]+$/, { timeout: 10_000 });

  const pipelineId = new URL(page.url()).pathname.split('/').filter(Boolean).pop();
  if (!pipelineId) throw new Error('no pipeline id in URL after save');

  // Reload the SAME pipeline into a fresh visual builder session.
  await page.goto(`/pipelines/${pipelineId}/visual`);
  await page.waitForLoadState('networkidle');
  const reloadedNode = page.locator('.react-flow__node').first();
  await expect(reloadedNode).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(1);

  await expect(page.locator('[data-testid="node-label"]')).toContainText(uniqueLabel);

  const posAfter = await translateOf(reloadedNode);
  expect(posAfter, 'reloaded node should report a parsable transform').not.toBeNull();
  // Round to the nearest pixel — the two positions must be model-identical,
  // not merely close, but sub-pixel float noise from the drag isn't the point
  // of this assertion.
  expect(Math.round(posAfter?.x ?? Number.NaN)).toBe(Math.round(posBefore?.x ?? Number.NaN));
  expect(Math.round(posAfter?.y ?? Number.NaN)).toBe(Math.round(posBefore?.y ?? Number.NaN));
});
