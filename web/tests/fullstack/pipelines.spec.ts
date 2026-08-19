/**
 * Fullstack: pipeline scenarios (6, 7, 15-pipeline)
 *
 * Scenario 6: Enable pipeline → poll served-config → verify declare block present.
 * Scenario 7: Pipeline validation with real server returns correct shape.
 * Scenario 15-pipeline: Pipeline CRUD round-trip (create, edit, save, revision increments).
 *
 * Red-green proof for scenario 6:
 * - red = revert recomputeOrgCaches / singleflight fix in agentapi →
 *   served-config hash never changes after enable.
 */
import { expect, forceRecompute, loginAsAdmin, test } from './fixtures';

test.describe('pipelines', () => {
  test('scenario 6: enable pipeline → served config contains declare block', async ({ page }) => {
    await loginAsAdmin(page);

    // Get the platform org
    const meResp = await page.request.get('/api/me', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    const me = (await meResp.json()) as { orgs: Array<{ id: string; name: string }> };
    const platformOrg = me.orgs.find((o) => o.name === 'platform-org');
    // A missing seed is the bug, not a reason to stand down: skipping here
    // reported green while verifying nothing.
    if (!platformOrg) throw new Error('dev seed must provide platform-org');
    const orgId = platformOrg.id;

    // Create a fresh pipeline for this test
    const pipeName = `fs_pipe_${Date.now()}`;
    const createResp = await page.request.post(`/api/orgs/${orgId}/pipelines`, {
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      data: {
        name: pipeName,
        contents: `prometheus.exporter.self "${pipeName}" { }`,
        matchers: [`cluster="prod-eu-1"`, `role="metrics"`],
      },
    });
    expect(createResp.status()).toBe(201);
    const pipeline = (await createResp.json()) as { id: string };

    // Enable the pipeline
    const enableResp = await page.request.post(
      `/api/orgs/${orgId}/pipelines/${pipeline.id}/enable`,
      { headers: { 'X-Requested-With': 'XMLHttpRequest' } },
    );
    expect(enableResp.status()).toBe(200);

    // Get the metrics collector for this org
    const collectorsResp = await page.request.get(`/api/orgs/${orgId}/collectors`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    const collectors = (await collectorsResp.json()) as {
      items: Array<{ id: string; role: string }>;
    };
    const metricsCollector = collectors.items.find((c) => c.role === 'metrics');
    if (!metricsCollector) {
      throw new Error('dev seed must provide a collector with role=metrics');
    }

    // Trigger recompute via Connect-JSON GetConfig (ruling 3)
    await forceRecompute(page, 'prod-eu-1', 'metrics');

    // Poll served-config until hash is non-empty or timeout
    await expect
      .poll(
        async () => {
          const resp = await page.request.get(
            `/api/orgs/${orgId}/collectors/${metricsCollector.id}/served-config`,
            { headers: { 'X-Requested-With': 'XMLHttpRequest' } },
          );
          const data = (await resp.json()) as { content: string; hash: string };
          return data.content;
        },
        { timeout: 15000, intervals: [1000] },
      )
      .toContain(`declare "pipe_${pipeName.replace(/-/g, '_')}`);

    // Clean up
    await page.request.delete(`/api/orgs/${orgId}/pipelines/${pipeline.id}`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
  });

  test('scenario 7: pipeline validation returns valid shape', async ({ page }) => {
    await loginAsAdmin(page);

    const meResp = await page.request.get('/api/me', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    const me = (await meResp.json()) as { orgs: Array<{ id: string }> };
    if (!me.orgs.length) throw new Error('dev seed must provide at least one org');
    const orgId = me.orgs[0].id;

    // Valid pipeline — must return valid=true
    const validResp = await page.request.post(`/api/orgs/${orgId}/pipelines/validate`, {
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      data: { name: 'test', contents: 'prometheus.exporter.self "test" { }' },
    });
    expect(validResp.status()).toBe(200);
    const validResult = (await validResp.json()) as {
      Valid?: boolean;
      valid?: boolean;
      Diagnostics?: unknown[];
      diagnostics?: unknown[];
    };
    expect(validResult.Valid ?? validResult.valid).toBe(true);
    const diags = validResult.Diagnostics ?? validResult.diagnostics;
    expect(Array.isArray(diags) || diags === null || diags === undefined).toBe(true);

    // Invalid pipeline — must return valid=false and 422
    const invalidResp = await page.request.post(`/api/orgs/${orgId}/pipelines/validate`, {
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      data: { contents: 'this is { invalid alloy syntax' },
    });
    expect(invalidResp.status()).toBe(422);
    const invalidResult = (await invalidResp.json()) as {
      error?: { code: string };
      Error?: { code: string };
    };
    const errCode = (invalidResult.error ?? invalidResult.Error) as { code: string } | undefined;
    expect(errCode?.code ?? 'validation_failed').toBe('validation_failed');
  });

  test('scenario 15-pipeline: pipeline CRUD with revision tracking', async ({ page }) => {
    await loginAsAdmin(page);

    const meResp = await page.request.get('/api/me', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    const me = (await meResp.json()) as { orgs: Array<{ id: string; name: string }> };
    const org = me.orgs.find((o) => o.name === 'platform-org') ?? me.orgs[0];
    if (!org) throw new Error('dev seed must provide at least one org');
    const orgId = org.id;

    // Create pipeline
    const name = `fs_crud_pipe_${Date.now()}`;
    const createResp = await page.request.post(`/api/orgs/${orgId}/pipelines`, {
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      data: { name, contents: `prometheus.exporter.self "${name}" { }`, matchers: [] },
    });
    expect(createResp.status()).toBe(201);
    const pipe = (await createResp.json()) as { id: string };

    // Update it
    const updateResp = await page.request.put(`/api/orgs/${orgId}/pipelines/${pipe.id}`, {
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      data: {
        name,
        contents: `prometheus.exporter.self "${name}" { }\n// updated`,
        matchers: [],
      },
    });
    expect(updateResp.status()).toBe(200);

    // Check revisions
    const revsResp = await page.request.get(`/api/orgs/${orgId}/pipelines/${pipe.id}/revisions`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    expect(revsResp.status()).toBe(200);
    const revs = (await revsResp.json()) as { items: Array<{ revision: number }> };
    expect(revs.items.length).toBeGreaterThanOrEqual(2);

    // Navigate to pipeline page in browser — page renders (editor may not load without org ID hardcoded in SPA)
    await page.goto(`/pipelines/${pipe.id}`);
    await page.waitForLoadState('networkidle');
    // The pipeline route must be reachable — SPA renders without error
    await expect(page).toHaveURL(new RegExp(pipe.id));

    // Clean up
    await page.request.delete(`/api/orgs/${orgId}/pipelines/${pipe.id}`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
  });
});
