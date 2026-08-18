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
