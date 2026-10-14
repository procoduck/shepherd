// visual-graph-view-modules.spec.ts — the read-only graph view draws each Alloy
// `declare` module as a group of its components (mocked suite).
//
// The GraphView response is the server's real parse of the alloy-metrics
// config grafana/k8s-monitoring 4.4.0 renders (internal/visual/testdata/
// k8s-monitoring/metrics.alloy), modules expanded:
// internal/visual/parse_modules_test.go fails when the fixture drifts from the
// parser. Four modules hold 30 of its 33 components, and every one of them
// reaches the shared remote_write only through `argument.metrics_destinations`
// and the module's instance.
import { readFileSync } from 'node:fs';
import { expect, type Page } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { reader } from '../fixtures/personas';
import { shippedSchema } from '../fixtures/schema-fixture';
import { type ApiFixture, test } from '../fixtures/test';

interface FixtureNode {
  id: string;
  component: string;
  label: string;
  parent?: string;
}
const metrics = JSON.parse(
  readFileSync(
    new URL('../fixtures/k8s-monitoring-metrics-graphview.json', import.meta.url),
    'utf8',
  ),
) as {
  graph: { nodes: FixtureNode[]; edges: unknown[]; bindings: unknown[] };
  opaque: boolean;
  warning: string;
};
const groups = metrics.graph.nodes.filter((n) => n.component === 'declare');
const leaves = metrics.graph.nodes.filter((n) => n.component !== 'declare');
const group = (label: string) => {
  const g = groups.find((n) => n.label === label);
  if (!g) throw new Error(`fixture has no module ${label}`);
  return g;
};
const childrenOf = (id: string) => metrics.graph.nodes.filter((n) => n.parent === id);

async function open(page: Page, api: ApiFixture) {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    schema: shippedSchema,
    pipelines: [s.pipelines[2]],
    graphViewResult: metrics,
  });
  await page.goto(`/pipelines/${s.pipelines[2].id}/graph`);
  await expect(page.getByTestId('graph-view')).toBeVisible();
}

type Box = { x: number; y: number; width: number; height: number };
/** Every rendered node's on-screen box, by node id. */
async function boxes(page: Page): Promise<Map<string, Box>> {
  const entries = await page.locator('.react-flow__node').evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect();
      return [
        el.getAttribute('data-id') ?? '',
        { x: r.x, y: r.y, width: r.width, height: r.height },
      ] as const;
    }),
  );
  return new Map(entries);
}
const overlaps = (a: Box, b: Box) =>
  a.x < b.x + b.width - 0.5 &&
  b.x < a.x + a.width - 0.5 &&
  a.y < b.y + b.height - 0.5 &&
  b.y < a.y + a.height - 0.5;

