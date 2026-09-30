/**
 * Admin → Users. Local accounts only.
 *
 * The states worth pinning are the ones an operator can misread: an account
 * that still owes a password change, one that is disabled, and the fact that a
 * non-app-admin is refused rather than shown an empty list.
 */
import { appAdmin, orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('shows an alert, not an empty state, when the user list fails to load', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.failNext('POST', '/shepherd.mgmt.v1.UserService/ListUsers', 503, 'unavailable');
  await page.goto('/admin/users');

  await expect(page.getByTestId('users-error')).toBeVisible({ timeout: 5000 });
});

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
  await expect(
    page.getByText('password must be at least 8 characters', { exact: true }),
  ).toBeVisible();
});

test('a non-app-admin is refused', async ({ page, api }) => {
  // W6-S7: routeManifest's requiredRole ('app-admin' for admin/*) denies the
  // direct navigation before AdminUsersPage ever mounts, so its own
  // 'users-forbidden' banner is unreachable now — the guard redirects to '/'
  // (and shows a route-denied element on the way) instead of letting the
  // page render and refuse. See route-guard.spec.ts for the full matrix.
  await api.loginAs(orgAdmin);
  await page.goto('/admin/users');
  await expect(async () => {
    const onRoot = new URL(page.url()).pathname === '/';
    const hasDeniedBanner = await page
      .getByTestId('route-denied')
      .isVisible()
      .catch(() => false);
    expect(onRoot || hasDeniedBanner).toBe(true);
  }).toPass({ timeout: 5000 });
});

// An account with no org membership signs in and sees nothing, so assigning
// one has to be reachable from the same place the account is created.
test('gives a user a role in an organisation, changes it, and removes it', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/users');

  await page.getByTestId('user-edit-alice').click();
  await expect(page.getByTestId('edit-org-role-prod-org')).toHaveValue('editor');

  await page.getByTestId('edit-org-role-prod-org').selectOption('viewer');
  // The modal shows the live membership, so the change is visible without
  // closing and reopening it.
  await expect(page.getByTestId('edit-org-role-prod-org')).toHaveValue('viewer');

  await page.getByTestId('edit-org-remove-prod-org').click();
  await expect(page.getByTestId('edit-orgs-empty')).toBeVisible();
});

test('says plainly that an account with no organisation can see nothing', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/admin/users');

  await page.getByTestId('user-edit-admin').click();
  await page.getByTestId('edit-org-remove-prod-org').click();
  await expect(page.getByTestId('edit-orgs-empty')).toContainText('sign in but see nothing');
});

// Walkthrough (#212): Delete on the signed-in, only app admin looked like any
// other row's — no word that it was their own account or the last admin.
test('deleting your own account, the only app admin, says both', async ({ page, api }) => {
  await api.loginAs({ ...appAdmin, userOid: 'local:admin', authMethod: 'local' });
  await page.goto('/admin/users');

  await page.getByTestId('user-delete-admin').click();
  await expect(page.getByRole('heading', { name: 'Delete your own account?' })).toBeVisible();
  await expect(page.getByText(/you will be signed out/)).toBeVisible();
  await expect(page.getByText(/only active app admin/)).toBeVisible();
  await page.getByRole('button', { name: 'Cancel' }).click();

  // Someone else's account reads as before.
  await page.getByTestId('user-delete-alice').click();
  await expect(page.getByRole('heading', { name: 'Delete alice?' })).toBeVisible();
  await expect(page.getByText(/you will be signed out/)).toHaveCount(0);
});

test('adding a user to an organisation defaults to the least-privileged role', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [
      { id: 'org-0001', name: 'prod-org', display_name: 'Production Org' },
      { id: 'org-0002', name: 'staging-org', display_name: 'Staging Org' },
    ],
  });
  await page.goto('/admin/users');
  await page.getByTestId('user-edit-alice').click();
  await expect(page.getByLabel('Role in the organisation to add')).toHaveValue('viewer');
  await expect(page.getByLabel('Organisation to add', { exact: true })).toBeVisible();
});
