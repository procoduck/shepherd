import { collector, org } from '../fixtures/factories';
import { appAdmin, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// B5: CollectorDetailPage's Access tab — list/add/remove group assignments.
// Org-admin only (appAdmin/orgAdmin personas both carry role "admin" for
// org-0001; reader carries role "reader").

test('collector access tab lists, adds, and removes group assignments', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001' });
  const c = collector({ id: 'col-0001', cluster: 'prod-eu-1', role: 'metrics' });
  api.seed({ orgs: [o], collectors: [c] });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Access' }).click();

  await expect(page.getByText('No groups have access to this collector yet.')).toBeVisible();

  await page.getByTestId('group-id-input').fill('readers-group-id');
  await page.getByTestId('add-assignment-btn').click();

  const row = page.getByRole('row').filter({ hasText: 'readers-group-id' });
  await expect(row).toBeVisible();

  const createCalls = api.calls('/shepherd.mgmt.v1.FleetService/CreateAssignment');
  expect(createCalls).toHaveLength(1);
  expect(createCalls[0]?.body).toMatchObject({
    orgId: 'org-0001',
    collectorId: 'col-0001',
    groupId: 'readers-group-id',
  });

  await row.getByRole('button', { name: /remove/i }).click();
  await expect(page.getByText('No groups have access to this collector yet.')).toBeVisible();

  const deleteCalls = api.calls('/shepherd.mgmt.v1.FleetService/DeleteAssignment');
  expect(deleteCalls).toHaveLength(1);
  expect(deleteCalls[0]?.body).toMatchObject({
    orgId: 'org-0001',
    collectorId: 'col-0001',
    groupId: 'readers-group-id',
  });
});

test('group search is wired and degrades gracefully when it returns nothing', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001' });
  const c = collector({ id: 'col-0001' });
  // Real server: SearchGroups is a stub, so the default empty seed is the
  // realistic case — the box must still degrade gracefully.
  api.seed({ orgs: [o], collectors: [c], groupSearchResults: [] });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Access' }).click();

  const input = page.getByTestId('group-search-input');
  await input.fill('platform');
  await expect(page.getByText(/No groups found\. Paste a group ID below instead\./)).toBeVisible();
  expect(api.calls('/shepherd.mgmt.v1.AdminService/SearchGroups').length).toBeGreaterThanOrEqual(1);
});

test('group search debounce suppresses calls within the typing window', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001' });
  const c = collector({ id: 'col-0001' });
  api.seed({ orgs: [o], collectors: [c] });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Access' }).click();

  const input = page.getByTestId('group-search-input');
  // Type rapidly — the 300ms debounce should suppress calls fired mid-burst.
  await input.pressSequentially('test', { delay: 50 });
  expect(api.calls('/shepherd.mgmt.v1.AdminService/SearchGroups').length).toBeLessThanOrEqual(1);
  await page.waitForTimeout(400);
  expect(api.calls('/shepherd.mgmt.v1.AdminService/SearchGroups').length).toBeGreaterThanOrEqual(1);
});

test('search result click adds the group directly', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001' });
  const c = collector({ id: 'col-0001' });
  api.seed({
    orgs: [o],
    collectors: [c],
    groupSearchResults: [{ id: 'grp-9001', display_name: 'Platform Team' }],
  });

  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Access' }).click();
  await page.getByTestId('group-search-input').fill('platform');

  await page.getByRole('button', { name: /Platform Team/ }).click();

  const row = page.getByRole('row').filter({ hasText: 'Platform Team' });
  await expect(row).toBeVisible();
  const createCalls = api.calls('/shepherd.mgmt.v1.FleetService/CreateAssignment');
  expect(createCalls.at(-1)?.body).toMatchObject({ groupId: 'grp-9001' });
});

test('Access tab is hidden for a non-admin org role', async ({ page, api }) => {
  await api.loginAs(reader);
  const o = org({ id: 'org-0001' });
  const c = collector({ id: 'col-0001' });
  api.seed({ orgs: [o], collectors: [c] });

  await page.goto(`/collectors/${c.id}`);
  await expect(page.getByRole('button', { name: 'Served Config' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Access' })).toHaveCount(0);
});
