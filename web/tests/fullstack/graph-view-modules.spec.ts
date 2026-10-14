/**
 * Fullstack: the read-only graph view of a pipeline built from a `declare`
 * module, against the real backend — VisualService.GraphView's module
 * expansion (internal/visual/parse.go, ParseOptions.ExpandModules) and the
 * elkjs layout worker served from the Go binary's embedded SPA
 * (web/src/visual/elkLayout.ts). The mocked suite proves the UI against a
 * recorded GraphView response; this proves the deployed pair: the server
 * really sends `parent`, and the worker script really loads from go:embed.
 */
import { expect, test } from '@playwright/test';
import { getMe, loginAsAdmin, rpc } from './fixtures';

test('a pipeline with a module opens as a laid-out group traced to its destination', async ({
  page,
}) => {
  await loginAsAdmin(page);
  const me = await getMe(page);
  const org = me.orgs.find((o) => o.name === 'platform-org');
  if (!org) throw new Error('dev seed must provide platform-org');

  const name = `fs_graph_modules_${Date.now()}`;
  const created = await rpc(page, 'PipelineService', 'CreatePipeline', {
    orgId: org.id,
    name,
    contents: `declare "feature" {
  argument "dest" { }

  prometheus.exporter.unix "host" { }

  prometheus.scrape "host" {
    targets    = prometheus.exporter.unix.host.targets
    forward_to = argument.dest.value
  }
}

feature "x" {
  dest = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "http://example.com/api/v1/push"
  }
}
`,
    matchers: ['role="fullstack-graph-view-modules"'],
  });
  expect(created.status(), await created.text()).toBe(200);
  const pipeline = (await created.json()) as { id: string };

  const worker = page.waitForRequest(/elk-worker[^/]*\.js$/);
  await page.goto(`/pipelines/${pipeline.id}/graph`);
  await expect(page.getByTestId('graph-view-canvas')).toHaveAttribute('data-layout', 'elk', {
    timeout: 20_000,
  });
  expect((await (await worker).response())?.status()).toBe(200);

  const group = page.getByTestId('module-group');
  await expect(group).toHaveCount(1);
  await expect(group.getByTestId('module-group-count')).toHaveText('2 components');
  // Each end named by its kind and label: the module's component, the module,
  // its instance, the destination.
  const edge = (from: string, to: string) =>
    page.locator(`[aria-label="Edge from ${from} to ${to}"]`);
  await expect(edge('prometheus.scrape host', 'module feature')).toHaveCount(1);
  await expect(edge('module feature', 'feature x')).toHaveCount(1);
  await expect(edge('feature x', 'prometheus.remote_write sink')).toHaveCount(1);

  await rpc(page, 'PipelineService', 'DeletePipeline', { orgId: org.id, id: pipeline.id });
});
