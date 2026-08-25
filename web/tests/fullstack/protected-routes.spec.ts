/**
 * Fullstack: protected-routes matrix (V4-16b)
 * Every protected route redirects unauthenticated users to /login.
 * The completeness guard fails if any route lacks a tag.
 */
import { readFileSync } from 'node:fs';
import { routeManifest } from '../../src/routes/routeManifest';
import { expect, test } from './fixtures';

// Completeness, properly.
//
// This used to check only that every listed route had a `tag` -- which the
// RouteEntry type already guarantees, so it could never fail. Meanwhile /git
// and the three canvas routes were absent from the manifest entirely and were
// therefore never tested for the login redirect. A guard that cannot fail is
// worse than none: it reads as coverage.
//
// Parsing router.tsx for `path:` literals is crude, but it is the only source
// that cannot drift from the routes the app actually serves.
const routerSource = readFileSync(new URL('../../src/routes/router.tsx', import.meta.url), 'utf8');
const declaredPaths = [...routerSource.matchAll(/path:\s*'([^']+)'/g)].map((m) => m[1]);
const manifestPaths = new Set(routeManifest.map((r) => r.path));
const missing = declaredPaths.filter((p) => !manifestPaths.has(p));
if (missing.length > 0) {
  throw new Error(
    `routeManifest.ts is missing ${missing.length} route(s) the router declares: ${missing.join(', ')}. ` +
      'Untagged routes are never checked for the login redirect.',
  );
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
