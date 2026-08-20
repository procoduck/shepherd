import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

/*
 * Two sibling specs were removed rather than fixed. 'destinations page renders
 * heading' only asserted the heading, which every test below already does and
 * the fullstack walkthrough covers for all routes. 'destination list requires
 * its GET handler' was labelled a kill-switch probe but asserted the heading
 * too — it could not fail if the handler disappeared. The first test below is
 * the real kill-switch: it reads rows that only exist if the list is fetched.
 */

test('seeded destinations render name, type, and URL host', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: s.destinations });
  await page.goto('/destinations');

  await expect(page.getByText('prom-prod')).toBeVisible();
  await expect(page.getByRole('cell', { name: 'prometheus', exact: true })).toBeVisible();
  await expect(page.getByText(/prometheus\.example\.com/).first()).toBeVisible();
});

test('destination create dialog reports invalid URL', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new|create|add destination/i }).click();
  // A malformed URL, not an empty one: empty fields are caught by the inputs'
  // own `required` before submit ever runs, so they exercise the browser rather
  // than this dialog's validation.
  await page.getByLabel(/name/i).fill('bad-dest');
  await page.getByLabel(/url/i).fill('not-a-url');
  await page.getByRole('button', { name: /save|create/i }).click();

  // Assert the message itself. This used to match /valid URL|URL/i, whose second
  // branch matches the dialog's own "URL" field LABEL — so it passed whether or
  // not any validation rendered, and would not have caught the message going away.
  await expect(page.getByText(/Enter a valid URL/i)).toBeVisible();
});

test('created destination appears in the list', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new|create|add destination/i }).click();
  await page.getByLabel(/name/i).fill('new-destination');
  await page.getByLabel(/url/i).fill('https://new.example.com');
  await page.getByRole('button', { name: /save|create/i }).click();

  await expect(page.getByText('new-destination')).toBeVisible();
});
