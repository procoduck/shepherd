import { expect } from '@playwright/test';
import { basicScenario, org } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

test.describe('visual simulate S2', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    // Bottom drawer defaults to collapsed; open it before selecting a tab.
    await page.getByTestId('drawer-toggle').click();
    await page.getByTestId('drawer-tab-simulate').click();
  });

  test('7.6.7.1 — simulate tab renders relabel sub-panel by default', async ({ page }) => {
    await expect(page.getByTestId('simulate-relabel-panel')).toBeVisible();
  });

  test('7.6.7.2 — relabel run with builtin fixtures renders step cards', async ({ page, api }) => {
    api.seed({
      simulateRelabelResult: {
        traces: [
          {
            input: { __meta_kubernetes_pod_name: 'api-server-abc123' },
            steps: [
              {
                rule_index: 0,
                action: 'replace',
                before: { __meta_kubernetes_pod_name: 'api-server-abc123' },
                after: { instance: 'api-server-abc123' },
                kept: true,
              },
            ],
            output: { instance: 'api-server-abc123' },
            kept: true,
          },
        ],
      },
    });
    await page.getByTestId('simulate-relabel-run').click();
    await expect(page.getByTestId('step-card-0').first()).toBeVisible();
  });

  test('7.6.7.3 — dropped target shows dropped-badge', async ({ page, api }) => {
    api.seed({
      simulateRelabelResult: {
        traces: [
          {
            input: { __meta_kubernetes_pod_name: 'worker-xyz789' },
            steps: [
              {
                rule_index: 0,
                action: 'keep',
                before: { __meta_kubernetes_pod_name: 'worker-xyz789' },
                kept: false,
              },
            ],
            kept: false,
          },
        ],
      },
    });
    await page.getByTestId('simulate-relabel-run').click();
    await expect(page.getByTestId('dropped-badge').first()).toBeVisible();
  });

  test('7.6.7.4 — logs sub-panel accessible via tab', async ({ page }) => {
    await page.getByTestId('simulate-logs-tab').click();
    await expect(page.getByTestId('simulate-logs-panel')).toBeVisible();
  });

  test('7.6.7.5 — log stage run renders stage cards', async ({ page, api }) => {
    api.seed({
      simulateLogsResult: {
        traces: [
          {
            input: '{"level":"info","msg":"ok"}',
            steps: [
              {
                stage_index: 0,
                stage_type: 'json',
                simulated: true,
                line_before: '{"level":"info","msg":"ok"}',
                line_after: '{"level":"info","msg":"ok"}',
                labels_before: {},
                labels_after: {},
              },
              {
                stage_index: 1,
                stage_type: 'labels',
                simulated: true,
                line_before: '{"level":"info","msg":"ok"}',
                line_after: '{"level":"info","msg":"ok"}',
                labels_before: {},
                labels_after: { level: 'info' },
              },
            ],
            output: '{"level":"info","msg":"ok"}',
            dropped: false,
          },
        ],
      },
    });
    await page.getByTestId('simulate-logs-tab').click();
    await page.getByTestId('simulate-logs-run').click();
    await expect(page.getByTestId('step-card-0').first()).toBeVisible();
  });

  test('7.6.7.6 — not_simulated stage shows note text', async ({ page, api }) => {
    api.seed({
      simulateLogsResult: {
        traces: [
          {
            input: 'some log line',
            steps: [
              {
                stage_index: 0,
                stage_type: 'timestamp',
                simulated: false,
                line_before: 'some log line',
                line_after: 'some log line',
                labels_before: {},
                labels_after: {},
                note: 'not_simulated — passes through unchanged in this preview',
              },
            ],
            output: 'some log line',
            dropped: false,
          },
        ],
      },
    });
    await page.getByTestId('simulate-logs-tab').click();
    await page.getByTestId('simulate-logs-run').click();
    await expect(page.getByText('not_simulated')).toBeVisible();
  });

  test('7.6.7.7 — flow check toggle activates overlay', async ({ page }) => {
    await page.getByTestId('flow-check-toggle').click();
    await expect(page.getByTestId('flow-check-active')).toBeVisible();
  });
});

// F3: BottomDrawer's relabel simulation used to send me.orgs[0].id — always
// the user's FIRST org — instead of the org selected in the switcher.
test.describe('visual simulate S2 — org selection', () => {
  const orgA = org({ id: 'org-0001', name: 'prod-org', display_name: 'Production Org' });
  const orgB = org({ id: 'org-0002', name: 'data-eng', display_name: 'Data Eng' });
  const twoOrgAdmin = {
    userOid: 'u-two-org-admin',
    email: 'twoorg@example.com',
    displayName: 'Two-Org Admin',
    isAppAdmin: true,
    authMethod: 'oidc',
    orgs: [
      { id: orgA.id, name: orgA.name, displayName: orgA.display_name, role: 'admin' },
      { id: orgB.id, name: orgB.name, displayName: orgB.display_name, role: 'admin' },
    ],
  };

  test('relabel simulation uses the selected org', async ({ page, api }) => {
    await api.loginAs(twoOrgAdmin);
    await page.addInitScript(() => {
      window.localStorage.setItem('shepherd.orgId', 'org-0002');
    });
    api.seed({ orgs: [orgA, orgB], schema: schemaFixture });

    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.getByTestId('drawer-toggle').click();
    await page.getByTestId('drawer-tab-simulate').click();
    await page.getByTestId('simulate-relabel-run').click();

    const calls = api.calls('SimulateService/SimulateRelabel');
    expect(calls.length).toBeGreaterThan(0);
    expect((calls[0].body as Record<string, unknown>).orgId).toBe('org-0002');
  });
});
