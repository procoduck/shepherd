import { destination, org, pipeline } from '../fixtures/factories';
import { appAdmin, orgAdmin } from '../fixtures/personas';
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

test('an org admin, not just an app admin, can view the audit log', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001' })],
    auditRows: [auditRow({ id: 1, actor: 'alice@example.com' })],
  });
  await page.goto('/audit');
  await expect(page.locator('tbody tr')).toHaveCount(1);
});

// W7-08: /audit requires org admin (routeManifest.ts) — an org admin's
// access is the whole feature, not just the ability to land on the page,
// so its write/filter affordances need their own positive control too.
test('an org admin can also filter the audit log by actor', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001' })],
    auditRows: [
      auditRow({ id: 1, actor: 'alice@example.com', action: 'pipeline.update' }),
      auditRow({ id: 2, actor: 'bob@example.com', action: 'pipeline.delete' }),
    ],
  });
  await page.goto('/audit');
  await expect(page.locator('tbody tr')).toHaveCount(2);

  await page.getByLabel('Actor').fill('bob');
  await page.getByRole('button', { name: 'Filter' }).click();

  await expect(page.locator('tbody tr')).toHaveCount(1);
  await expect(page.locator('tbody tr')).toContainText('bob@example.com');
});

// #212: inline, a full UUID ran into the resource type and read as one token.
test('the resource column shows the type and a shortened id, full id on hover', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const uuid = '0b3e9a4c-5d2f-4e61-9a7b-1c2d3e4f5a6b';
  api.seed({ orgs: [org({ id: 'org-0001' })], auditRows: [auditRow({ resource_id: uuid })] });
  await page.goto('/audit');

  const id = page.getByTestId('audit-resource-id');
  await expect(id).toHaveText('0b3e9a4c…');
  await expect(id).toHaveAttribute('title', uuid);
});

// #253: an org's audit view used to include platform events (local user
// management, SSO — rows with no org) for app admins. The org view is now that
// org's trail only; app admins read platform events in an explicit global
// scope, which asks the server for every org (no orgId).
test('an app admin reads platform events in the global scope, not in the org view', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001', name: 'prod-org' })],
    auditRows: [
      auditRow({ id: 1, action: 'pipeline.update' }),
      auditRow({ id: 2, org_id: '', action: 'user.create', resource_type: 'user' }),
    ],
  });
  await page.goto('/audit');

  await expect(page.locator('tbody tr')).toHaveCount(1);
  await expect(page.locator('tbody')).not.toContainText('user.create');

  await page.getByLabel('Scope').selectOption('all');
  await expect(page.locator('tbody tr')).toHaveCount(2);
  const platformRow = page.locator('tbody tr').filter({ hasText: 'user.create' });
  await expect(platformRow).toContainText('Platform');
  // #309: the org's display name, not its slug.
  await expect(page.locator('tbody tr').filter({ hasText: 'pipeline.update' })).toContainText(
    'Production Org',
  );
  // proto3 JSON omits an empty string, so "no org" arrives as a missing orgId.
  const calls = api.calls('AuditService/ListAudit');
  expect(calls.some((c) => !(c.body as { orgId?: string }).orgId)).toBe(true);
});

test('an org admin gets no global audit scope', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  api.seed({ orgs: [org({ id: 'org-0001' })], auditRows: [auditRow()] });
  await page.goto('/audit');
  await expect(page.locator('tbody tr')).toHaveCount(1);
  await expect(page.getByLabel('Scope')).toHaveCount(0);
});

// #309: the resource column showed a truncated id where the page could name
// the thing, and the global scope named orgs by slug.
test('the resource column names a resource the org still has', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001' })],
    pipelines: [pipeline({ id: 'pip-0001', name: 'ui-enabled' })],
    destinations: [destination({ id: 'dst-0001', name: 'prom-prod' })],
    auditRows: [
      auditRow({ id: 2, resource_type: 'pipeline', resource_id: 'pip-0001' }),
      auditRow({
        id: 1,
        at: '2026-08-19T08:00:00Z',
        action: 'destination.update',
        resource_type: 'destination',
        resource_id: 'dst-0001',
      }),
      auditRow({
        id: 3,
        at: '2026-08-19T07:00:00Z',
        resource_type: 'pipeline',
        resource_id: 'pip-gone-0000-0000',
      }),
    ],
  });
  await page.goto('/audit');

  const names = page.getByTestId('audit-resource-name');
  await expect(names).toHaveText(['ui-enabled', 'prom-prod']);
  await expect(names.first()).toHaveAttribute('title', 'pip-0001');
  // A deleted resource keeps the shortened id.
  await expect(page.getByTestId('audit-resource-id')).toHaveText('pip-gone…');
});

// 2026-10-14 walkthrough L5: a team.member event for a team deleted since
// asked ListTeamMembers for it anyway, and the 404 landed in the console.
test('a member event for a since-deleted team asks for no members, a live team still names its member', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001' })],
    auditRows: [
      auditRow({
        id: 2,
        action: 'team.member.add',
        resource_type: 'team',
        resource_id: 'team-2/user-2',
      }),
      auditRow({
        id: 1,
        at: '2026-08-19T08:00:00Z',
        action: 'team.member.add',
        resource_type: 'team',
        resource_id: 'team-gone-0000-0000/user-2',
      }),
    ],
  });
  await page.goto('/audit');

  await expect(page.getByTestId('audit-resource-name')).toHaveText(['local-squad / Alice']);
  const asked = api
    .calls('TeamService/ListTeamMembers')
    .map((c) => (c.body as { teamId: string }).teamId);
  expect(asked).toEqual(['team-2']);
});
