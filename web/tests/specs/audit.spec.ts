import { org } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

function auditRow(o: Partial<Record<string, unknown>> = {}) {
  return {
    id: 1,
    at: '2026-08-19T09:00:00Z',
    actor: 'alice@example.com',
    actor_type: 'user',
    org_id: 'org-0001',
    action: 'pipeline.update',
    resource_type: 'pipeline',
    resource_id: 'pip-0001',
    ...o,
  };
}

test('audit page shows an empty state when there are no entries', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [org({ id: 'org-0001' })], auditRows: [] });
  await page.goto('/audit');
  await expect(page.getByText('No audit entries yet.')).toBeVisible();
});

test('audit page lists entries newest first', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001' })],
    auditRows: [
      auditRow({ id: 1, at: '2026-08-19T09:00:00Z', actor: 'alice@example.com' }),
      auditRow({ id: 2, at: '2026-08-19T10:00:00Z', actor: 'bob@example.com' }),
    ],
  });
  await page.goto('/audit');

  const rows = page.locator('tbody tr');
  await expect(rows).toHaveCount(2);
  // Newest (bob, 10:00) first — mirrors the backend's ORDER BY at DESC.
  await expect(rows.nth(0)).toContainText('bob@example.com');
  await expect(rows.nth(1)).toContainText('alice@example.com');
});

test('audit page filters by actor and action', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001' })],
    auditRows: [
      auditRow({ id: 1, actor: 'alice@example.com', action: 'pipeline.update' }),
      auditRow({ id: 2, actor: 'bob@example.com', action: 'pipeline.delete' }),
    ],
  });
  await page.goto('/audit');
  await expect(page.locator('tbody tr')).toHaveCount(2);

  await page.getByLabel('Actor').fill('alice');
  await page.getByRole('button', { name: 'Filter' }).click();

  await expect(page.locator('tbody tr')).toHaveCount(1);
  await expect(page.locator('tbody tr')).toContainText('alice@example.com');

  const calls = api.calls('/shepherd.mgmt.v1.AuditService/ListAudit');
  expect(calls.at(-1)?.body).toMatchObject({ actor: 'alice' });

  await page.getByRole('button', { name: 'Clear' }).click();
  await expect(page.locator('tbody tr')).toHaveCount(2);
});

test('audit page pages through results with limit/offset', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const rows = Array.from({ length: 30 }, (_, i) =>
    auditRow({ id: i + 1, actor: `user-${i + 1}@example.com` }),
  );
  api.seed({ orgs: [org({ id: 'org-0001' })], auditRows: rows });
  await page.goto('/audit');

  await expect(page.locator('tbody tr')).toHaveCount(25);
  await expect(page.getByText('1–25 of 30')).toBeVisible();

  await page.getByRole('button', { name: 'Next page' }).click();
  await expect(page.getByText('26–30 of 30')).toBeVisible();
  await expect(page.locator('tbody tr')).toHaveCount(5);

  const calls = api.calls('/shepherd.mgmt.v1.AuditService/ListAudit');
  expect(calls.at(-1)?.body).toMatchObject({ limit: 25, offset: 25 });

  await page.getByRole('button', { name: 'Previous page' }).click();
  await expect(page.getByText('1–25 of 30')).toBeVisible();
});
