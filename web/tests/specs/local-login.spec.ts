import { basicScenario } from '../fixtures/factories';
import { localAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('methods:both renders both OIDC button and local form', async ({ page, api }) => {
  api.seed({ me: null, authMethods: { oidc: true, local_admin: true } });
  await page.goto('/login');
  await expect(page.getByTestId('oidc-login-btn')).toBeVisible();
  await expect(page.getByTestId('local-login-submit')).toBeVisible();
  await expect(page.getByText(/Access is managed through your Entra ID groups/)).toBeVisible();
});

test('methods:local-only hides Microsoft button and Entra copy', async ({ page, api }) => {
  api.seed({ me: null, authMethods: { oidc: false, local_admin: true } });
  await page.goto('/login');
  await expect(page.getByTestId('oidc-login-btn')).not.toBeVisible();
  await expect(page.getByText(/Access is managed through your Entra ID groups/)).not.toBeVisible();
  await expect(page.getByTestId('local-login-submit')).toBeVisible();
});

test('bad credentials show the inline error message', async ({ page, api }) => {
  api.seed({
    me: null,
    authMethods: { oidc: false, local_admin: true },
    localAdminCreds: { username: 'admin', password: 'correct-pass' },
  });
  await page.goto('/login');
  await page.getByTestId('local-username').fill('admin');
  await page.getByTestId('local-password').fill('wrong-pass');
  await page.getByTestId('local-login-submit').click();
  await expect(page.getByTestId('local-login-error')).toBeVisible({ timeout: 3000 });
  await expect(page.getByTestId('local-login-error')).toContainText(
    /Invalid username or password/i,
  );
});

test('success navigates to / and amber banner is visible', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({
    me: null,
    orgs: [s.org],
    authMethods: { oidc: false, local_admin: true },
    localAdminCreds: { username: 'admin', password: 'correct-pass' },
    localAdminPersona: localAdmin,
  });
  await page.goto('/login');
  await page.getByTestId('local-username').fill('admin');
  await page.getByTestId('local-password').fill('correct-pass');
  await page.getByTestId('local-login-submit').click();
  await expect(page).toHaveURL(/\/$/, { timeout: 5000 });
  await expect(page.getByTestId('local-admin-banner')).toBeVisible();
  await page.reload();
  await expect(page.getByTestId('local-admin-banner')).toBeVisible();
});

test('methods:neither shows the no-methods error card', async ({ page, api }) => {
  api.seed({ me: null, authMethods: { oidc: false, local_admin: false } });
  await page.goto('/login');
  await expect(page.getByTestId('no-methods-card')).toBeVisible();
  await expect(page.getByTestId('oidc-login-btn')).not.toBeVisible();
  await expect(page.getByTestId('local-login-submit')).not.toBeVisible();
});
