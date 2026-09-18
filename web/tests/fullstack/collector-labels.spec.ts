import { expect, forceRecompute, loginAsAdmin, normalizeServedConfig, test } from './fixtures';

test('collector labels persist through agent polling and group inventory without changing config', async ({
  page,
}) => {
  await loginAsAdmin(page);
  const headers = { 'X-Requested-With': 'XMLHttpRequest' };
  const meResponse = await page.request.post('/shepherd.mgmt.v1.MeService/GetMe', {
    headers,
    data: {},
  });
  expect(meResponse.ok()).toBeTruthy();
  const me = await meResponse.json();
  const org = me.orgs.find((entry: { name: string }) => entry.name === 'platform-org');
  if (!org) throw new Error('The dev seed must provide platform-org');
  const list = await page.request.post('/shepherd.mgmt.v1.FleetService/ListCollectors', {
    headers,
    data: { orgId: org.id },
  });
  expect(list.ok()).toBeTruthy();
  const collector = (await list.json()).items.find(
    (entry: { cluster: string; role: string }) =>
      entry.cluster === 'prod-eu-1' && entry.role === 'metrics',
  );
  if (!collector) throw new Error('The dev seed must provide prod-eu-1/metrics');
  const key = `ui-test-${Date.now()}`;
  await forceRecompute(page, 'prod-eu-1', 'metrics');
  const served = async () => {
    const response = await page.request.post('/shepherd.mgmt.v1.FleetService/GetServedConfig', {
      headers,
      data: { orgId: org.id, id: collector.id },
    });
    expect(response.ok()).toBeTruthy();
    return response.json();
  };
  const before = await served();
  await page.goto('/collectors');
  await page.evaluate((id: string) => localStorage.setItem('shepherd.orgId', id), org.id);
  let cleanupOK = false;
  try {
    await page.goto(`/collectors/${collector.id}`);
    await page.getByRole('button', { name: 'Manage labels' }).click();
    await page.getByLabel('Key', { exact: true }).fill(key);
    await page.getByLabel('Value', { exact: true }).fill('payments');
    await page.getByRole('button', { name: 'Add label', exact: true }).click();
    await expect(page.getByRole('button', { name: `Edit label ${key}` })).toBeVisible();
    await forceRecompute(page, 'prod-eu-1', 'metrics');
    await page.reload();
    await page.getByRole('button', { name: 'Manage labels' }).click();
    await expect(page.getByRole('region', { name: 'Collector labels' })).toContainText('payments');
    await expect(page.getByRole('region', { name: 'Alloy attributes' })).toContainText('prod-eu-1');
    await page.getByLabel('Key', { exact: true }).fill(key.toUpperCase());
    await page.getByLabel('Value', { exact: true }).fill('replacement');
    await page.getByRole('button', { name: 'Add label', exact: true }).click();
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.getByRole('region', { name: 'Collector labels' })).toContainText('payments');
    await page.getByRole('button', { name: 'Add label', exact: true }).click();
    await page.getByRole('button', { name: 'Replace label', exact: true }).click();
    await expect(page.getByRole('region', { name: 'Collector labels' })).toContainText(
      'replacement',
    );
    await page.getByRole('button', { name: `Edit label ${key}` }).click();
    await page.getByLabel('Value', { exact: true }).fill('\u00e9'.repeat(257));
    await expect(page.getByRole('button', { name: 'Save label', exact: true })).toBeDisabled();
    await page.getByLabel('Value', { exact: true }).fill('\u00e9'.repeat(256));
    await page.getByRole('button', { name: 'Save label', exact: true }).click();
    await page.reload();
    await page.getByRole('button', { name: 'Manage labels' }).click();
    await expect(page.getByRole('region', { name: 'Collector labels' })).toContainText(
      '\u00e9'.repeat(256),
    );
    await page.getByRole('button', { name: `Edit label ${key}` }).click();
    await page.getByLabel('Value', { exact: true }).fill('platform');
    await page.getByRole('button', { name: 'Save label', exact: true }).click();
    await expect(page.getByRole('region', { name: 'Collector labels' })).toContainText('platform');
    await forceRecompute(page, 'prod-eu-1', 'metrics');
    const whileLabelPresent = await served();
    // The label edits above each mark this collector's serve cache dirty
    // (see rpc_fleet.go's SetCollectorLabel/DeleteCollectorLabel), so this
    // forceRecompute genuinely re-renders — unlike `before`'s baseline
    // recompute, it stamps a new "generated at" timestamp even though
    // nothing matching-relevant changed. Compare with that line normalized
    // away, the same way internal/serve/compute_test.go does for the Go
    // suite; a raw hash/content equality check here would really be
    // asserting "the cache was never invalidated", not "labels don't affect
    // matching".
    expect(normalizeServedConfig(whileLabelPresent.content)).toBe(
      normalizeServedConfig(before.content),
    );
    await page.goto('/collectors');
    await page.getByLabel('Group by').selectOption(key);
    await expect(page.getByRole('heading', { name: `${key}=platform (1)` })).toBeVisible();
    await page.getByLabel('Search collectors').fill(`${key}=platform`);
    await expect(page.locator('main a[href^="/collectors/"]')).toHaveCount(1);
    await page.goto(`/collectors/${collector.id}`);
    await page.getByRole('button', { name: 'Manage labels' }).click();
    await page.getByRole('button', { name: `Delete label ${key}` }).click();
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.getByRole('button', { name: `Edit label ${key}` })).toBeVisible();
    await page.getByRole('button', { name: `Delete label ${key}` }).click();
    await page.getByRole('button', { name: 'Delete label', exact: true }).click();
    await expect(page.getByRole('button', { name: `Edit label ${key}` })).toHaveCount(0);
  } finally {
    const cleanup = await page.request.post('/shepherd.mgmt.v1.FleetService/DeleteCollectorLabel', {
      headers,
      data: { orgId: org.id, collectorId: collector.id, key },
    });
    cleanupOK = cleanup.ok();
  }
  expect(cleanupOK).toBeTruthy();
});
