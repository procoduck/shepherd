import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
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
