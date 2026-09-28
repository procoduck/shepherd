/**
 * Fullstack: an S3 sandbox run against the real simulator, driven from the
 * visual builder's Simulate menu (W7-11).
 *
 * Gated behind FULLSTACK_SIM=1 — the `sim` compose profile brings up a real
 * `grafana/alloy`-backed simulator container (dev/docker-compose.dev.yaml),
 * which `make test-fullstack`'s plain dev stack does not start. Run this
 * spec with `make test-fullstack-sim` (local-only; NOT wired into CI —
 * D13). Unset FULLSTACK_SIM (the default `make test-fullstack` run) skips
 * this file outright rather than failing it.
 *
 * Before touching the UI, the test itself probes for simulator enablement
 * the same way the real server would tell it apart from a missing profile:
 * SimulateService.CreateRun checks `cfg.Simulator.Enabled` before anything
 * else (internal/mgmtapi/rpc_simulate.go), so a deliberately incomplete
 * CreateRun call surfaces "sandbox simulation is not enabled on this
 * server" when the profile is absent, distinct from any other rejection
 * (e.g. the graph-shape error it would give once actually enabled).
 *
 * Red run (recorded, not re-run by CI):
 *   FULLSTACK_SIM=1 pnpm exec playwright test --config
 *   playwright.fullstack.config.ts tests/fullstack/sandbox-run.spec.ts run
 *   against the PLAIN dev stack (no `sim` profile, `SHEPHERD_SIM_ENABLED`
 *   unset) fails at the probe with exactly:
 *     simulator not enabled in this stack — run make test-fullstack-sim
 */
import { expect, getMe, loginAsAdmin, rpc, test } from './fixtures';

test.describe('sandbox-run', () => {
  test.skip(
    !process.env.FULLSTACK_SIM,
    'requires the sim compose profile — run `make test-fullstack-sim` (sets FULLSTACK_SIM=1)',
  );

  test('sandbox run on the demo-visual pipeline shows healthy components', async ({ page }) => {
    test.setTimeout(150_000);
    await loginAsAdmin(page);

    const me = await getMe(page);
    const org = me.orgs.find((o) => o.name === 'platform-org');
    if (!org) throw new Error('dev seed must provide platform-org');
    const orgId = org.id;

    // Probe: a deliberately graph-less CreateRun. If the simulator profile
    // is absent, cfg.Simulator.Enabled is false and this is rejected before
    // the graph is ever inspected — that specific message is the signal.
    const probeResp = await rpc(page, 'SimulateService', 'CreateRun', { orgId });
    const probeBody = (await probeResp.json().catch(() => ({}))) as { message?: string };
    if ((probeBody.message ?? '').includes('not enabled')) {
      throw new Error('simulator not enabled in this stack — run make test-fullstack-sim');
    }

    const listResp = await rpc(page, 'PipelineService', 'ListPipelines', { orgId });
    const list = (await listResp.json()) as { items?: Array<{ id: string; name: string }> };
    const demo = (list.items ?? []).find((p) => p.name === 'demo-visual');
    if (!demo) throw new Error('dev seed must contain the demo-visual pipeline');

    await page.goto(`/pipelines/${demo.id}/visual`);
    await expect(page.getByTestId('pipeline-node').first()).toBeVisible({ timeout: 20000 });
    await expect(page.getByTestId('pipeline-node')).toHaveCount(3);

    await page.getByTestId('simulate-menu-trigger').click();
    await expect(page.getByTestId('simulate-menu')).toBeVisible();
    await page.getByTestId('simulate-menu-sandbox-run').click();

    await expect(page.getByTestId('sandbox-run-dialog')).toBeVisible();
    // The run genuinely takes ~30s (SandboxRunPanel's REQUESTED_DURATION_SECONDS)
    // plus queue/transform/collect time — no shortcut here, this is the real harness.
    await expect(page.getByTestId('sandbox-run-results')).toBeVisible({ timeout: 90000 });
    await expect(page.getByTestId('sandbox-run-status-error')).toHaveCount(0);

    await page.getByTestId('sim-results-tab-health').click();
    const healthTable = page.getByTestId('sim-results-health');
    await expect(healthTable).toBeVisible();
    await expect(healthTable.getByTestId('sim-health-empty')).toHaveCount(0);

    const rows = page.getByTestId('sim-health-row');
    await expect(rows.first()).toBeVisible();
    const rowCount = await rows.count();
    expect(rowCount).toBeGreaterThan(0);
    // demo-visual is discovery.kubernetes -> prometheus.scrape ->
    // prometheus.remote_write, the exact shape docs/proofs/sandbox-sim-e2e.md
    // §2 records as healthy end to end (including the stubbed
    // discovery.kubernetes) — every reported component must read healthy.
    for (let i = 0; i < rowCount; i++) {
      await expect(rows.nth(i)).toHaveAttribute('data-health-state', 'healthy');
    }
  });
});
