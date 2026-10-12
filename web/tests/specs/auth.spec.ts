import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('unauthenticated visit to / redirects to /login', async ({ page, api }) => {
  api.seed({ me: null });
  await page.goto('/');
  await expect(page).toHaveURL(/\/login/);
  await expect(page.getByRole('link', { name: /Continue with Microsoft/i })).toBeVisible();
});

test('authenticated visit to /login redirects to /', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/login');
  await expect(page).toHaveURL('/');
});

test('auth_error query param shows error alert', async ({ page, api }) => {
  api.seed({ me: null });
  await page.goto('/login?auth_error=1');
  await expect(page.getByText(/Authentication failed/i)).toBeVisible();
});

// #309: a signed-out load asked GetMe twice — the password-change probe re-ran
// it after the 401 — and logged two console 401s.
// (A shell page's redirect to /login is a full page load of its own, with its
// own single GetMe.)
test('a signed-out page load asks GetMe once', async ({ page, api }) => {
  api.seed({ me: null });
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: /sign in to shepherd/i })).toBeVisible();
  await expect(page.getByRole('link', { name: /Continue with Microsoft/i })).toBeVisible();
  expect(api.calls('MeService/GetMe')).toHaveLength(1);
});
