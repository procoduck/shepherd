/**
 * Fullstack: self-monitoring wizard -> real pipeline -> enabled -> served
 * config contains its block (W7-10, via the API).
 *
 * internal/wizard/selfmonitoring: with logs off its Auto role is "metrics"
 * (B6, 2026-10-09 walkthrough), so this test picks role "singleton"
 * explicitly to target the seeded "singleton" collector on prod-eu-1
 * without needing a cluster_pattern.
 */
import { expect, forceRecompute, getMe, loginAsAdmin, rpc, test } from './fixtures';

test.describe('wizard-commit', () => {
  test('committing the self-monitoring wizard produces a pipeline whose served config carries its block', async ({
    page,
  }) => {
    await loginAsAdmin(page);

    const me = await getMe(page);
    const org = me.orgs.find((o) => o.name === 'platform-org');
    if (!org) throw new Error('dev seed must provide platform-org');
    const orgId = org.id;

    const name = `fs-selfmon-${Date.now()}`;
    const commitResp = await rpc(page, 'WizardService', 'CommitWizard', {
      orgId,
      kind: 'self-monitoring',
      name,
      // `state` is a google.protobuf.Struct, so its keys go over the wire
      // verbatim (snake_case, as the wizard package reads them).
      state: {
        job_name: 'alloy-self',
        scrape_interval: '60s',
        metrics_dest_name: 'prom-prod',
        // Logs deliberately left off (logs_dest_name/log_path empty):
        // Commit() only emits the loki.* blocks when both are set, and
        // this test only needs the pipeline to exist and match, not the
        // mixed-signal detail the wizard package itself already covers.
        logs_enabled: false,
        role: 'singleton',
      },
    });
    expect(commitResp.status()).toBe(200);
    const pipeline = (await commitResp.json()) as { id: string; matchers: string[] };
    expect(pipeline.matchers).toContain('role="singleton"');

    const enableResp = await rpc(page, 'PipelineService', 'EnablePipeline', {
      orgId,
      id: pipeline.id,
    });
    expect(enableResp.status()).toBe(200);

    const collectorsResp = await rpc(page, 'FleetService', 'ListCollectors', { orgId });
    const collectors = (await collectorsResp.json()) as {
      items?: Array<{ id: string; role: string; cluster: string }>;
    };
    const singletonCollector = (collectors.items ?? []).find(
      (c) => c.role === 'singleton' && c.cluster === 'prod-eu-1',
    );
    if (!singletonCollector) {
      throw new Error('dev seed must provide the prod-eu-1/singleton collector');
    }

    // Nothing real polls the singleton collector (dev-guide: it has no
    // compose container backing it), so a lazy recompute needs a nudge here
    // — same ruling pipelines.spec.ts's scenario 6 already relies on.
    await forceRecompute(page, 'prod-eu-1', 'singleton');

    const blockName = `pipe_${name.replace(/-/g, '_')}`;
    await expect
      .poll(
        async () => {
          const resp = await rpc(page, 'FleetService', 'GetServedConfig', {
            orgId,
            id: singletonCollector.id,
          });
          // protojson omits an empty content (a cache miss) — '' is that.
          const data = (await resp.json()) as { content?: string };
          return data.content ?? '';
        },
        { timeout: 15000, intervals: [1000] },
      )
      .toContain(`declare "${blockName}"`);

    await rpc(page, 'PipelineService', 'DeletePipeline', { orgId, id: pipeline.id });
  });
});
