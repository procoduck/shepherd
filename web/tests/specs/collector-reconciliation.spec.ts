// collector-reconciliation.spec.ts — #110: CollectorDetailPage's Reconciliation
// tab surfaces declared/served/observed drift from FleetService.GetReconciliation.
import { expect } from '@playwright/test';
import { basicScenario, collector } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { test } from '../fixtures/test';

test('reconciliation tab shows "in sync" when there are no findings', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const c = collector({ id: 'col-recon-1', role: 'singleton' });
  api.seed({ orgs: [s.org], collectors: [c], reconciliation: { findings: [] } });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Reconciliation' }).click();

  await expect(page.getByTestId('reconciliation-in-sync')).toBeVisible();
  await expect(page.getByTestId('reconciliation-findings')).toHaveCount(0);
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
            'declared role "singleton"; beacon observed component "pipe_ghost" running that Shepherd never served to this collector',
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
