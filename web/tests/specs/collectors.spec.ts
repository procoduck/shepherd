import { basicScenario } from '../fixtures/factories';
import { appAdmin, orgAdmin, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('collectors table shows rows', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto('/collectors');
  await expect(page.getByRole('table')).toBeVisible();
});

test('row click navigates to collector detail', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto('/collectors');
  // Wait for data to load
  await expect(page.getByRole('row').nth(1)).toBeVisible();
  // Click the link inside the first data row
  await page.getByRole('link', { name: 'prod-eu-1' }).first().click();
  await expect(page).toHaveURL(/\/collectors\//);
});

test('an org admin generates k8s-monitoring values to connect a cluster', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors, claimedElsewhere: ['theirs'] });
  await api.loginAs(orgAdmin);
  await page.goto('/collectors');
  await page.getByTestId('connect-cluster-open').click();
  const dialog = page.getByTestId('connect-cluster');

  await dialog.getByTestId('connect-cluster-name').fill('prod-eu-1');
  await dialog.getByTestId('connect-cluster-role-singleton').uncheck();
  await dialog.getByTestId('connect-cluster-role-receiver').check();
  await dialog.getByTestId('connect-cluster-render').click();

  await expect(dialog.getByTestId('connect-cluster-status')).toContainText(
    'has not seen this cluster',
  );
  await expect(dialog.getByTestId('connect-cluster-values')).toContainText('name: "prod-eu-1"');
  await expect(dialog.getByTestId('connect-cluster-values')).toContainText('alloy-receiver');
  await expect(dialog.getByTestId('connect-cluster-values')).not.toContainText('alloy-singleton');
  await expect(dialog.getByTestId('connect-cluster-secret-command')).toContainText('-n monitoring');
  const calls = api.calls('/shepherd.mgmt.v1.FleetService/RenderChartValues');
  expect(calls[0].body).toMatchObject({
    clusterName: 'prod-eu-1',
    roles: ['metrics', 'logs', 'receiver'],
  });

  // A name another org owns is refused with the reason.
  await dialog.getByTestId('connect-cluster-name').fill('theirs');
  await dialog.getByTestId('connect-cluster-render').click();
  await expect(dialog.getByTestId('connect-cluster-error')).toContainText('another organisation');
});

test('a reader is not offered Connect a cluster', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await api.loginAs(reader);
  await page.goto('/collectors');
  await expect(page.getByRole('table')).toBeVisible();
  await expect(page.getByTestId('connect-cluster-open')).toHaveCount(0);
});
