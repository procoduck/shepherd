/**
 * Fullstack: protected-routes matrix (V4-16b)
 * Every protected route redirects unauthenticated users to /login.
 * The completeness guard fails if any route lacks a tag.
 */
import { routeManifest } from '../../src/routes/routeManifest';
import { expect, test } from './fixtures';

for (const entry of routeManifest) {
  if (!entry.tag) {
    throw new Error(`Route ${entry.path} is missing a tag in routeManifest.ts`);
  }
}

const protectedRoutes = routeManifest.filter((route) => route.tag === 'protected');
const publicRoutes = routeManifest.filter((route) => route.tag === 'public');

test.describe('protected-routes: unauthenticated access', () => {
  for (const route of protectedRoutes) {
    test(`${route.path} redirects to /login when unauthenticated`, async ({ page }) => {
      await page.goto(route.path);
      await expect(page).toHaveURL(/\/login/);
    });
  }
});

test.describe('public routes accessible unauthenticated', () => {
  for (const route of publicRoutes) {
    test(`${route.path} is accessible without auth`, async ({ page }) => {
      await page.goto(route.path);
      await expect(page).toHaveURL(new RegExp(route.path.replace('/', '\\/')));
    });
  }
});
