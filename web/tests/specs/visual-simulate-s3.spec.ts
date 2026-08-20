// visual-simulate-s3.spec.ts — VB-1 design doc §6.4 step 4: the S3 sandbox
// run UI (Simulate menu -> progress -> results -> per-node health badges ->
// "what was rewritten" disclosure -> fidelity note).
//
// The mock's GetRun (web/tests/mocks/handlers.ts) advances queued -> running
// -> running -> terminal off its own call count, not real time, so these
// specs observe the whole progression without faking wall-clock timing.
import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

// Generous but bounded: results land ~1.5s after the run starts (see
// handlers.ts's call-count thresholds), well under this on any machine —
// this is a ceiling, not an expected duration.
const RESULTS_TIMEOUT = 10_000;

test.describe('visual simulate S3 — sandbox run', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  });

  test('7.6.8.1 — successful run: progress, results tabs, canvas health badge, rewrites, fidelity note', async ({
    page,
    api,
  }) => {
    // Two nodes so the health-badge assertion below proves the PER-NODE
    // wiring, not just "a badge exists somewhere": if CanvasPane ever
    // projected the whole health map onto every node instead of keying by
    // id, both nodes would show identically and this test would still pass
    // unless we check the SECOND node stays unbadged.
    await page.click('[data-component="prometheus.scrape"]');
    await page.click('[data-component="prometheus.remote_write"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);
    const scrapeId = await page
      .locator('[data-testid="pipeline-node"]')
      .first()
      .getAttribute('data-node-id');
    const writeId = await page
      .locator('[data-testid="pipeline-node"]')
      .nth(1)
      .getAttribute('data-node-id');
    if (!scrapeId || !writeId) throw new Error('placed nodes did not get ids');

    api.seed({
      simulateRunResult: {
        status: 'completed',
        requested_duration_seconds: 1,
        rewrites: [
          {
            node_id: writeId,
            node_label: 'remote_write',
            component: 'prometheus.remote_write',
            kind: 'destination_endpoint',
            detail: 'endpoint rewritten to the harness capture receiver',
          },
        ],
        captured_series: [
          { name: 'synthetic_requests_total', labels: { job: 'synthetic' }, sample_count: 12 },
          { name: 'synthetic_latency_bucket', labels: { job: 'synthetic' }, sample_count: 30 },
        ],
        captured_log_lines: [],
        component_health: [
          {
            node_id: scrapeId,
            node_label: 'scrape',
            component: 'prometheus.scrape',
            health_state: 'unhealthy',
            message: 'context deadline exceeded scraping synthetic target',
          },
        ],
        fidelity_note: 'TEST FIDELITY NOTE — S3 stubs discovery and drops all secrets.',
      },
    });

    await page.getByTestId('simulate-menu-trigger').click();
    await page.getByTestId('simulate-menu-sandbox-run').click();

    // The dialog opens straight into a progress view, not results —
    // asserting this ordering is what the "collecting"/"transforming"
    // states as themselves would be tested against.
    await expect(page.getByTestId('sandbox-run-progress')).toBeVisible();

    const results = page.getByTestId('sandbox-run-results');
    await expect(results).toBeVisible({ timeout: RESULTS_TIMEOUT });

    // Fidelity note — §6.5's required one-liner, content-level.
    await expect(page.getByTestId('sandbox-run-fidelity-note')).toHaveText(
      'TEST FIDELITY NOTE — S3 stubs discovery and drops all secrets.',
    );

    // Tab labels carry the real counts from the seeded run.
    await expect(page.getByTestId('sim-results-tab-metrics')).toHaveText('Metrics captured (2)');
    await expect(page.getByTestId('sim-results-tab-logs')).toHaveText('Logs captured (0)');
    await expect(page.getByTestId('sim-results-tab-health')).toHaveText('Component health (1)');

    // Metrics tab (default) — both series present.
    await expect(page.getByTestId('sim-series-row')).toHaveCount(2);
    // Filter narrows the table — a real filter, not a decorative input:
    // breaking the filter's substring match leaves this at 2, not 1.
    await page.getByTestId('sim-series-filter').fill('latency');
    await expect(page.getByTestId('sim-series-row')).toHaveCount(1);
    await expect(page.getByTestId('sim-series-row')).toContainText('synthetic_latency_bucket');
    await page.getByTestId('sim-series-filter').fill('');

    // Component health tab.
    await page.getByTestId('sim-results-tab-health').click();
    const healthRow = page.getByTestId('sim-health-row');
    await expect(healthRow).toHaveCount(1);
    await expect(healthRow).toHaveAttribute('data-health-state', 'unhealthy');
    await expect(healthRow).toContainText('context deadline exceeded');

    // The single best debugging affordance (§6.4 step 4): the SAME health
    // state painted onto the matching canvas node, and NOT onto the other
    // one — proves the per-node id projection, not just that a badge exists.
    const scrapeBadge = page.locator(
      `[data-node-id="${scrapeId}"] [data-testid="node-health-badge"]`,
    );
    await expect(scrapeBadge).toHaveAttribute('data-health-state', 'unhealthy');
    await expect(
      page.locator(`[data-node-id="${writeId}"] [data-testid="node-health-badge"]`),
    ).toHaveCount(0);

    // "What was rewritten" disclosure — content-level, not just "is visible".
    const rewriteRow = page.getByTestId('sandbox-run-rewrite-row');
    await expect(rewriteRow).toHaveCount(1);
    await expect(rewriteRow).toContainText('destination_endpoint');
    await expect(rewriteRow).toContainText('endpoint rewritten to the harness capture receiver');

    // Closing the dialog clears the canvas badges — they're scoped to
    // "while viewing", not a permanent decoration.
    await page.getByTestId('sandbox-run-close').click();
    await expect(page.getByTestId('sandbox-run-overlay')).toHaveCount(0);
    await expect(page.locator('[data-testid="node-health-badge"]')).toHaveCount(0);
  });

  test('7.6.8.2 — failed run surfaces the error, not a fake success view', async ({
    page,
    api,
  }) => {
    api.seed({
      simulateRunResult: {
        status: 'failed',
        error_code: 'cannot_stub',
        error_message: 'cannot stub discovery.docker — use S2 for its downstream rules',
        rewrites: [],
        captured_series: [],
        captured_log_lines: [],
        component_health: [],
      },
    });

    await page.getByTestId('simulate-menu-trigger').click();
    await page.getByTestId('simulate-menu-sandbox-run').click();

    const statusError = page.getByTestId('sandbox-run-status-error');
    await expect(statusError).toBeVisible({ timeout: RESULTS_TIMEOUT });
    await expect(statusError).toContainText('cannot_stub');
    await expect(statusError).toContainText(
      'cannot stub discovery.docker — use S2 for its downstream rules',
    );
    // A failed run still renders the results shell (fidelity note, empty
    // tabs) rather than a dead end — the design's own point that failures
    // are the signal, not a crash.
    await expect(page.getByTestId('sandbox-run-fidelity-note')).toBeVisible();
  });

  test('7.6.8.3 — CreateRun failure shows the real server error, not a silent hang', async ({
    page,
    api,
  }) => {
    // 500 is on the harness's console-error allowlist (test.ts) alongside
    // 503/422 — a mock-injected failure is expected to log a failed resource
    // load, unlike a real unhandled error.
    api.seed({
      simulateCreateRunError: {
        status: 500,
        code: 'resource_exhausted',
        message: 'too many concurrent sandbox runs for this organisation',
      },
    });

    await page.getByTestId('simulate-menu-trigger').click();
    await page.getByTestId('simulate-menu-sandbox-run').click();

    const err = page.getByTestId('sandbox-run-error');
    await expect(err).toBeVisible();
    await expect(err).toContainText('too many concurrent sandbox runs');
    // No progress/results content leaks through alongside the error.
    await expect(page.getByTestId('sandbox-run-results')).toHaveCount(0);
  });
});
