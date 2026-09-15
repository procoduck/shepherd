// The server has always reported must_change_password on local login and
// blocked every other request until the password is changed
// (internal/auth/password_change.go); until 2026-09-15 the SPA had no screen
// for it, so a bootstrap admin without SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD, and
// every admin-created account, was stuck unless the operator knew the raw
// endpoint. These specs drive the three ways the screen is reached.
import { basicScenario } from '../fixtures/factories';
import { localAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

const localAdminSeed = () => ({
  me: null,
  orgs: [basicScenario().org],
  authMethods: { oidc: false, local_admin: true },
  localAdminCreds: { username: 'admin', password: 'admin' },
  localAdminPersona: localAdmin,
});

test('a local login that owes a password change is taken straight to the screen', async ({
  page,
  api,
}) => {
  api.seed({ ...localAdminSeed(), localAdminMustChange: true });
  await page.goto('/login');
  await page.getByTestId('local-username').fill('admin');
  await page.getByTestId('local-password').fill('admin');
  await page.getByTestId('local-login-submit').click();

  await expect(page).toHaveURL(/\/change-password/, { timeout: 5000 });
  await expect(page.getByTestId('change-password-required')).toBeVisible();

  // Mismatched confirmation never leaves the browser.
  await page.getByTestId('current-password').fill('admin');
  await page.getByTestId('new-password').fill('a-much-better-one');
  await page.getByTestId('confirm-password').fill('a-different-one');
  await page.getByTestId('change-password-submit').click();
  await expect(page.getByTestId('change-password-error')).toContainText(/do not match/i);

  // The server's own refusals are shown verbatim.
  await page.getByTestId('current-password').fill('not-admin');
  await page.getByTestId('confirm-password').fill('a-much-better-one');
  await page.getByTestId('change-password-submit').click();
  await expect(page.getByTestId('change-password-error')).toContainText(
    /current password is incorrect/i,
  );

  await page.getByTestId('current-password').fill('admin');
  await page.getByTestId('new-password').fill('short');
  await page.getByTestId('confirm-password').fill('short');
  await page.getByTestId('change-password-submit').click();
  await expect(page.getByTestId('change-password-error')).toContainText(/at least 8 characters/i);

  // Success unblocks the session and lands on the overview.
  await page.getByTestId('new-password').fill('a-much-better-one');
  await page.getByTestId('confirm-password').fill('a-much-better-one');
  await page.getByTestId('change-password-submit').click();
  await expect(page).toHaveURL(/\/$/, { timeout: 5000 });
  await expect(
    page.getByTestId('nav-overview').or(page.getByRole('link', { name: 'Overview' })),
  ).toBeVisible();
});

test('a session that owes a password change is redirected from any page', async ({ page, api }) => {
  // The SPA does not decide this: the server answers every request with 403
  // password_change_required, and the SPA routes that to the screen instead
  // of treating it as "not signed in" and bouncing to /login in a loop.
  api.seed({ ...localAdminSeed(), me: localAdmin, passwordChangeRequired: true });
  await page.goto('/pipelines');
  await expect(page).toHaveURL(/\/change-password/, { timeout: 5000 });
  await expect(page.getByTestId('change-password-required')).toBeVisible();
});

test('a local user changes their own password from Admin → Users', async ({ page, api }) => {
  api.seed({ ...localAdminSeed(), me: localAdmin });
  await page.goto('/admin/users');
  await page.getByTestId('change-my-password').click();
  await expect(page).toHaveURL(/\/change-password/);
  // Voluntary: no "you must" banner.
  await expect(page.getByTestId('change-password-required')).toHaveCount(0);
  await page.getByTestId('current-password').fill('admin');
  await page.getByTestId('new-password').fill('a-much-better-one');
  await page.getByTestId('confirm-password').fill('a-much-better-one');
  await page.getByTestId('change-password-submit').click();
  await expect(page).toHaveURL(/\/$/, { timeout: 5000 });
});
