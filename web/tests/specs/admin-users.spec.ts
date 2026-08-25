/**
 * Admin → Users. Local accounts only.
 *
 * The states worth pinning are the ones an operator can misread: an account
 * that still owes a password change, one that is disabled, and the fact that a
 * non-app-admin is refused rather than shown an empty list.
 */
import { appAdmin, orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('lists local accounts with their status and org roles', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/users');

  await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
  await expect(page.getByTestId('user-row-admin')).toContainText('app admin');
  // The pending-handover state must be visible at a glance: an account whose
  // password was set by someone else is not yet the owner's.
  await expect(page.getByTestId('user-row-alice')).toContainText('must change password');
  await expect(page.getByTestId('user-row-alice')).toContainText('prod-org · editor');
});

test('says federated users are not listed here, rather than showing an empty page', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/users');
  await expect(page.getByText(/identity provider do not appear here/i)).toBeVisible();
});

test('creates a user, defaulting to a required password change', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/users');

  await page.getByTestId('user-new').click();
  // The default matters: an admin-chosen password is a handover, not a credential.
  await expect(page.getByTestId('user-must-change')).toBeChecked();

  await page.getByTestId('user-login').fill('carol');
  await page.getByTestId('user-display-name').fill('Carol');
  await page.getByTestId('user-password').fill('a-good-password');
  await page.getByRole('button', { name: /create user/i }).click();

  await expect(page.getByTestId('user-row-carol')).toBeVisible();
});

test('refuses a duplicate login and a short password', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/users');

  await page.getByTestId('user-new').click();
  await page.getByTestId('user-login').fill('ADMIN');
  await page.getByTestId('user-password').fill('a-good-password');
  await page.getByRole('button', { name: /create user/i }).click();
  await expect(page.getByText(/already exists/i)).toBeVisible();

  await page.getByTestId('user-login').fill('dave');
  await page.getByTestId('user-password').fill('short');
  await page.getByRole('button', { name: /create user/i }).click();
  // Match the server's message exactly rather than the phrase: the form's own
  // hint says "At least 8 characters" too, so a loose locator resolves to both
  // and fails strict mode -- but only once a toast from the assertion above is
  // still on screen, which is why it survived running this file alone.
  await expect(page.getByText('auth: password must be at least 8 characters')).toBeVisible();
});

test('a non-app-admin is refused', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  await page.goto('/admin/users');
  await expect(page.getByTestId('users-forbidden')).toBeVisible();
});
