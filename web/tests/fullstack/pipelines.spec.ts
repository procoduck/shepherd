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
import { expect, forceRecompute, getMe, loginAsAdmin, rpc, test } from './fixtures';

test.describe('pipelines', () => {
  test('scenario 6: enable pipeline → served config contains declare block', async ({ page }) => {
    await loginAsAdmin(page);

    // Get the platform org
    const me = await getMe(page);
    const platformOrg = me.orgs.find((o) => o.name === 'platform-org');
    // A missing seed is the bug, not a reason to stand down: skipping here
    // reported green while verifying nothing.
    if (!platformOrg) throw new Error('dev seed must provide platform-org');
    const orgId = platformOrg.id;

    // Create a fresh pipeline for this test
    const pipeName = `fs_pipe_${Date.now()}`;
    const createResp = await rpc(page, 'PipelineService', 'CreatePipeline', {
      orgId,
      name: pipeName,
      contents: `prometheus.exporter.self "${pipeName}" { }`,
      matchers: [`cluster="prod-eu-1"`, `role="metrics"`],
    });
    expect(createResp.status()).toBe(200);
    const pipeline = (await createResp.json()) as { id: string };

    // Enable the pipeline
    const enableResp = await rpc(page, 'PipelineService', 'EnablePipeline', {
      orgId,
      id: pipeline.id,
    });
    expect(enableResp.status()).toBe(200);

    // Get the metrics collector for this org
    const collectorsResp = await rpc(page, 'FleetService', 'ListCollectors', { orgId });
    const collectors = (await collectorsResp.json()) as {
      items?: Array<{ id: string; role: string }>;
    };
    const metricsCollector = (collectors.items ?? []).find((c) => c.role === 'metrics');
    if (!metricsCollector) {
      throw new Error('dev seed must provide a collector with role=metrics');
    }

    // Trigger recompute via Connect-JSON GetConfig (ruling 3)
    await forceRecompute(page, 'prod-eu-1', 'metrics');

    // Poll served-config until hash is non-empty or timeout
    await expect
      .poll(
        async () => {
          const resp = await rpc(page, 'FleetService', 'GetServedConfig', {
            orgId,
            id: metricsCollector.id,
          });
          // protojson omits an empty content (a cache miss) — '' is that.
          const data = (await resp.json()) as { content?: string; hash?: string };
          return data.content ?? '';
        },
        { timeout: 15000, intervals: [1000] },
      )
      .toContain(`declare "pipe_${pipeName.replace(/-/g, '_')}`);

    // Clean up
    await rpc(page, 'PipelineService', 'DeletePipeline', { orgId, id: pipeline.id });
  });

  test('scenario 7: pipeline validation returns valid shape', async ({ page }) => {
    await loginAsAdmin(page);

    const me = await getMe(page);
    if (!me.orgs.length) throw new Error('dev seed must provide at least one org');
    const orgId = me.orgs[0].id;

    // Valid pipeline — must return valid=true
    const validResp = await rpc(page, 'PipelineService', 'ValidatePipeline', {
      orgId,
      name: 'test',
      contents: 'prometheus.exporter.self "test" { }',
    });
    expect(validResp.status()).toBe(200);
    const validResult = (await validResp.json()) as { valid?: boolean; diagnostics?: unknown[] };
    expect(validResult.valid).toBe(true);
    // protojson omits an empty diagnostics list; when present it is an array.
    const diags = validResult.diagnostics;
    expect(Array.isArray(diags) || diags === undefined).toBe(true);

    // Invalid pipeline — must return valid=false. Over Connect a validation
    // verdict is a successful call (200) whose body says valid=false; the
    // REST shim's 422 was its own translation of that same verdict.
    // protojson omits valid=false, so absent means false — but the call must
    // still have succeeded and carried the diagnostics explaining why.
    const invalidResp = await rpc(page, 'PipelineService', 'ValidatePipeline', {
      orgId,
      contents: 'this is { invalid alloy syntax',
    });
    expect(invalidResp.status()).toBe(200);
    const invalidResult = (await invalidResp.json()) as {
      valid?: boolean;
      diagnostics?: unknown[];
    };
    expect(invalidResult.valid ?? false).toBe(false);
    expect(invalidResult.diagnostics?.length ?? 0).toBeGreaterThan(0);
  });

  test('scenario 15-pipeline: pipeline CRUD with revision tracking', async ({ page }) => {
    await loginAsAdmin(page);

    const me = await getMe(page);
    const org = me.orgs.find((o) => o.name === 'platform-org') ?? me.orgs[0];
    if (!org) throw new Error('dev seed must provide at least one org');
    const orgId = org.id;

    // Create pipeline
    const name = `fs_crud_pipe_${Date.now()}`;
    const createResp = await rpc(page, 'PipelineService', 'CreatePipeline', {
      orgId,
      name,
      contents: `prometheus.exporter.self "${name}" { }`,
      matchers: [],
    });
    expect(createResp.status()).toBe(200);
    const pipe = (await createResp.json()) as { id: string };

    // Update it
    const updateResp = await rpc(page, 'PipelineService', 'UpdatePipeline', {
      orgId,
      id: pipe.id,
      name,
      contents: `prometheus.exporter.self "${name}" { }\n// updated`,
      matchers: [],
    });
    expect(updateResp.status()).toBe(200);

    // Check revisions
    const revsResp = await rpc(page, 'PipelineService', 'ListRevisions', { orgId, id: pipe.id });
    expect(revsResp.status()).toBe(200);
    const revs = (await revsResp.json()) as { items?: Array<{ revision: number }> };
    expect(revs.items?.length ?? 0).toBeGreaterThanOrEqual(2);

    // Navigate to pipeline page in browser — page renders (editor may not load without org ID hardcoded in SPA)
    await page.goto(`/pipelines/${pipe.id}`);
    await page.waitForLoadState('networkidle');
    // The pipeline route must be reachable — SPA renders without error
    await expect(page).toHaveURL(new RegExp(pipe.id));

    // Clean up
    await rpc(page, 'PipelineService', 'DeletePipeline', { orgId, id: pipe.id });
  });
});
