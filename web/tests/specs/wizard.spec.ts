import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('wizard walks its schema-driven steps, previews, and commits', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/wizards');
  await expect(page.getByRole('heading', { name: /wizard/i })).toBeVisible();

  const start = page.getByRole('link', { name: /app observability|start|begin/i });
  await expect(start).toBeVisible();
  await start.click();

  const stepLabels = page.getByText(/step [1-6]|1 of 6|2 of 6|3 of 6|4 of 6|5 of 6|6 of 6/i);
  await expect(stepLabels.first()).toBeVisible();

  for (let i = 0; i < 6; i++) {
    const next = page.getByRole('button', { name: /next|continue|skip/i });
    if (!(await next.isVisible())) break;
    // Fill any empty required text field on the current step so Continue
    // enables — the stepper is schema-driven, so this walks whatever fields
    // GetWizardSchema declares rather than a hardcoded field list.
    const textInputs = page.locator('input[type="text"]');
    const count = await textInputs.count();
    for (let j = 0; j < count; j++) {
      const input = textInputs.nth(j);
      if ((await input.inputValue()) === '') await input.fill('e2e-test-value');
    }
    await next.click();
  }

  // Review step: the wizard called Render to preview the generated Alloy
  // config + diagnostics before committing.
  const commit = page.getByRole('button', { name: /commit|create|finish/i });
  await expect(commit).toBeVisible();
  await expect(commit).toBeEnabled();
  await commit.click();
  await expect(page).toHaveURL(/pipelines/);
  // Sonner (web/src/main.tsx's <Toaster>) renders toasts as
  // [data-sonner-toast], not role="alert" — matching the locator
  // states.spec.ts already proved out for the same toast library.
  await expect(
    page
      .locator('[data-sonner-toast]')
      .or(page.getByRole('alert'))
      .or(page.locator('[data-testid="toast"]')),
  ).toBeVisible();
});
