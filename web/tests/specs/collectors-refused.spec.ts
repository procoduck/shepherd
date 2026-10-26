// collectors-refused.spec.ts — 0.22 -> 0.23 upgrade walkthrough: while every
// poll of a collector was refused (an unbound agent token), the Collectors
// list and the collector page still read the last successful poll's APPLIED.
// The server now carries the latest refusal (migration 0041:
// last_refused_reason / last_refused_at, carried for agent.refusal_visible_for),
// and while it is newer than the last successful poll it takes precedence over
// the instance status.
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

// 0.20 walkthrough: one pod with a token scoped to another cluster, beside
// healthy pods of the same (cluster, role). The server now keeps the refusal
// for agent.refusal_visible_for even while the healthy pods poll; a poll that
// passed after it means the collector is healthy, so the status stays APPLIED
// and the refusal shows as a warning beside it.
test('a refused instance beside a healthy one shows APPLIED with a warning on the list', async ({
  page,
  api,
}) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  const c = collector({
    id: 'col-sibling',
    role: 'metrics',
    remote_config_status: 'APPLIED',
    last_seen: new Date(Date.now() - 5_000).toISOString(),
    last_refused_reason: 'claim_denied',
    last_refused_at: new Date(Date.now() - 3 * 60_000).toISOString(),
  });
  api.seed({ orgs: [s.org], collectors: [c] });

  await page.goto('/collectors');
  const row = page.getByRole('row').filter({ hasText: 'metrics' });
  await expect(row).toContainText('APPLIED');
  await expect(row.getByTestId('collector-refused-badge')).toHaveCount(0);
  await expect(row.getByTestId('collector-sibling-refused-badge')).toHaveAttribute(
    'title',
    'An instance using this collector was refused 3m ago: cluster not allowed',
  );
});

test('the collector page shows a sibling refusal as a warning, not as REFUSED', async ({
  page,
  api,
}) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  const c = collector({
    id: 'col-sibling-detail',
    remote_config_status: 'APPLIED',
    last_seen: new Date(Date.now() - 5_000).toISOString(),
    last_refused_reason: 'role_denied',
    last_refused_at: new Date(Date.now() - 2 * 60_000).toISOString(),
  });
  api.seed({ orgs: [s.org], collectors: [c] });

  await page.goto(`/collectors/${c.id}`);
  await expect(page.getByTestId('collector-status')).toHaveText('APPLIED');
  await expect(page.getByTestId('collector-refused')).toHaveCount(0);
  await expect(page.getByTestId('collector-sibling-refused')).toContainText(
    'An instance using this collector was refused 2m ago: role not allowed',
  );
});
