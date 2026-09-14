import { basicScenario } from '../fixtures/factories';
import { appAdmin, orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

/*
 * S2: DestinationsPage used to hand-roll its own overlay (a bare
 * fixed-inset-0 div with no role) instead of the shared dialog shell every
 * other create form uses. This is the kill-switch: a real role=dialog,
 * discoverable by its accessible name, not just "some element became
 * visible".
 */

test('new destination opens a labelled dialog', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new destination/i }).click();
  await expect(page.getByRole('dialog', { name: 'New destination' })).toBeVisible();
});

test('Escape closes the new destination dialog', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new destination/i }).click();
  const dialog = page.getByRole('dialog', { name: 'New destination' });
  await expect(dialog).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
});

// W7-08: real, positive coverage for a non-appAdmin persona — an org admin
// gets the same labelled dialog shell an app admin does (useCanAdminister
// permits app admin OR org role "admin", DestinationsPage.tsx).
test('an org admin also opens a labelled new destination dialog', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new destination/i }).click();
  await expect(page.getByRole('dialog', { name: 'New destination' })).toBeVisible();
});

/*
 * F1/S1: DestinationsPage (and every other page-owned dialog) keeps its
 * form state in the page, so its inline `onClose={() => setShowCreate(false)}`
 * is a new arrow function on every keystroke's re-render. Modal.tsx's
 * focus-trap effect used to be keyed on [onClose] and re-focused the panel
 * on every run, so typing more than one character kicked focus out of the
 * input — only the first character ever landed. This types character by
 * character (matching real keyboard input, unlike fill()) and asserts every
 * character survives.
 */
test('typing character by character into a page-owned dialog keeps every character', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new destination/i }).click();
  const name = page.getByLabel(/name/i);
  await name.pressSequentially('prom-eu-1', { delay: 20 });
  await expect(name).toHaveValue('prom-eu-1');
});

test('typing character by character into an admin dialog keeps every character', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ agentTokens: [] });
  await page.goto('/admin/tokens');

  await page.getByRole('button', { name: /new token/i }).click();
  const name = page.getByLabel(/name/i);
  await name.pressSequentially('prom-eu-1', { delay: 20 });
  await expect(name).toHaveValue('prom-eu-1');
});
