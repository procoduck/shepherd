/**
 * #250 (split from the 2026-10-01 walkthrough, #212): navigation polish.
 *
 *  - signing in returns you to the page you asked for, for local login and
 *    for the OIDC button, and a hostile ?next= is never followed;
 *  - a user in exactly one organisation sees its name in the header;
 *  - toasts no longer cover the header's org control;
 *  - detail-page breadcrumbs name the item instead of "Pipeline".
 */
import type { Page } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { orgAdmin, orgEditor, reader } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { expect, test } from '../fixtures/test';
import type { MockState } from '../mocks/router';

function seedSignedOut(api: { seed: (partial: Partial<MockState>) => void }) {
  const s = basicScenario();
  api.seed({
    me: null,
    orgs: [s.org],
    pipelines: s.pipelines,
    collectors: s.collectors,
    authMethods: { oidc: true, local_admin: true },
    localAdminCreds: { username: 'editor', password: 'correct-pass' },
    // A local editor with one org: the persona the local form signs in as.
    localAdminPersona: orgEditor,
  });
  return s;
}

async function signInLocally(page: Page) {
  await page.getByTestId('local-username').fill('editor');
  await page.getByTestId('local-password').fill('correct-pass');
  await page.getByTestId('local-login-submit').click();
}

test.describe('return to the requested page after sign-in', () => {
  test('a signed-out deep link goes to /login with next=, and local sign-in returns there', async ({
    page,
    api,
  }) => {
    seedSignedOut(api);
    await page.goto('/pipelines/pip-0001?tab=revisions');

    await expect(page).toHaveURL(/\/login\?next=/);
    const next = new URL(page.url()).searchParams.get('next');
    expect(next).toBe('/pipelines/pip-0001?tab=revisions');

    await signInLocally(page);
    await expect(page).toHaveURL(/\/pipelines\/pip-0001\?tab=revisions$/);
    await expect(page.getByRole('navigation', { name: 'breadcrumb' })).toContainText('ui-enabled');
  });

  test('the OIDC button carries next= to /auth/login', async ({ page, api }) => {
    seedSignedOut(api);
    await page.goto('/collectors/col-0002');

    await expect(page).toHaveURL(/\/login\?next=/);
    await expect(page.getByTestId('oidc-login-btn')).toHaveAttribute(
      'href',
      `/auth/login?next=${encodeURIComponent('/collectors/col-0002')}`,
    );
  });

  test('a plain /login has no next=, and sign-in lands on /', async ({ page, api }) => {
    seedSignedOut(api);
    await page.goto('/login');
    await expect(page.getByTestId('oidc-login-btn')).toHaveAttribute('href', '/auth/login');
    await signInLocally(page);
    await expect(page).toHaveURL(/\/$/);
  });

  for (const hostile of [
    'https://evil.example/',
    '//evil.example',
    '/\\evil.example',
    '/%2F%2Fevil.example',
    'javascript:alert(1)',
  ]) {
    test(`a hostile next=${hostile} is ignored by both sign-in paths`, async ({ page, api }) => {
      seedSignedOut(api);
      await page.goto(`/login?next=${encodeURIComponent(hostile)}`);

      await expect(page.getByTestId('oidc-login-btn')).toHaveAttribute('href', '/auth/login');
      await signInLocally(page);
      await expect(page).toHaveURL(/^http:\/\/localhost:\d+\/$/);
    });
  }

  test('an already signed-in visit to /login?next= goes straight to next', async ({
    page,
    api,
  }) => {
    await api.loginAs(reader);
    const s = basicScenario();
    api.seed({ orgs: [s.org], collectors: s.collectors });
    await page.goto(`/login?next=${encodeURIComponent('/collectors')}`);
    await expect(page).toHaveURL(/\/collectors$/);
  });
});

test('a user in one organisation sees its name in the header', async ({ page, api }) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');

  // Nothing to switch, so no switcher — but the org is still named.
  await expect(page.getByTestId('org-switcher')).toHaveCount(0);
  const orgName = page.getByTestId('org-name');
  await expect(orgName).toBeVisible();
  await expect(orgName).toHaveText('Production Org');
});

test('a toast does not cover the header org control', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto(`/collectors/${s.collectors[0].id}`);
  await page.getByRole('button', { name: 'Manage labels' }).click();
  await page.getByLabel('Key', { exact: true }).fill('environment');
  await page.getByLabel('Value', { exact: true }).fill('production');
  await page.getByRole('button', { name: 'Add label', exact: true }).click();

  const toast = page.locator('[data-sonner-toast]').filter({ hasText: 'Labels saved' });
  await expect(toast).toBeVisible();
  const toastBox = await toast.boundingBox();
  const orgBox = await page.getByTestId('org-name').boundingBox();
  expect(toastBox).not.toBeNull();
  expect(orgBox).not.toBeNull();
  if (!toastBox || !orgBox) return;
  const overlaps =
    toastBox.x < orgBox.x + orgBox.width &&
    orgBox.x < toastBox.x + toastBox.width &&
    toastBox.y < orgBox.y + orgBox.height &&
    orgBox.y < toastBox.y + toastBox.height;
  expect(overlaps, 'the toast must not sit on top of the org control').toBe(false);
});

test.describe('breadcrumbs name the item', () => {
  test('a pipeline page shows the pipeline name', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({ orgs: [s.org], pipelines: s.pipelines });
    await page.goto('/pipelines/pip-0001');
    await expect(page.getByRole('navigation', { name: 'breadcrumb' })).toHaveText(
      'Pipelines / ui-enabled',
    );
  });

  test('the graph view of a pipeline shows its name too', async ({ page, api }) => {
    await api.loginAs(reader);
    const s = basicScenario();
    api.seed({ orgs: [s.org], pipelines: s.pipelines, schema: schemaFixture });
    await page.goto('/pipelines/pip-0003/graph');
    await expect(page.getByRole('navigation', { name: 'breadcrumb' })).toHaveText(
      'Pipelines / git-pipe / Graph view',
    );
  });

  test('a collector page shows the collector cluster and role', async ({ page, api }) => {
    await api.loginAs(reader);
    const s = basicScenario();
    api.seed({ orgs: [s.org], collectors: s.collectors });
    await page.goto('/collectors/col-0002');
    await expect(page.getByRole('navigation', { name: 'breadcrumb' })).toHaveText(
      'Collectors / prod-eu-1 · logs',
    );
  });

  test('a wizard page shows the wizard title', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({ orgs: [s.org] });
    await page.goto('/wizards/app-observability');
    await expect(page.getByRole('navigation', { name: 'breadcrumb' })).toHaveText(
      'Wizards / App Observability',
    );
  });
});
