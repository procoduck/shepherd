import { basicScenario, destination } from '../fixtures/factories';
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
    // Same for an empty required select (e.g. a *_dest_name field rendered
    // as a destination picker when the org has destinations) — pick its
    // first real option so Continue enables. Harmless on a select that
    // isn't required: it just pre-picks a value nobody was going to leave
    // blank anyway.
    const selects = page.locator('select');
    const selectCount = await selects.count();
    for (let j = 0; j < selectCount; j++) {
      const select = selects.nth(j);
      if ((await select.inputValue()) === '') {
        const firstRealOption = select.locator('option:not([value=""])').first();
        if (await firstRealOption.count()) {
          await select.selectOption(await firstRealOption.getAttribute('value'));
        }
      }
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

test('destination fields pick from the org’s destinations by type, not free text', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [
      destination({ id: 'dst-prom', name: 'prom-prod', type: 'prometheus' }),
      destination({ id: 'dst-loki', name: 'loki-prod', type: 'loki' }),
    ],
  });
  await page.goto('/wizards');
  await page.getByRole('link', { name: /app observability|start|begin/i }).click();

  // Step 1 (Scrape targets): fill the required text fields, continue.
  await page.getByLabel('Metrics endpoint URL').fill('http://myapp:9090/metrics');
  await page.getByLabel('Job label').fill('my-app');
  await page.getByRole('button', { name: /next|continue/i }).click();

  // Step 2 (Log collection): nothing required, continue straight through.
  await page.getByRole('button', { name: /next|continue/i }).click();

  // Step 3 (Destinations): metrics_dest_name is a prometheus-only picker.
  const metricsDest = page.getByLabel('Metrics destination');
  await expect(metricsDest).toHaveRole('combobox');
  const optionTexts = await metricsDest.locator('option').allTextContents();
  expect(optionTexts).toContain('prom-prod');
  expect(optionTexts).not.toContain('loki-prod');
});

test('a matcher the wizard added silently is labelled on Review; one the user chose is not', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [destination({ id: 'dst-prom', name: 'prom-prod', type: 'prometheus' })],
  });
  // Simulate a wizard that appends role="singleton" after the user's own
  // cluster_pattern (the self-monitoring wizard's real behaviour,
  // internal/wizard/selfmonitoring/wizard.go:180-184) so the fixture is
  // independent of what the mocked app-observability schema happens to
  // render for `role`.
  api.override('POST', '/shepherd.mgmt.v1.WizardService/RenderWizard', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        contents: 'prometheus.scrape "app" {}\n',
        matchers: ['cluster=~"prod-.*"', 'role="singleton"'],
        valid: true,
        diagnostics: [],
        matchedCollectors: [],
      }),
    });
  });

  await page.goto('/wizards');
  await page.getByRole('link', { name: /app observability|start|begin/i }).click();

  await page.getByLabel('Metrics endpoint URL').fill('http://myapp:9090/metrics');
  await page.getByLabel('Job label').fill('my-app');
  await page.getByRole('button', { name: /next|continue/i }).click();

  await page.getByRole('button', { name: /next|continue/i }).click();

  await page.getByLabel('Metrics destination').selectOption('prom-prod');
  await page.getByRole('button', { name: /next|continue/i }).click();

  // Step 4 (Collector matching): type the same value the override's cluster
  // matcher carries, so that chip is recognised as the user's own input;
  // leave role at its schema default so the override's role="singleton"
  // does not match anything the user actually picked.
  await page.getByLabel('Cluster pattern (regex)').fill('prod-.*');
  await page.getByRole('button', { name: /next|continue/i }).click();

  // Review: exactly one chip is flagged as wizard-added.
  await expect(page.getByTestId('wizard-added-matcher')).toHaveCount(1);
  const roleChip = page.locator('span', { hasText: 'role="singleton"' }).first();
  await expect(roleChip.locator('[data-testid="wizard-added-matcher"]')).toHaveCount(1);
});
