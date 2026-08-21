import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('all icon-only buttons have an accessible name', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
  await page.goto('/pipelines');
  const buttons = page.getByRole('button');
  // Wait for render and guard against a vacuous pass: zero buttons means the
  // loop below asserts nothing.
  await expect(buttons.first()).toBeVisible();
  const count = await buttons.count();
  expect(count).toBeGreaterThan(0);
  for (let i = 0; i < count; i++) {
    const button = buttons.nth(i);
    if (!(await button.innerText()).trim())
      expect(
        (await button.getAttribute('aria-label')) ?? (await button.getAttribute('aria-labelledby')),
      ).toBeTruthy();
  }
});

test('matcher chips contain meaningful text', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
  await page.goto('/pipelines');
  // The pipelines table renders matchers as font-mono chips (PipelinesPage) —
  // there is no [role="badge"] anywhere (badge is not an ARIA role).
  const chips = page.locator('span.font-mono.bg-border');
  await expect(chips.first()).toBeVisible();
  const count = await chips.count();
  expect(count).toBeGreaterThan(0);
  for (let i = 0; i < count; i++)
    expect((await chips.nth(i).innerText()).trim().length).toBeGreaterThan(0);
});

test('validation regions use polite live announcements', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/pipelines/new');
  await expect(page.locator('[aria-live="polite"]').first()).toBeVisible();
});
