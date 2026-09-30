/**
 * Mocked direct-navigation denial matrix (persona x route) — the
 * behavioural proof for W6-S7's role-level route guard
 * (routeManifest.requiredRole + a RequireRole component that redirects
 * before a denied page ever renders).
 *
 * Contract (mirrors internal/mgmtapi/rpc_interceptor.go):
 *   - admin/* requires app-admin. None of orgAdmin, orgEditor, reader or
 *     nobody carry isAppAdmin, so all four are denied on every admin/*
 *     route below.
 *   - /teams requires 'org-reader' (routeManifest.ts assigns 'app-admin' to
 *     admin/*, 'org-admin' to /git and /audit, 'org-editor' to wizards and
 *     pipeline-create, and 'org-reader' to every other org-scoped route
 *     with no elevated requirement of its own, /teams included — mirroring
 *     TeamServiceListTeamsProcedure's RoleOrgReader in rpc_interceptor.go).
 *     orgAdmin, orgEditor and reader all belong to org-0001 and clear
 *     org-reader; nobody belongs to no org at all and is denied.
 * A denial is a visible [data-testid="route-denied"] page (since #206 shown
 * in place; it used to redirect to '/', which the poll below still accepts),
 * and — the part the marker alone cannot prove — the page's privileged RPC
 * for that route must never have fired, i.e. the denied page's data never
 * left the guard to render.
 *
 * This was the RED run for W6-S7 before it landed: every denial case below
 * failed with routeManifest carrying no requiredRole and no RequireRole
 * component, so every persona reached every route and its RPC fired.
 */
import type { Page } from '@playwright/test';
import { org } from '../fixtures/factories';
import { nobody, orgAdmin, orgEditor, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

const ORG = org({ id: 'org-0001' });

// Local, minimal view of the `api` fixture — just the one method this file
// needs. `ApiFixture` in fixtures/test.ts is not exported (that file is
// outside this workstream's territory), so this is declared rather than
// imported.
interface CallsFixture {
  calls: (pattern: string) => Array<{ method: string; path: string; body: unknown }>;
}

async function expectDenied(page: Page, api: CallsFixture, route: string, rpcPattern: string) {
  await page.goto(route);
  await expect(async () => {
    const onRoot = new URL(page.url()).pathname === '/';
    const hasDeniedBanner = await page
      .getByTestId('route-denied')
      .isVisible()
      .catch(() => false);
    expect(onRoot || hasDeniedBanner).toBe(true);
  }).toPass({ timeout: 5000 });
  expect(api.calls(rpcPattern)).toHaveLength(0);
}

async function expectAllowed(page: Page, route: string) {
  await expect(async () => {
    expect(new URL(page.url()).pathname).toBe(route);
  }).toPass({ timeout: 5000 });
  await expect(page.getByTestId('route-denied')).toHaveCount(0);
}

test.describe('route-guard: persona x route denial matrix', () => {
  test('orgAdmin is denied /admin/orgs', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/orgs', 'AdminService/ListOrgs');
  });

  test('orgAdmin is denied /admin/users', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/users', 'UserService/ListUsers');
  });

  test('orgAdmin is denied /admin/auth', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/auth', 'AdminService/GetOidcSettings');
  });

  test('orgAdmin is allowed /teams', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    api.seed({ orgs: [ORG] });
    await page.goto('/teams');
    await expectAllowed(page, '/teams');
  });

  test('orgEditor is denied /admin/orgs', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/orgs', 'AdminService/ListOrgs');
  });

  test('orgEditor is denied /admin/users', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/users', 'UserService/ListUsers');
  });

  test('orgEditor is denied /admin/auth', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/auth', 'AdminService/GetOidcSettings');
  });

  test('orgEditor is allowed /teams', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    api.seed({ orgs: [ORG] });
    await page.goto('/teams');
    await expectAllowed(page, '/teams');
  });

  test('reader is denied /admin/orgs', async ({ page, api }) => {
    await api.loginAs(reader);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/orgs', 'AdminService/ListOrgs');
  });

  test('reader is denied /admin/users', async ({ page, api }) => {
    await api.loginAs(reader);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/users', 'UserService/ListUsers');
  });

  test('reader is denied /admin/auth', async ({ page, api }) => {
    await api.loginAs(reader);
    api.seed({ orgs: [ORG] });
    await expectDenied(page, api, '/admin/auth', 'AdminService/GetOidcSettings');
  });

  test('reader is allowed /teams', async ({ page, api }) => {
    await api.loginAs(reader);
    api.seed({ orgs: [ORG] });
    await page.goto('/teams');
    await expectAllowed(page, '/teams');
  });

  test('nobody is denied /admin/orgs', async ({ page, api }) => {
    await api.loginAs(nobody);
    api.seed({});
    await expectDenied(page, api, '/admin/orgs', 'AdminService/ListOrgs');
  });

  test('nobody is denied /admin/users', async ({ page, api }) => {
    await api.loginAs(nobody);
    api.seed({});
    await expectDenied(page, api, '/admin/users', 'UserService/ListUsers');
  });

  test('nobody is denied /admin/auth', async ({ page, api }) => {
    await api.loginAs(nobody);
    api.seed({});
    await expectDenied(page, api, '/admin/auth', 'AdminService/GetOidcSettings');
  });

  test('nobody is denied /teams', async ({ page, api }) => {
    await api.loginAs(nobody);
    api.seed({});
    await expectDenied(page, api, '/teams', 'TeamService/ListTeams');
  });
});
