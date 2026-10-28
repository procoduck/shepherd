// visual-bindings.spec.ts — W5-02
// SecretField's binding picker: wiring an existing remote.kubernetes.secret
// node into prometheus.remote_write's endpoint.basic_auth.password, and the
// unified "Bound" display covering the new props-based binding channel.
import { expect, type Page } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { waitForViewportSettled } from '../fixtures/canvas';
import { basicScenario, pipeline } from '../fixtures/factories';
import { appAdmin, orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

test.describe('visual bindings', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  });

  test('binds an existing secret-source node into a nested secret field, and the code tab shows the reference', async ({
    page,
  }) => {
    // Place the secret source and the destination.
    await page.click('[data-component="remote.kubernetes.secret"]');
    await page.click('[data-component="prometheus.remote_write"]');
    await expect(page.locator('[data-testid="pipeline-node"]')).toHaveCount(2);

    // Select the remote_write node (it was placed second).
    await page.locator('.react-flow__controls-fitview').click();
    await waitForViewportSettled(page);
    const remoteWriteId = await page
      .locator('[data-testid="pipeline-node"]')
      .last()
      .getAttribute('data-node-id');
    await page.click(`[data-node-id="${remoteWriteId}"]`, { force: true });
    await expect(page.locator('[data-testid="inspector"]')).toContainText(
      'prometheus.remote_write',
      {
        timeout: 5_000,
      },
    );

    // Open an endpoint instance, then its basic_auth block, to reach `password`.
    await page.click('[data-testid="block-add-endpoint"]');
    await page.click('[data-testid="block-add-endpoint.basic_auth"]');

    const picker = page.locator('[data-testid="attr-binding-source-password"]');
    await expect(picker).toBeVisible({ timeout: 5_000 });

    await picker.selectOption({ label: 'secret (remote.kubernetes.secret)' });
    await page.fill('[data-testid="attr-binding-key-password"]', 'password');
    await page.click('[data-testid="attr-binding-bind-password"]');

    await expect(page.locator('[data-testid="attr-secret-password"]')).toContainText(
      'remote.kubernetes.secret.secret.data["password"]',
    );

    await page.click('[data-testid="drawer-toggle"]');
    await page.click('[data-testid="drawer-tab-code"]');
    await expect(page.locator('[data-testid="code-tab-content"]')).toContainText(
      'remote.kubernetes.secret.secret.data["password"]',
      { timeout: 5_000 },
    );
  });

  test('unbind clears the field back to the picker', async ({ page }) => {
    await page.click('[data-component="remote.kubernetes.secret"]');
    await page.click('[data-component="prometheus.remote_write"]');
    await page.locator('.react-flow__controls-fitview').click();
    await waitForViewportSettled(page);
    const remoteWriteId = await page
      .locator('[data-testid="pipeline-node"]')
      .last()
      .getAttribute('data-node-id');
    await page.click(`[data-node-id="${remoteWriteId}"]`, { force: true });
    await page.click('[data-testid="block-add-endpoint"]');
    await page.click('[data-testid="block-add-endpoint.basic_auth"]');

    await page
      .locator('[data-testid="attr-binding-source-password"]')
      .selectOption({ label: 'secret (remote.kubernetes.secret)' });
    await page.fill('[data-testid="attr-binding-key-password"]', 'password');
    await page.click('[data-testid="attr-binding-bind-password"]');
    await expect(page.locator('[data-testid="attr-secret-password"]')).toContainText('Bound');

    await page.click('[data-testid="attr-binding-unbind-password"]');

    await expect(page.locator('[data-testid="attr-binding-source-password"]')).toBeVisible();
    await expect(page.locator('[data-testid="attr-secret-password"]')).not.toContainText('Bound');
  });
});

