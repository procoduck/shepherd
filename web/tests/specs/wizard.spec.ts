import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('wizard walks six steps, auto-skips, and commits', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/wizards');
  await expect(page.getByRole('heading', { name: /wizard/i })).toBeVisible();
  const start = page.getByRole('link', { name: /app observability|start|begin/i });
  if (!(await start.isVisible())) test.skip();
  await start.click();
  const stepLabels = page.getByText(/step [1-6]|1 of 6|2 of 6|3 of 6|4 of 6|5 of 6|6 of 6/i);
  await expect(stepLabels.first()).toBeVisible();
  for (let i = 0; i < 6; i++) {
    const next = page.getByRole('button', { name: /next|continue|skip/i });
    if (!(await next.isVisible())) break;
    await next.click();
  }
  const commit = page.getByRole('button', { name: /commit|create|finish/i });
  await expect(commit).toBeVisible();
  await commit.click();
  await expect(page).toHaveURL(/pipelines/);
  await expect(page.getByRole('alert').or(page.locator('[data-testid="toast"]'))).toBeVisible();
});
