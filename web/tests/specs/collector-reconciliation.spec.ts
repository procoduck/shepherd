// collector-reconciliation.spec.ts — #110: CollectorDetailPage's Reconciliation
// tab surfaces declared/served/observed drift from FleetService.GetReconciliation.
import { expect } from '@playwright/test';
import { basicScenario, collector } from '../fixtures/factories';
import { appAdmin, viewer } from '../fixtures/personas';
import { test } from '../fixtures/test';

test('reconciliation tab shows "in sync" when there are no findings and the beacon reported', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const c = collector({ id: 'col-recon-1', role: 'singleton' });
  api.seed({
    orgs: [s.org],
    collectors: [c],
    reconciliation: { findings: [], observed_known: true },
  });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Reconciliation' }).click();

  await expect(page.getByTestId('reconciliation-in-sync')).toBeVisible();
  await expect(page.getByTestId('reconciliation-observed-unknown')).toHaveCount(0);
  await expect(page.getByTestId('reconciliation-findings')).toHaveCount(0);
});

// 0.22 -> 0.23 upgrade walkthrough: with no beacon data the tab said "In sync —
// declared, served and observed all agree". Observed is UNKNOWN then
// (internal/reconcile), and the tab says so, with where to look.
test('reconciliation tab says observed state is unknown when the beacon has reported nothing', async ({
  page,
  api,
}) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  const c = collector({ id: 'col-recon-unknown', role: 'metrics' });
  api.seed({
    orgs: [s.org],
    collectors: [c],
    reconciliation: { findings: [], observed_known: false },
  });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Reconciliation' }).click();

  const unknown = page.getByTestId('reconciliation-observed-unknown');
  await expect(unknown).toContainText(
    'Observed state unknown — no beacon data from this collector yet',
  );
  await expect(unknown).toContainText('SHEPHERD_AGENT_TOKEN_ID');
  await expect(unknown.getByRole('link')).toHaveAttribute('href', /docs\/collectors\.html/);
  await expect(page.getByTestId('reconciliation-in-sync')).toHaveCount(0);
});

test('reconciliation tab lists a drift finding', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const c = collector({ id: 'col-recon-2', role: 'singleton' });
  api.seed({
    orgs: [s.org],
    collectors: [c],
    reconciliation: {
      findings: [
        {
          kind: 'unserved_component_observed',
          sources: ['served', 'observed'],
          summary:
            'declared role "singleton"; beacon observed component "pipe_ghost" running (healthy=true) but not currently served to this collector',
          controller_path: 'pipe_ghost',
          stale: false,
        },
      ],
    },
  });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Reconciliation' }).click();

  const findings = page.getByTestId('reconciliation-findings');
  await expect(findings).toBeVisible();
  await expect(findings).toContainText('Running, not served');
  await expect(findings).toContainText('pipe_ghost');
  await expect(findings).toContainText('served ↔ observed');
  await expect(page.getByTestId('reconciliation-in-sync')).toHaveCount(0);
});

// M2 (2026-10-08 walkthrough): a pipeline matched to this collector but kept out
// of its served config by role enforcement used to leave only a comment in the
// served config, and this tab said "in sync".
test('reconciliation tab names a pipeline excluded by role, and does not say "in sync"', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const c = collector({ id: 'col-recon-excluded', role: 'metrics' });
  api.seed({
    orgs: [s.org],
    collectors: [c],
    reconciliation: {
      findings: [
        {
          kind: 'role_signal_excluded',
          sources: ['declared', 'served'],
          summary:
            'pipeline "app-logs" matches this collector but is excluded from its served config: its signals (logs) are not allowed on role metrics',
          pipeline_name: 'app-logs',
          stale: false,
        },
      ],
    },
  });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Reconciliation' }).click();

  const findings = page.getByTestId('reconciliation-findings');
  await expect(findings).toContainText('Matched, excluded by role');
  await expect(findings).toContainText('"app-logs"');
  await expect(findings).toContainText('its signals (logs) are not allowed on role metrics');
  await expect(findings).toContainText('declared ↔ served');
  await expect(page.getByTestId('reconciliation-in-sync')).toHaveCount(0);
});

test('reconciliation tab does not say "in sync" for a collector that rejected its config (#198)', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const c = collector({
    id: 'col-recon-failed',
    role: 'metrics',
    remote_config_status: 'FAILED',
    remote_config_error: '40:3: Failed to build component',
  });
  api.seed({ orgs: [s.org], collectors: [c], reconciliation: { findings: [] } });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Reconciliation' }).click();

  await expect(page.getByTestId('reconciliation-load-failed')).toContainText('Not in sync');
  await expect(page.getByTestId('reconciliation-in-sync')).toHaveCount(0);
});
