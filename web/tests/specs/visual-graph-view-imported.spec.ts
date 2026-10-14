// visual-graph-view-imported.spec.ts — the read-only graph view of a pipeline
// that was NOT built in the visual builder (mocked suite).
//
// The GraphView response is the server's real parse of a trimmed
// grafana/k8s-monitoring 4.4.0 config (internal/visual/testdata/k8s-monitoring/
// singleton.alloy), modules expanded as GraphView parses them;
// internal/visual/parse_modules_test.go fails when the fixture drifts from the
// parser. It holds one `declare` module drawn as a group of its two components,
// the module's instance (an opaque block), ten edges — three of them carrying
// the module's data out through its argument and the instance to loki.write —
// and four secret references reported as bindings.
import { readFileSync } from 'node:fs';
import { expect } from '@playwright/test';
import { basicScenario, pipeline } from '../fixtures/factories';
import { appAdmin, reader } from '../fixtures/personas';
import { schemaFixture, shippedSchema } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const k8sMonitoring = JSON.parse(
  readFileSync(new URL('../fixtures/k8s-monitoring-graphview.json', import.meta.url), 'utf8'),
) as {
  graph: {
    nodes: { id: string; component: string; parent?: string }[];
    edges: unknown[];
    bindings: unknown[];
  };
  opaque: boolean;
  warning: string;
};

const nodeByComponent = (component: string) => {
  const n = k8sMonitoring.graph.nodes.find((x) => x.component === component);
  if (!n) throw new Error(`fixture has no ${component} node`);
  return n.id;
};

test.describe('graph view is reachable for a non-visual pipeline', () => {
  test('the pipelines list links a text pipeline to its graph and a visual one to the builder', async ({
    page,
    api,
  }) => {
    await api.loginAs(reader);
    const s = basicScenario();
    const visual = pipeline({ id: 'pip-0009', name: 'drawn', source: 'visual' });
    api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [...s.pipelines, visual] });
    await page.goto('/pipelines');

    const gitRow = page.getByTestId('pipeline-row-git-pipe');
    const graph = gitRow.getByRole('link', { name: 'View git-pipe as graph' });
    await expect(graph).toBeVisible();
    await expect(graph).toHaveAttribute('href', '/pipelines/pip-0003/graph');
    await expect(
      page.getByTestId('pipeline-row-drawn').getByRole('link', { name: /as graph/ }),
    ).toHaveCount(0);
    await expect(
      page.getByTestId('pipeline-row-drawn').getByRole('link', { name: /visual/ }),
    ).toBeVisible();

    await graph.click();
    await expect(page).toHaveURL(/\/pipelines\/pip-0003\/graph$/);
    await expect(page.getByTestId('graph-view')).toBeVisible();
  });

  test('the editor offers "View as graph" for a text pipeline, the builder for a visual one', async ({
    page,
    api,
  }) => {
    await api.loginAs(reader);
    const s = basicScenario();
    const visual = pipeline({ id: 'pip-0009', name: 'drawn', source: 'visual' });
    api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [...s.pipelines, visual] });

    await page.goto('/pipelines/pip-0003');
    const view = page.getByRole('link', { name: 'View as graph' });
    await expect(view).toBeVisible();
    await expect(page.getByTestId('editor-open-visual')).toHaveCount(0);
    await view.click();
    await expect(page).toHaveURL(/\/pipelines\/pip-0003\/graph$/);

    await page.goto('/pipelines/pip-0009');
    await expect(page.getByTestId('editor-open-visual')).toBeVisible();
    await expect(page.getByRole('link', { name: 'View as graph' })).toHaveCount(0);
  });
});

