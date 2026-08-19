/**
 * Fullstack: a wired visual pipeline saves and its rendered config is a
 * real, working Alloy pipeline (B1/B2 in docs/reviews/README.md).
 *
 * Regression: CanvasPane's onConnect stored React Flow's source->target
 * handle verbatim. React Flow always drags export-kind handles as its own
 * "source" and argument-kind handles as its own "target" — independent of
 * D1's dataflow role — so a receiver-kind wire (prometheus.scrape.forward_to
 * -> prometheus.remote_write.receiver) was stored backwards. L1 then reported
 * `port_role_invalid`, the renderer silently dropped `forward_to`, and the
 * resulting config failed `alloy validate` with "missing required attribute
 * \"forward_to\"". Toolbar.tsx's `canSave` also blocked every such graph, so
 * an operator could not save a metrics pipeline that actually moves data at
 * all.
 *
 * This spec places the canonical discovery -> scrape -> remote_write chain,
 * wires BOTH hops (the ordinary data hop and the receiver hop), fills in the
 * two required fields (discovery.kubernetes.role, remote_write's endpoint
 * url), and saves. A companion script (not run by Playwright) feeds the
 * saved pipeline's persisted `contents` to a real `alloy validate` binary —
 * see the task's verification notes; this spec's job is to prove the UI
 * itself can produce and save that content in the first place.
 */
import { expect, type Locator, type Page, test } from '@playwright/test';
import { loginAsAdmin } from './fixtures';

async function dragWire(page: Page, from: Locator, to: Locator) {
  const fromBox = await from.boundingBox();
  const toBox = await to.boundingBox();
  if (!fromBox || !toBox) throw new Error('handle has no bounding box');
  const fx = fromBox.x + fromBox.width / 2;
  const fy = fromBox.y + fromBox.height / 2;
  const tx = toBox.x + toBox.width / 2;
  const ty = toBox.y + toBox.height / 2;
  await page.mouse.move(fx, fy);
  await page.mouse.down();
  const steps = 20;
  for (let i = 1; i <= steps; i++) {
    await page.mouse.move(fx + ((tx - fx) * i) / steps, fy + ((ty - fy) * i) / steps);
    await page.waitForTimeout(15);
  }
  await page.mouse.up();
  await page.waitForTimeout(600);
}

test('a fully wired discovery -> scrape -> remote_write pipeline saves with both hops rendered', async ({
  page,
}) => {
  await loginAsAdmin(page);
  await page.goto('/pipelines/visual/new');
  await page.waitForLoadState('networkidle');
  await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });

  await page.locator('[data-testid="palette-item-discovery.kubernetes"]').click();
  await page.locator('[data-testid="palette-item-prometheus.scrape"]').click();
  await page.locator('[data-testid="palette-item-prometheus.remote_write"]').click();
  await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(3);

  // All 3 nodes into view before wiring — a real operator reaches for this
  // exact control (React Flow's built-in "fit view" button) once a graph
  // outgrows the visible pane; asserting the wire-drawing that follows still
  // works end to end is the point of this spec, not the auto-fit heuristics.
  await page.locator('.react-flow__controls-fitview').click();
  await page.waitForTimeout(300);

  const nodes = page.locator('.react-flow__node');
  const k8sNode = nodes.nth(0);
  const scrapeNode = nodes.nth(1);
  const remoteWriteNode = nodes.nth(2);

  // Hop 1 (ordinary data export): discovery.kubernetes.targets -> prometheus.scrape.targets.
  await dragWire(
    page,
    k8sNode.locator('[data-handleid="targets"]'),
    scrapeNode.locator('[data-handleid="targets"]'),
  );
  await expect(page.locator('.react-flow__edge')).toHaveCount(1);

  // Hop 2 (receiver export, D1's inverted case): React Flow's strict
  // connectionMode only allows dragging FROM the export (remote_write's
  // `receiver`, an RF `source` handle) TO the argument that references it
  // (scrape's `forward_to`, an RF `target` handle) — the reverse of the
  // dataflow arrow the canvas draws. wireOrient.ts must still store this
  // produces->accepts (forward_to -> receiver), not as dragged.
  await dragWire(
    page,
    remoteWriteNode.locator('[data-handleid="receiver"]'),
    scrapeNode.locator('[data-handleid="forward_to"]'),
  );
  await expect(page.locator('.react-flow__edge')).toHaveCount(2);

  // Fill discovery.kubernetes's one required field.
  await k8sNode.click();
  await expect(page.locator('[data-testid="inspector-component"]')).toHaveText(
    'discovery.kubernetes',
  );
  await page.locator('[data-testid="attr-select-role"]').selectOption('pod');

  // Fill remote_write's required endpoint.url.
  await remoteWriteNode.click();
  await expect(page.locator('[data-testid="inspector-component"]')).toHaveText(
    'prometheus.remote_write',
  );
  await page.locator('[data-testid="block-add-endpoint"]').click();
  await page.locator('[data-testid="attr-input-url"]').fill('http://example.com/api/v1/push');

  // No wire should be reported as both wired and missing at once (task item 5).
  await scrapeNode.click();
  await expect(page.locator('[data-testid="attr-wired-forward_to"]')).toContainText(
    'Wired on the canvas',
  );
  await expect(page.locator('[data-testid="inspector-diagnostic-count"]')).toHaveCount(0);

  const pipelineName = `fs_wiring_pipeline_${Date.now()}`;
  await page.locator('[data-testid="toolbar-name"]').fill(pipelineName);
  const matcherInput = page.locator('[data-testid="matcher-input"]');
  await matcherInput.fill('cluster="prod-eu-1"');
  await matcherInput.press('Enter');
  await expect(page.locator('[data-testid="matcher-chip"]')).toHaveCount(1);

  // The whole point of B1/B2: a correctly wired pipeline with a destination
  // must show zero blocking problems and an enabled Save button.
  await expect(page.locator('[data-testid="toolbar-validity"]')).toContainText('Valid');
  await expect(page.locator('[data-testid="toolbar-save"]')).toBeEnabled();

  await page.locator('[data-testid="toolbar-save"]').click();
  await expect(page).toHaveURL(/\/pipelines\/[^/]+$/, { timeout: 10_000 });

  const pipelineId = new URL(page.url()).pathname.split('/').filter(Boolean).pop();
  if (!pipelineId) throw new Error('no pipeline id in URL after save');

  // Read back the saved content through the real API — this is what
  // `alloy validate` is run against outside this spec (Docker isn't
  // reachable from inside the Playwright browser sandbox).
  const meResp = await page.request.get('/api/me');
  const me = await meResp.json();
  const orgId: string = me.orgs[0].id;
  const getResp = await page.request.post('/shepherd.mgmt.v1.PipelineService/GetPipeline', {
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
    data: { org_id: orgId, id: pipelineId },
  });
  expect(getResp.status()).toBe(200);
  const pipeline = await getResp.json();
  expect(pipeline.contents).toContain('forward_to = [prometheus.remote_write.');
  expect(pipeline.contents).toContain('targets = [discovery.kubernetes.');
  expect(pipeline.contents).toContain('url');
});
