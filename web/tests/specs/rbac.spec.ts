import { basicScenario } from '../fixtures/factories';
import { appAdmin, localAdmin, orgAdmin, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('appAdmin sees admin nav group', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByText(/Admin/i)).toBeVisible();
});

test('orgAdmin does not see admin nav group', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Orgs' })).not.toBeVisible();
});

test('appAdmin sees New pipeline button', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByRole('link', { name: /New pipeline/i })).toBeVisible();
});

test('local admin persona sees Admin nav group', async ({ page, api }) => {
  await api.loginAs(localAdmin);
  await page.goto('/');
  await expect(page.getByText('Admin')).toBeVisible();
});
