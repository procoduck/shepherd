import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('all icon-only buttons have an accessible name', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
  await page.goto('/pipelines');
  const buttons = page.getByRole('button');
  for (let i = 0; i < (await buttons.count()); i++) {
    const button = buttons.nth(i);
    if (!(await button.innerText()).trim())
      expect(
        (await button.getAttribute('aria-label')) ?? (await button.getAttribute('aria-labelledby')),
      ).toBeTruthy();
  }
});

test('status badges contain meaningful text', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
  await page.goto('/pipelines');
  const badges = page.locator('[role="badge"], .badge, [data-testid*="status"]');
  for (let i = 0; i < (await badges.count()); i++)
    expect((await badges.nth(i).innerText()).trim().length).toBeGreaterThan(0);
});

test('validation regions use polite live announcements', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/pipelines/new');
  await expect(page.locator('[aria-live="polite"]').first()).toBeVisible();
});