test.describe('graph view of a chart-generated pipeline (k8s-monitoring)', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: shippedSchema,
      pipelines: [s.pipelines[2]],
      graphViewResult: k8sMonitoring,
    });
    await page.goto(`/pipelines/${s.pipelines[2].id}/graph`);
    await expect(page.getByTestId('graph-view')).toBeVisible();
    await expect(page.getByTestId('pipeline-node')).toHaveCount(13);
    await expect(page.getByTestId('module-group')).toHaveCount(1);
  });

  test('shows the server warning text, not only a badge', async ({ page }) => {
    const warning = page.getByTestId('graph-view-warning');
    await expect(warning).toBeVisible();
    await expect(warning).toContainText('"cluster_events" is not a component');
    await expect(warning).toContainText('module "cluster_events" is shown as a group');
  });

  test('draws every edge, including the one from the unmapped block', async ({ page }) => {
    await expect(page.locator('.react-flow__edge')).toHaveCount(k8sMonitoring.graph.edges.length);
    const opaque = page.locator(
      `[data-testid="pipeline-node"][data-node-id="${nodeByComponent('opaque.cluster_events')}"]`,
    );
    // In from the module's group on its left, out to loki.write on its right.
    await expect(opaque.locator('.react-flow__handle')).toHaveCount(2);
    await expect(page.locator('[aria-label="Edge from feature to loki"]')).toHaveCount(1);
    await expect(page.locator('[aria-label="Edge from cluster_events to feature"]')).toHaveCount(1);
  });

  test("labels unmapped blocks and shows a module's source text", async ({ page }) => {
    const opaque = page.locator(
      `[data-testid="pipeline-node"][data-node-id="${nodeByComponent('opaque.cluster_events')}"]`,
    );
    await expect(opaque.getByTestId('node-opaque-badge')).toHaveText('not mapped');
    await expect(page.getByTestId('node-opaque-badge')).toHaveCount(1);

    await page
      .locator(`[data-testid="module-group"][data-node-id="${nodeByComponent('declare')}"]`)
      .getByTestId('module-group-count')
      .click();
    const inspector = page.getByRole('complementary', { name: 'Node details' });
    await expect(inspector).toBeVisible();
    const raw = inspector.getByTestId('inspector-raw');
    await expect(raw).toContainText('declare "cluster_events" {');
    await expect(raw).toContainText('loki.source.kubernetes_events "cluster_events"');
    await expect(inspector).not.toContainText('$raw');
  });

  test('renders nested props as formatted JSON, never [object Object]', async ({ page }) => {
    await page
      .locator(`[data-testid="pipeline-node"][data-node-id="${nodeByComponent('loki.write')}"]`)
      .click();
    const inspector = page.getByRole('complementary', { name: 'Node details' });
    await expect(inspector).toBeVisible();
    await expect(inspector).not.toContainText('[object Object]');
    const endpoint = inspector.getByTestId('inspector-prop-endpoint');
    await expect(endpoint).toContainText('"url": "https://loki.example.com/loki/api/v1/push"');
    await expect(endpoint).toContainText('"basic_auth": {');
    // The secret references, by the binding the server reported.
    const refs = inspector.getByTestId('inspector-bindings');
    await expect(refs).toContainText('endpoint.basic_auth.password');
    await expect(refs).toContainText('remote.kubernetes.secret "loki"');
  });

  test('Recreate is unavailable, with the reason, while blocks cannot be carried over', async ({
    page,
  }) => {
    const recreate = page.getByRole('button', { name: 'Recreate as visual pipeline' });
    await expect(recreate).toHaveAttribute('aria-disabled', 'true');
    await expect(recreate).toHaveAccessibleDescription(
      /2 blocks can.t be carried over to the visual builder/,
    );
    await recreate.click({ force: true });
    await expect(page.getByTestId('recreate-confirm-dialog')).toHaveCount(0);
  });
});

test('Recreate stays available for a fully mapped graph and drops the view-only bindings', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const mapped = {
    ...k8sMonitoring.graph,
    nodes: k8sMonitoring.graph.nodes.filter(
      (n) => !n.component.startsWith('opaque.') && n.component !== 'declare' && !n.parent,
    ),
    edges: (k8sMonitoring.graph.edges as { from: { node: string }; to: { node: string } }[]).filter(
      (e) =>
        [e.from.node, e.to.node].every((id) => /^n_(?!opaque_|declare_|cluster_events__)/.test(id)),
    ),
  };
  api.seed({
    orgs: [s.org],
    schema: shippedSchema,
    pipelines: [s.pipelines[2]],
    graphViewResult: { graph: mapped, opaque: false, warning: '' },
  });
  await page.goto(`/pipelines/${s.pipelines[2].id}/graph`);
  const recreate = page.getByRole('button', { name: 'Recreate as visual pipeline' });
  await expect(recreate).toBeVisible();
  await expect(recreate).not.toHaveAttribute('aria-disabled', 'true');
  await expect(page.getByTestId('graph-view-warning')).toHaveCount(0);
  // Capture the draft the page hands the builder (it is read and removed
  // again on arrival, and the navigation is client-side, so window survives).
  await page.evaluate(() => {
    const original = sessionStorage.setItem.bind(sessionStorage);
    sessionStorage.setItem = (key: string, value: string) => {
      if (key === 'vb:import-graph') (window as unknown as { imported: string }).imported = value;
      original(key, value);
    };
  });
  await recreate.click();
  await page.getByTestId('recreate-confirm-btn').click();
  await expect(page).toHaveURL(/\/pipelines\/visual\/new$/);
  await expect(page.getByTestId('pipeline-node')).toHaveCount(10);
  const imported = JSON.parse(
    await page.evaluate(() => (window as unknown as { imported: string }).imported),
  ) as { nodes: unknown[]; bindings: unknown[] };
  expect(imported.nodes).toHaveLength(10);
  // The server's bindings are a view of expressions the props already hold;
  // carried into the builder they would render twice.
  expect(k8sMonitoring.graph.bindings.length).toBeGreaterThan(0);
  expect(imported.bindings).toEqual([]);
});
