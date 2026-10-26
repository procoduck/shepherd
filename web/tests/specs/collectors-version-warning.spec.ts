// collectors-version-warning.spec.ts — #375: when a collector's instances
// change after a save (an older Alloy pod joins), the validated config keeps
// being served and the agent API records what the version check found on
// the collector (Collector.version_warning, migration 0044). The Collectors
// list marks it beside the version; the collector page lists the findings.
import { expect } from '@playwright/test';
import { basicScenario, collector } from '../fixtures/factories';
import { orgAdmin, viewer } from '../fixtures/personas';
import { test } from '../fixtures/test';

const since = new Date(Date.now() - 60_000).toISOString();
const missing =
  'line 12: attribute scrape_classic_histograms (in prometheus.operator.podmonitors "pods" > scrape) is not available in Alloy v1.18.1';
const note =
  'line 3: component otelcol.exporter.debug is experimental in Alloy v1.21.2; collectors need --stability.level=experimental to load it';

test('the Collectors list marks a version mismatch beside the version', async ({ page, api }) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  const flagged = collector({
    id: 'col-mismatch',
    role: 'metrics',
    alloy_version: 'v1.21.2',
    version_warning: { findings: [missing], mismatch: true, since },
  });
  const noted = collector({
    id: 'col-note',
    role: 'logs',
    alloy_version: 'v1.21.2',
    version_warning: { findings: [note], mismatch: false, since },
  });
  const clean = collector({ id: 'col-clean', role: 'singleton', alloy_version: 'v1.21.2' });
  api.seed({ orgs: [s.org], collectors: [flagged, noted, clean] });

  await page.goto('/collectors');
  const flaggedRow = page.getByRole('row').filter({ hasText: 'metrics' });
  await expect(flaggedRow.getByTestId('collector-version-warning-badge')).toHaveText('MISMATCH');
  await expect(flaggedRow.getByTestId('collector-version-warning-badge')).toHaveAttribute(
    'title',
    /scrape_classic_histograms/,
  );
  const notedRow = page.getByRole('row').filter({ hasText: 'logs' });
  await expect(notedRow.getByTestId('collector-version-warning-badge')).toHaveText('NOTE');
  const cleanRow = page.getByRole('row').filter({ hasText: 'singleton' });
  await expect(cleanRow.getByTestId('collector-version-warning-badge')).toHaveCount(0);
});

test('the collector page lists the findings of a version mismatch', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  const c = collector({
    id: 'col-mismatch-detail',
    remote_config_status: 'APPLIED',
    version_warning: { findings: [missing, note], mismatch: true, since },
  });
  api.seed({ orgs: [s.org], collectors: [c] });

  await page.goto(`/collectors/${c.id}`);
  const banner = page.getByTestId('collector-version-warning');
  await expect(banner).toContainText('Alloy version mismatch');
  await expect(banner).toContainText('keeps serving the validated config');
  await expect(banner.getByRole('listitem')).toHaveCount(2);
  await expect(banner).toContainText('scrape_classic_histograms');
  await expect(page.getByTestId('collector-status')).toHaveText('APPLIED');
});

test('a collector with no version warning shows no banner', async ({ page, api }) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  const c = collector({ id: 'col-no-warning', remote_config_status: 'APPLIED' });
  api.seed({ orgs: [s.org], collectors: [c] });

  await page.goto(`/collectors/${c.id}`);
  await expect(page.getByTestId('collector-status')).toHaveText('APPLIED');
  await expect(page.getByTestId('collector-version-warning')).toHaveCount(0);
});
