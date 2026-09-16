/**
 * Fullstack: real fleet convergence — enable a pipeline through the UI and
 * watch the REAL Alloy agent (dev/alloy-metrics.alloy, polling every 10s)
 * pick up, apply, and report the new served config, with no shortcut
 * (forceRecompute) triggering the recompute ourselves (W7-09).
 *
 * This is deliberately NOT scenario 6 in pipelines.spec.ts: that test proves
 * the server's recompute-on-GetConfig logic in isolation via a synthetic
 * GetConfig call. This one proves the whole loop the product actually
 * ships: UI enable -> next real agent poll -> served-config recompute ->
 * agent applies -> agent reports status -> UI reflects it.
 *
 * Locate the page-level status badge by its stable test ID so adding
 * collector controls does not break the convergence assertion.
 *
 * Red run (recorded, not re-run by CI):
 *   Control-level: in internal/agentapi/service.go, comment out the
 *   UpdateCollectorInstanceStatus persistence call in the RemoteConfigStatus
 *   branch of GetConfig (so the agent's reported status is read but never
 *   written), rebuild shepherd:local, restart the dev stack's shepherd
 *   container. The status-badge poll below times out at 90s — the real
 *   agent applies the config fine (served-config content still updates),
 *   but the collector's remote_config_status column never leaves its prior
 *   value, so the badge never reads APPLIED. Reverted and rebuilt back to
 *   green afterward.
 */
import { expect, loginAsAdmin, test } from './fixtures';

test.describe('rollout: real fleet convergence', () => {
  test('enabling a pipeline from /pipelines is applied by the real agent and reported back', async ({
    page,
  }) => {
    test.setTimeout(150_000);
    await loginAsAdmin(page);

    const meResp = await page.request.get('/api/me', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    const me = (await meResp.json()) as { orgs: Array<{ id: string; name: string }> };
    const org = me.orgs.find((o) => o.name === 'platform-org');
    if (!org) throw new Error('dev seed must provide platform-org');
    const orgId = org.id;

    const collectorsResp = await page.request.get(`/api/orgs/${orgId}/collectors`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    const collectors = (await collectorsResp.json()) as {
      items: Array<{ id: string; role: string; cluster: string }>;
    };
    const metricsCollector = collectors.items.find(
      (c) => c.role === 'metrics' && c.cluster === 'prod-eu-1',
    );
    if (!metricsCollector) {
      throw new Error('dev seed must provide the prod-eu-1/metrics collector (alloy-metrics)');
    }

    const baselineResp = await page.request.get(
      `/api/orgs/${orgId}/collectors/${metricsCollector.id}/served-config`,
      { headers: { 'X-Requested-With': 'XMLHttpRequest' } },
    );
    const baseline = (await baselineResp.json()) as { hash: string };

    // Create a fresh pipeline matching this exact collector, via the API —
    // only the enable step below goes through the UI (that's the part this
    // spec is actually about).
    // Alloy component block labels must be valid identifiers even quoted (a
    // dash is rejected by `alloy validate`), and this pipeline's contents
    // reuse `name` as its own block label below — underscores only, matching
    // pipelines.spec.ts's fs_pipe_... convention.
    const name = `fs_rollout_${Date.now()}`;
    const createResp = await page.request.post(`/api/orgs/${orgId}/pipelines`, {
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      data: {
        name,
        contents: `prometheus.exporter.self "${name}" { }`,
        matchers: [`cluster="prod-eu-1"`, `role="metrics"`],
      },
    });
    expect(createResp.status()).toBe(201);
    const pipeline = (await createResp.json()) as { id: string };

    try {
      await page.goto('/pipelines');
      // The admin belongs to more than one org, and with no persisted
      // selection in this fresh context, useOrg falls back to orgs[0] —
      // /api/me sorts by name, so that is data-eng, not platform-org, which
      // is where this pipeline actually lives (walkthrough.spec.ts hits the
      // identical default).
      await page.getByTestId('org-switcher').selectOption({ label: 'Platform Engineering' });
      await page.waitForLoadState('networkidle');
      const row = page.getByTestId(`pipeline-row-${name}`);
      await expect(row).toBeVisible();
      await row.getByRole('button', { name: 'Enable' }).click();
      await expect(row.getByRole('button', { name: 'Disable' })).toBeVisible();

      // EnablePipeline recomputes the org's serve caches eagerly, in a
      // goroutine (rpc_pipeline.go's recomputeOrgCaches) — so the SERVER
      // side of this is near-instant and not itself proof of anything about
      // the real fleet. What only the real agent's own poll can do is
      // fetch that new hash, apply it, and report back — that's the part
      // with no shortcut here (no forceRecompute call, by design, W7-09).
      const blockName = `pipe_${name.replace(/-/g, '_')}`;
      await expect
        .poll(
          async () => {
            const resp = await page.request.get(
              `/api/orgs/${orgId}/collectors/${metricsCollector.id}/served-config`,
              { headers: { 'X-Requested-With': 'XMLHttpRequest' } },
            );
            const data = (await resp.json()) as { content: string; hash: string };
            return data.hash !== baseline.hash && data.content.includes(`declare "${blockName}"`);
          },
          { timeout: 30000, intervals: [2000] },
        )
        .toBe(true);

      // The status text alone ("APPLIED") cannot distinguish "applied the
      // NEW config" from "was already APPLIED for the old one and hasn't
      // been touched since" — the string never changes across that
      // transition. So this waits for at least two DISTINCT remote_config
      // report timestamps first: one real poll to fetch the new hash, a
      // second to report having applied it — before trusting the status
      // text at all. Only the real 10s agent poll advances last_seen; nothing
      // in this test does.
      const seenReports = new Set<string>();
      let lastStatus = '';
      await expect(async () => {
        const resp = await page.request.get(
          `/api/orgs/${orgId}/collectors/${metricsCollector.id}`,
          { headers: { 'X-Requested-With': 'XMLHttpRequest' } },
        );
        const detail = (await resp.json()) as { last_seen?: string; remote_config_status?: string };
        if (detail.last_seen) seenReports.add(detail.last_seen);
        lastStatus = (detail.remote_config_status ?? '').toUpperCase();
        expect(
          seenReports.size,
          'need >=2 real agent poll reports since the config changed',
        ).toBeGreaterThanOrEqual(2);
        expect(lastStatus).toBe('APPLIED');
      }).toPass({ timeout: 90000, intervals: [3000] });

      await page.goto(`/collectors/${metricsCollector.id}`);
      await page.waitForLoadState('networkidle');
      // The convergence wait above already did the hard part; this just
      // confirms the UI the task actually names reflects the same state.
      const statusBadge = page.getByTestId('collector-status');
      await expect(statusBadge).toHaveText('APPLIED', { timeout: 20000 });
    } finally {
      await page.request.delete(`/api/orgs/${orgId}/pipelines/${pipeline.id}`, {
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
      });
    }
  });
});
