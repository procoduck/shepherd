import type { Route } from '@playwright/test';
import { basicScenario, destination } from '../fixtures/factories';
import { appAdmin, orgEditor } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// Walkthrough finding M2: a saved pipeline whose matchers select a collector
// whose role refuses its signals is left out of that collector's served
// config by role enforcement (gate G6). The server marks each such collector
// with excluded_reason; the pipeline page and the wizard's matched list must
// say so instead of staying silent.

const REASON = 'its signals (logs) are not allowed on role metrics';

const fulfillJSON = (route: Route, body: unknown) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

test('the pipeline page counts matched collectors and warns about ones its role excludes', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  // Before the save the cluster-only matcher also selects a metrics
  // collector; after it, only the logs one.
  let saved = false;
  api.override('POST', '/shepherd.mgmt.v1.PipelineService/PreviewMatches', (route) =>
    fulfillJSON(route, {
      collectors: saved
        ? [{ id: 'col-logs', cluster: 'prod-eu-1', role: 'logs' }]
        : [
            { id: 'col-logs', cluster: 'prod-eu-1', role: 'logs' },
            { id: 'col-metrics', cluster: 'prod-eu-1', role: 'metrics', excludedReason: REASON },
          ],
    }),
  );

  await page.goto('/pipelines/pip-0001');
  await expect(page.getByTestId('pipeline-match-count')).toHaveText('Matches 2 collectors.');
  await expect(page.getByTestId('pipeline-role-exclusion')).toHaveText(
    `Excluded from 1 collector(s): prod-eu-1/metrics — ${REASON}.`,
  );

  // Enable/disable refetches the preview.
  const before = api.calls('PipelineService/PreviewMatches').length;
  await page.getByRole('switch', { name: 'Enabled: ui-enabled' }).click();
  await expect(page.getByTestId('pipeline-match-count')).toHaveText(
    'Matches 2 collectors (when enabled).',
  );
  await expect
    .poll(() => api.calls('PipelineService/PreviewMatches').length)
    .toBeGreaterThan(before);

  // So does a save: the preview is of the SAVED pipeline.
  saved = true;
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByTestId('pipeline-match-count')).toHaveText(
    'Matches 1 collector (when enabled).',
  );
  await expect(page.getByTestId('pipeline-role-exclusion')).toHaveCount(0);
});

test('the wizard Review step marks a matched collector its role excludes', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [destination({ id: 'dst-prom', name: 'prom-prod', type: 'prometheus' })],
  });
  api.override('POST', '/shepherd.mgmt.v1.WizardService/RenderWizard', (route) =>
    fulfillJSON(route, {
      contents: 'prometheus.scrape "app" {}\n',
      matchers: ['cluster=~"prod-.*"'],
      valid: true,
      diagnostics: [],
      matchedCollectors: [
        { id: 'col-metrics', cluster: 'prod-eu-1', role: 'metrics' },
        { id: 'col-logs', cluster: 'prod-eu-1', role: 'logs', excludedReason: REASON },
      ],
      warnings: [`Excluded from 1 collector(s): prod-eu-1/logs — ${REASON}.`],
    }),
  );

  await page.goto('/wizards');
  await page.getByRole('link', { name: /app observability|start|begin/i }).click();
  await page.getByLabel('Metrics endpoint URL').fill('http://myapp:9090/metrics');
  await page.getByLabel('Job label').fill('my-app');
  await page.getByRole('button', { name: /next|continue/i }).click();
  await page.getByRole('button', { name: /next|continue/i }).click();
  await page.getByLabel('Metrics destination').selectOption('prom-prod');
  await page.getByRole('button', { name: /next|continue/i }).click();
  await page.getByRole('button', { name: /next|continue/i }).click();

  await expect(page.getByTestId('wizard-match-preview')).toContainText('Matches 2 collectors');
  const marker = page.getByTestId('wizard-excluded-collector');
  await expect(marker).toHaveCount(1);
  await expect(page.locator('li', { has: marker })).toContainText('prod-eu-1 / logs');
  await expect(marker).toContainText(REASON);
});
