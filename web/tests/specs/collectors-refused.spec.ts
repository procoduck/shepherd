// collectors-refused.spec.ts — 0.22 -> 0.23 upgrade walkthrough: while every
// poll of a collector was refused (an unbound agent token), the Collectors
// list and the collector page still read the last successful poll's APPLIED.
// The server now carries the latest refusal (migration 0041:
// last_refused_reason / last_refused_at, set only while newer than the last
// successful poll), and it takes precedence over the instance status.
import { expect } from '@playwright/test';
import { basicScenario, collector } from '../fixtures/factories';
import { orgAdmin, viewer } from '../fixtures/personas';
import { test } from '../fixtures/test';

const refusedAt = new Date(Date.now() - 30_000).toISOString();

test('the Collectors list shows a refused collector as REFUSED, not APPLIED', async ({
  page,
  api,
}) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  const refused = collector({
    id: 'col-refused',
    cluster: 'prod-eu-1',
    role: 'metrics',
    remote_config_status: 'APPLIED',
    last_seen: new Date(Date.now() - 120_000).toISOString(),
    last_refused_reason: 'token_unbound',
    last_refused_at: refusedAt,
  });
  const healthy = collector({
    id: 'col-healthy',
    cluster: 'prod-eu-1',
    role: 'logs',
    remote_config_status: 'APPLIED',
    last_seen: new Date().toISOString(),
  });
  api.seed({ orgs: [s.org], collectors: [refused, healthy] });

  await page.goto('/collectors');
  const refusedRow = page.getByRole('row').filter({ hasText: 'metrics' });
  await expect(refusedRow.getByTestId('collector-refused-badge')).toHaveText('REFUSED');
  await expect(refusedRow.getByTestId('collector-refused-badge')).toHaveAttribute(
    'title',
    /agent token not bound to an organisation/,
  );
  await expect(refusedRow).not.toContainText('APPLIED');
  const healthyRow = page.getByRole('row').filter({ hasText: 'logs' });
  await expect(healthyRow).toContainText('APPLIED');
  await expect(healthyRow.getByTestId('collector-refused-badge')).toHaveCount(0);
});

test('the collector page explains a token_unbound refusal and links to Agent tokens', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  const c = collector({
    id: 'col-refused-detail',
    remote_config_status: 'APPLIED',
    last_refused_reason: 'token_unbound',
    last_refused_at: refusedAt,
  });
  api.seed({ orgs: [s.org], collectors: [c] });

  await page.goto(`/collectors/${c.id}`);
  await expect(page.getByTestId('collector-status')).toHaveText('REFUSED');
  const banner = page.getByTestId('collector-refused');
  await expect(banner).toContainText('Refused — agent token not bound to an organisation');
  await expect(banner.getByRole('link', { name: 'Agent tokens' })).toHaveAttribute(
    'href',
    '/admin/tokens',
  );
});

for (const [reason, text] of [
  ['claim_denied', 'Refused — cluster not allowed'],
  ['role_denied', 'Refused — role not allowed'],
] as const) {
  test(`the collector page explains a ${reason} refusal`, async ({ page, api }) => {
    await api.loginAs(viewer);
    const s = basicScenario();
    const c = collector({
      id: `col-${reason}`,
      remote_config_status: 'APPLIED',
      last_refused_reason: reason,
      last_refused_at: refusedAt,
    });
    api.seed({ orgs: [s.org], collectors: [c] });

    await page.goto(`/collectors/${c.id}`);
    await expect(page.getByTestId('collector-status')).toHaveText('REFUSED');
    await expect(page.getByTestId('collector-refused')).toContainText(text);
    await expect(page.getByTestId('collector-refused').getByRole('link')).toHaveCount(0);
  });
}

test('a collector with no refusal shows its instance status and no banner', async ({
  page,
  api,
}) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  const c = collector({ id: 'col-ok', remote_config_status: 'APPLIED' });
  api.seed({ orgs: [s.org], collectors: [c] });

  await page.goto(`/collectors/${c.id}`);
  await expect(page.getByTestId('collector-status')).toHaveText('APPLIED');
  await expect(page.getByTestId('collector-refused')).toHaveCount(0);
});