test.describe('graph view of a config built from modules (k8s-monitoring metrics)', () => {
  test.beforeEach(async ({ page, api }) => {
    await open(page, api);
    await expect(page.getByTestId('graph-view-canvas')).toHaveAttribute('data-layout', 'elk');
  });

  test('draws each module as a group holding its components', async ({ page }) => {
    await expect(page.getByTestId('module-group')).toHaveCount(4);
    await expect(page.getByTestId('pipeline-node')).toHaveCount(leaves.length);
    const cluster = group('cluster_metrics');
    const box = page.locator(`[data-testid="module-group"][data-node-id="${cluster.id}"]`);
    await expect(box).toHaveAttribute('data-collapsed', 'false');
    await expect(box.getByTestId('module-group-count')).toHaveText('15 components');

    // Every component sits inside its group's box on screen.
    const all = await boxes(page);
    for (const g of groups) {
      const outer = all.get(g.id) as Box;
      for (const c of childrenOf(g.id)) {
        const inner = all.get(c.id) as Box;
        expect(inner, c.id).toBeDefined();
        expect(inner.x >= outer.x - 0.5 && inner.y >= outer.y - 0.5, c.id).toBe(true);
        expect(inner.x + inner.width <= outer.x + outer.width + 0.5, c.id).toBe(true);
        expect(inner.y + inner.height <= outer.y + outer.height + 0.5, c.id).toBe(true);
      }
    }
  });

  test('lays the graph out with no two nodes on the same level overlapping', async ({ page }) => {
    const all = await boxes(page);
    expect(all.size).toBe(metrics.graph.nodes.length);
    const parentOf = new Map(metrics.graph.nodes.map((n) => [n.id, n.parent ?? '']));
    const ids = [...all.keys()];
    for (let i = 0; i < ids.length; i++) {
      for (let j = i + 1; j < ids.length; j++) {
        if (parentOf.get(ids[i]) !== parentOf.get(ids[j])) continue;
        expect(
          overlaps(all.get(ids[i]) as Box, all.get(ids[j]) as Box),
          `${ids[i]} overlaps ${ids[j]}`,
        ).toBe(false);
      }
    }
  });

  test('traces a module component across the module boundary to the destination', async ({
    page,
  }) => {
    await expect(page.locator('.react-flow__edge')).toHaveCount(metrics.graph.edges.length);
    // Inside the module: the kubelet relabel hands its data to the module's
    // metrics_destinations argument …
    await expect(page.locator('[aria-label="Edge from kubelet to cluster_metrics"]')).toHaveCount(
      1,
    );
    // … which the module's instance sets …
    await expect(page.locator('[aria-label="Edge from cluster_metrics to feature"]')).toHaveCount(
      1,
    );
    // … to the remote_write's receiver (one edge per instance).
    await expect(page.locator('[aria-label="Edge from feature to prometheus"]')).toHaveCount(4);
    const cluster = group('cluster_metrics');
    const port = page
      .locator(`.react-flow__node[data-id="${cluster.id}"]`)
      .locator('.react-flow__handle-right');
    await expect(port).toHaveCount(1);
  });

  test('collapses and expands a module', async ({ page }) => {
    const cluster = group('cluster_metrics');
    const box = page.locator(`[data-testid="module-group"][data-node-id="${cluster.id}"]`);
    // By keyboard: at the suite's viewport the minimap can sit over the
    // toggle of a group in the canvas's lower left.
    await box.getByRole('button', { name: 'Collapse module cluster_metrics' }).press('Enter');
    await expect(box).toHaveAttribute('data-collapsed', 'true');
    await expect(page.getByTestId('pipeline-node')).toHaveCount(leaves.length - 15);
    for (const c of childrenOf(cluster.id).slice(0, 3)) {
      await expect(page.locator(`.react-flow__node[data-id="${c.id}"]`)).toHaveCount(0);
    }
    // Still wired to its instance, through the port on its collapsed box.
    await expect(page.locator('[aria-label="Edge from cluster_metrics to feature"]')).toHaveCount(
      1,
    );
    await expect(page.locator('[aria-label="Edge from kubelet to cluster_metrics"]')).toHaveCount(
      0,
    );
    await expect(box.getByTestId('module-group-port')).toHaveText('argument.metrics_destinations');
    // The rest of the graph is laid out again around the smaller box.
    const all = await boxes(page);
    const parentOf = new Map(metrics.graph.nodes.map((n) => [n.id, n.parent ?? '']));
    for (const [id, b] of all) {
      if (id === cluster.id || parentOf.get(id) !== '') continue;
      expect(overlaps(b, all.get(cluster.id) as Box), id).toBe(false);
    }

    await box.getByRole('button', { name: 'Expand module cluster_metrics' }).press('Enter');
    await expect(box).toHaveAttribute('data-collapsed', 'false');
    await expect(page.getByTestId('pipeline-node')).toHaveCount(leaves.length);
  });

  test("opens a module's source in the inspector", async ({ page }) => {
    const cluster = group('cluster_metrics');
    await page
      .locator(`[data-testid="module-group"][data-node-id="${cluster.id}"]`)
      .getByTestId('module-group-count')
      .click();
    const inspector = page.getByRole('complementary', { name: 'Node details' });
    await expect(inspector.getByTestId('inspector-raw')).toContainText(
      'declare "cluster_metrics" {',
    );
  });

  test('keeps Recreate unavailable: modules cannot be carried over', async ({ page }) => {
    const recreate = page.getByRole('button', { name: 'Recreate as visual pipeline' });
    await expect(recreate).toHaveAttribute('aria-disabled', 'true');
    await expect(recreate).toHaveAccessibleDescription(/8 blocks can.t be carried over/);
  });
});

test('loads the layout engine on the graph route only', async ({ page, api }) => {
  const elk: string[] = [];
  page.on('request', (r) => {
    if (/elk/i.test(r.url())) elk.push(r.url());
  });
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    schema: shippedSchema,
    pipelines: [s.pipelines[2]],
    graphViewResult: metrics,
  });
  await page.goto('/pipelines');
  await expect(page.getByTestId('pipeline-row-git-pipe')).toBeVisible();
  await page.goto(`/pipelines/${s.pipelines[2].id}`);
  await expect(page.getByRole('link', { name: 'View as graph' })).toBeVisible();
  expect(elk).toEqual([]);

  await page.getByRole('link', { name: 'View as graph' }).click();
  await expect(page.getByTestId('graph-view-canvas')).toHaveAttribute('data-layout', 'elk');
  expect(elk.some((u) => /elk-api/.test(u))).toBe(true);
  expect(elk.some((u) => /elk-worker/.test(u))).toBe(true);
});

test('falls back to the server grid when the layout engine cannot load', async ({ page, api }) => {
  // The chunk loads but will not evaluate (an aborted request would also log
  // a resource error, which the suite fails on).
  await page.route(/elk-api-[^/]*\.js$/, (route) =>
    route.fulfill({
      contentType: 'text/javascript',
      body: "throw new Error('layout engine blocked by the test');",
    }),
  );
  await open(page, api);
  await expect(page.getByTestId('graph-view-canvas')).toHaveAttribute('data-layout', 'grid');
  await expect(page.getByTestId('graph-view-layout-fallback')).toBeVisible();
  // Nothing is lost: every node and edge is still drawn, groups as boxes of
  // their own with nothing to expand.
  await expect(page.getByTestId('pipeline-node')).toHaveCount(leaves.length);
  await expect(page.getByTestId('module-group')).toHaveCount(4);
  await expect(page.getByTestId('module-group-toggle')).toHaveCount(0);
  await expect(page.locator('.react-flow__edge')).toHaveCount(metrics.graph.edges.length);
});
