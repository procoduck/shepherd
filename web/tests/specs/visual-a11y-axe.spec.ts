/**
 * #426: axe over the visual builder with a non-trivial graph — six nodes over
 * metrics and logs, fan-in, a disabled node, a config node and a nested
 * doc.bindings[] entry — in both themes, with a node, a port and the shortcut
 * sheet open. Fails on any serious or critical violation; minor and moderate
 * findings are printed so they stay visible without blocking.
 *
 * No rule is disabled. The scan is scoped to the builder (`visual-builder`),
 * not the app shell around it, which has its own specs.
 */
import AxeBuilder from '@axe-core/playwright';
import { expect, type Page } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { waitForViewportSettled } from '../fixtures/canvas';
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const node = (
  id: string,
  component: string,
  label: string,
  x: number,
  y: number,
  props: Record<string, unknown> = {},
  disabled = false,
) => ({ id, component, label, position: { x, y }, props, disabled, notes: '' });

const graph = {
  kind: 'alloy-graph/v1',
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    node('n1', 'discovery.kubernetes', 'pods', 0, 0, { role: 'pod' }),
    node('n2', 'discovery.relabel', 'keep', 320, 0),
    node('n3', 'prometheus.scrape', 'app', 640, 0, { job_name: 'app' }),
    node('n4', 'prometheus.remote_write', 'sink', 960, 0, {
      endpoint: [{ url: 'https://prom.example.com/api/v1/write', basic_auth: { username: 'u' } }],
    }),
    node('n5', 'remote.kubernetes.secret', 'creds', 960, 260, { namespace: 'mon', name: 'c' }),
    node('n6', 'loki.source.file', 'files', 0, 260),
    node('n7', 'loki.process', 'proc', 320, 260, {}, true),
    node('n8', 'loki.write', 'logs', 640, 260, {
      endpoint: [{ url: 'https://loki.example.com/loki/api/v1/push' }],
    }),
  ],
  edges: [
    { id: 'e1', from: { node: 'n1', port: 'targets' }, to: { node: 'n2', port: 'targets' } },
    { id: 'e2', from: { node: 'n2', port: 'output' }, to: { node: 'n3', port: 'targets' } },
    { id: 'e3', from: { node: 'n1', port: 'targets' }, to: { node: 'n3', port: 'targets' } },
    { id: 'e4', from: { node: 'n3', port: 'forward_to' }, to: { node: 'n4', port: 'receiver' } },
    { id: 'e5', from: { node: 'n6', port: 'forward_to' }, to: { node: 'n8', port: 'receiver' } },
  ],
  bindings: [
    {
      node: 'n4',
      prop: 'endpoint[0].basic_auth.password',
      ref: { node: 'n5', export: 'data', expr: 'remote.kubernetes.secret.creds.data["password"]' },
    },
  ],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-visual-builder' },
};

async function expectNoSeriousViolations(page: Page, label: string) {
  const results = await new AxeBuilder({ page })
    .include('[data-testid="visual-builder"]')
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'])
    .analyze();
  const blocking = results.violations.filter(
    (v) => v.impact === 'serious' || v.impact === 'critical',
  );
  const other = results.violations.filter((v) => !blocking.includes(v));
  if (other.length > 0)
    console.log(
      `[axe ${label}] non-blocking:`,
      other.map((v) => `${v.id} (${v.impact})`).join(', '),
    );
  expect(
    blocking.map((v) => ({
      rule: v.id,
      impact: v.impact,
      help: v.help,
      targets: v.nodes.slice(0, 8).map((n) => `${n.target.join(' ')} — ${n.any[0]?.message ?? ''}`),
    })),
    `axe ${label}`,
  ).toEqual([]);
}

test.describe('visual builder accessibility (axe, #426)', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [
        { ...pipeline({ id: 'pip-axe', name: 'axe', source: 'visual' }), wizard_state: graph },
      ],
    });
    await page.goto('/pipelines/pip-axe/visual');
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('.react-flow__node')).toHaveCount(graph.nodes.length);
    await expect(page.locator('.react-flow__edge')).toHaveCount(graph.edges.length);
    await waitForViewportSettled(page);
  });

  for (const theme of ['dark', 'light'] as const) {
    test(`has no serious or critical violations (${theme})`, async ({ page }) => {
      await page.evaluate((t) => {
        document.documentElement.classList.remove('dark', 'light');
        document.documentElement.classList.add(t);
      }, theme);
      await expectNoSeriousViolations(page, `${theme}, at rest`);

      // A node focused and selected (the inspector open on it, the nested
      // binding shown), then a port focused.
      await page.getByTestId('canvas').focus();
      await page.keyboard.press('End');
      await page.locator('.react-flow__node[data-id="n4"]').focus();
      await page.keyboard.press(' ');
      await expect(page.getByTestId('inspector-component')).toHaveText('prometheus.remote_write');
      await expectNoSeriousViolations(page, `${theme}, node selected`);
      await page.keyboard.press('Enter');
      await expectNoSeriousViolations(page, `${theme}, port focused`);
    });
  }

  test('the shortcut sheet has no serious or critical violations', async ({ page }) => {
    await page.getByTestId('canvas').focus();
    await page.keyboard.press('?');
    await expect(page.getByRole('dialog', { name: 'Canvas keyboard shortcuts' })).toBeVisible();
    const results = await new AxeBuilder({ page })
      .include('[data-testid="canvas-shortcuts"]')
      .analyze();
    expect(
      results.violations
        .filter((v) => v.impact === 'serious' || v.impact === 'critical')
        .map((v) => v.id),
    ).toEqual([]);
  });
});
