// pipeline-rejection.spec.ts — #297 (walkthrough L2/L3): when a collector
// rejects its served config, its error points at a line of the MERGED config
// and nothing said which pipeline that line belongs to. Reconciliation now
// carries a served_config_rejected finding naming the pipeline; the collector
// banner, the Reconciliation tab and the pipeline page all show it.
import type { Route } from '@playwright/test';
import { basicScenario, collector } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

const SUMMARY =
  'Collector rejected in pipeline "ui-enabled" (line 5 of its text, line 40 of the merged config); the collector is still running its previous config.';

const rejection = {
  kind: 'served_config_rejected',
  sources: ['served', 'observed'],
  summary: SUMMARY,
  pipeline_name: 'ui-enabled',
  stale: false,
};

const failed = collector({
  id: 'col-rejecting',
  cluster: 'prod-eu-1',
  role: 'metrics',
  remote_config_status: 'FAILED',
  remote_config_error: '40:3: Failed to build component: decoding configuration',
});

const fulfillJSON = (route: Route, body: unknown) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

test('the collector banner and Reconciliation tab name the pipeline the collector rejected', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: [failed], reconciliation: { findings: [rejection] } });

  await page.goto(`/collectors/${failed.id}`);
  // The banner, visible on any tab.
  await expect(page.getByTestId('collector-rejected-pipeline')).toContainText(
    'rejected in pipeline "ui-enabled" (line 5 of its text',
  );

  await page.getByRole('button', { name: 'Reconciliation' }).click();
  const findings = page.getByTestId('reconciliation-findings');
  await expect(findings).toContainText('Rejected by collector');
  await expect(findings).toContainText(SUMMARY);
  // L3: the FAILED notice no longer points at a list "below" that may be empty.
  await expect(page.getByTestId('reconciliation-load-failed')).not.toContainText('below');
});

test('the pipeline page says which matched collector rejected this pipeline', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: s.pipelines,
    collectors: [failed, collector({ id: 'col-fine', role: 'logs' })],
    reconciliation: { findings: [rejection] },
  });
  api.override('POST', '/shepherd.mgmt.v1.PipelineService/PreviewMatches', (route) =>
    fulfillJSON(route, {
      collectors: [
        { id: 'col-rejecting', cluster: 'prod-eu-1', role: 'metrics' },
        { id: 'col-fine', cluster: 'prod-eu-1', role: 'logs' },
      ],
    }),
  );

  await page.goto('/pipelines/pip-0001');
  const banner = page.getByTestId('pipeline-rejected-by');
  await expect(banner).toContainText('prod-eu-1/metrics');
  await expect(banner).toContainText('line 5 of its text');
  await expect(banner).toContainText('40:3: Failed to build component');
  // Only the FAILED collector is asked about.
  await expect.poll(() => api.calls('FleetService/GetReconciliation').length).toBe(1);
});

// Review of #327: the fan-out is one GetReconciliation per FAILED matched
// collector, so it must not poll, and it asks about 20 collectors at most.
test('the pipeline page asks each failing collector once, and at most 20 of them', async ({
  page,
  api,
}) => {
  await page.clock.install();
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const many = Array.from({ length: 25 }, (_, i) =>
    collector({
      id: `col-fail-${i}`,
      cluster: `c${i}`,
      role: 'metrics',
      remote_config_status: 'FAILED',
      remote_config_error: '40:3: Failed to build component',
    }),
  );
  api.seed({
    orgs: [s.org],
    pipelines: s.pipelines,
    collectors: many,
    reconciliation: { findings: [rejection] },
  });
  api.override('POST', '/shepherd.mgmt.v1.PipelineService/PreviewMatches', (route) =>
    fulfillJSON(route, {
      collectors: many.map((c) => ({ id: c.id, cluster: c.cluster, role: c.role })),
    }),
  );

  await page.goto('/pipelines/pip-0001');
  await expect(page.getByTestId('pipeline-rejected-by')).toContainText('c0/metrics');
  await expect(page.getByTestId('pipeline-rejected-by')).toContainText(
    '5 more failing collectors were not checked',
  );
  await expect.poll(() => api.calls('FleetService/GetReconciliation').length).toBe(20);

  // No polling: two minutes later nothing has been asked again.
  await page.clock.fastForward('02:00');
  await expect(page.getByTestId('pipeline-rejected-by')).toBeVisible();
  expect(api.calls('FleetService/GetReconciliation')).toHaveLength(20);
});

test('the pipeline page stays quiet when a collector rejected a different pipeline', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: s.pipelines,
    collectors: [failed],
    reconciliation: { findings: [{ ...rejection, pipeline_name: 'git-pipe' }] },
  });
  api.override('POST', '/shepherd.mgmt.v1.PipelineService/PreviewMatches', (route) =>
    fulfillJSON(route, {
      collectors: [{ id: 'col-rejecting', cluster: 'prod-eu-1', role: 'metrics' }],
    }),
  );

  await page.goto('/pipelines/pip-0001');
  await expect(page.getByTestId('pipeline-match-count')).toBeVisible();
  await expect.poll(() => api.calls('FleetService/GetReconciliation').length).toBe(1);
  await expect(page.getByTestId('pipeline-rejected-by')).toHaveCount(0);
});