// #426: doc.bindings[] entries at nested paths — a repeated block's second
// instance (endpoint[1].basic_auth.password) and a dotted block name
// (stage.tenant[0].value) — load, show on the right field, render inside
// their block, and unbind.
test.describe('nested doc.bindings[] entries (#426)', () => {
  const node = (
    id: string,
    component: string,
    label: string,
    x: number,
    props: Record<string, unknown>,
  ) => ({ id, component, label, position: { x, y: 0 }, props, disabled: false, notes: '' });
  const graph = {
    kind: 'alloy-graph/v1',
    schema_version: currentSchemaVersion(schemaFixture),
    nodes: [
      node('s1', 'remote.kubernetes.secret', 'creds', 0, { namespace: 'mon', name: 'c' }),
      node('w1', 'prometheus.remote_write', 'sink', 320, {
        endpoint: [
          { url: 'https://a.example.com/push' },
          { url: 'https://b.example.com/push', basic_auth: { username: 'u' } },
        ],
      }),
      node('p1', 'loki.process', 'tenant', 640, { 'stage.tenant': [{}] }),
    ],
    edges: [],
    bindings: [
      {
        node: 'w1',
        prop: 'endpoint[1].basic_auth.password',
        ref: { node: 's1', export: 'data', expr: 'remote.kubernetes.secret.creds.data["pw"]' },
      },
      {
        node: 'p1',
        prop: 'stage.tenant[0].value',
        ref: { node: 's1', export: 'data', expr: 'remote.kubernetes.secret.creds.data["tenant"]' },
      },
    ],
    viewport: { x: 0, y: 0, zoom: 1 },
    meta: { created_with: 'shepherd-visual-builder' },
  };

  const select = async (page: Page, id: string) => {
    await page.click(`[data-node-id="${id}"]`, { force: true });
  };

  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [
        { ...pipeline({ id: 'pip-nb', name: 'nb', source: 'visual' }), wizard_state: graph },
      ],
    });
    await page.goto('/pipelines/pip-nb/visual');
    await expect(page.locator('.react-flow__node')).toHaveCount(3, { timeout: 10_000 });
    await waitForViewportSettled(page);
  });

  test('shows each binding on its own instance and renders it inside the block', async ({
    page,
  }) => {
    await select(page, 'w1');
    // Only the second endpoint has a basic_auth block, and its password is
    // the bound one.
    const passwords = page.locator('[data-testid="attr-secret-password"]');
    await expect(passwords).toHaveCount(1);
    await expect(passwords).toContainText('Bound: remote.kubernetes.secret.creds.data["pw"]');
    // The Bindings list names it by its path.
    await expect(
      page.locator('[data-testid="node-binding-endpoint[1].basic_auth.password"]'),
    ).toContainText('remote.kubernetes.secret.creds.data["pw"]');

    await page.click('[data-testid="drawer-toggle"]');
    await page.click('[data-testid="drawer-tab-code"]');
    const code = page.locator('[data-testid="code-tab-content"]');
    await expect(code).toContainText('url = "https://b.example.com/push"');
    await expect(code).toContainText('password = remote.kubernetes.secret.creds.data["pw"]');
    await expect(code).toContainText('value = remote.kubernetes.secret.creds.data["tenant"]');
    await expect(code).not.toContainText('endpoint[1]');
    await expect(code).not.toContainText('stage.tenant[0]');
  });

  test('a bound non-secret attribute shows the binding and unbinds back to its input', async ({
    page,
  }) => {
    await select(page, 'p1');
    const bound = page.locator('[data-testid="attr-bound-value"]');
    await expect(bound).toContainText('Bound: remote.kubernetes.secret.creds.data["tenant"]');
    await page.click('[data-testid="attr-binding-unbind-value"]');
    await expect(bound).toHaveCount(0);
    await expect(page.locator('[data-testid="inspector-node-bindings"]')).toHaveCount(0);

    await page.click('[data-testid="drawer-toggle"]');
    await page.click('[data-testid="drawer-tab-code"]');
    await expect(page.locator('[data-testid="code-tab-content"]')).not.toContainText(
      'creds.data["tenant"]',
    );
  });
});
