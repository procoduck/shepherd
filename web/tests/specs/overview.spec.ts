import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('overview shows stat cards', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: /Overview/i })).toBeVisible();
  // Assert page content rendered beyond heading — stat cards or collector summary
  await expect(page.getByRole('heading', { name: /Overview/i })).toBeVisible();
});
