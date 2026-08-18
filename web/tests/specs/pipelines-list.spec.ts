import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('pipelines table shows all pipelines', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByRole('table')).toBeVisible();
  await expect(page.getByText('ui-enabled')).toBeVisible();
  await expect(page.getByText('wizard-disabled')).toBeVisible();
  await expect(page.getByText('git-pipe')).toBeVisible();
});

test('New pipeline button is visible for orgAdmin', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByRole('link', { name: /New pipeline/i })).toBeVisible();
});
