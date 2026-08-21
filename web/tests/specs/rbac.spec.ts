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

// Reader negatives: write affordances are hidden, not disabled. Each test
// asserts a positive control first — a page that failed to render would
// otherwise make every absence assertion pass vacuously.
test('reader sees the pipelines list without New pipeline or the Enabled switch', async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByRole('link', { name: 'ui-enabled' })).toBeVisible();
  await expect(page.getByRole('link', { name: /New pipeline/i })).toHaveCount(0);
  await expect(page.getByRole('link', { name: /Visual builder/i })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^(Enable|Disable)$/ })).toHaveCount(0);
});

test('reader sees the pipeline editor without a Save button', async ({ page, api }) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
  await page.goto('/pipelines/pip-0001');
  await expect(page.locator('.cm-editor')).toBeVisible();
  await expect(page.getByRole('button', { name: /Save/i })).toHaveCount(0);
});
