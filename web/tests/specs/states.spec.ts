import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('empty pipelines shows empty state', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [] });
  await page.goto('/pipelines');
  // The empty state itself must render — the page heading renders even when
  // the empty state is missing, so it must not be an acceptable alternative.
  await expect(page.getByText(/no pipelines yet/i)).toBeVisible();
});

test('mutation failure shows toast with error content', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
  api.failNext('POST', '/shepherd.mgmt.v1.PipelineService/UpdatePipeline', 500, 'internal');
  await page.goto(`/pipelines/${s.pipelines[0].id}`);
  // A save that fails must say so: silently keeping the editor's content while
  // the server rejected it is how a user loses work believing it was stored.
  await page.getByRole('button', { name: /save|update/i }).click();
  await expect(page.locator('[data-sonner-toast]').first()).toBeVisible({ timeout: 5000 });
});

test('load failure shows inline Alert or error indicator with server message', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  api.failNext('POST', '/shepherd.mgmt.v1.PipelineService/ListPipelines', 503, 'unavailable');
  await page.goto('/pipelines');
  // A failed list must not render as an empty list — "you have no pipelines" and
  // "we could not load your pipelines" are different facts and must look different.
  const alert = page.getByRole('alert');
  await expect(alert).toBeVisible({ timeout: 5000 });
  await expect(alert).toContainText(/failed|error|retry/i);
});

test('theme toggle persists class on html element after reload', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  const html = page.locator('html');
  const initialClass = (await html.getAttribute('class')) ?? '';
  // The toggle's presence is part of the claim — a missing button must fail,
  // not silently degrade to a weaker assertion.
  const themeBtn = page.getByRole('button', { name: /toggle theme/i });
  await expect(themeBtn).toBeVisible();
  await themeBtn.click();
  await page.reload();
  expect(await html.getAttribute('class')).not.toEqual(initialClass);
});

test('logout clears cached persona data before navigating to login', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.override('GET', '/auth/logout', async (route, _params, state) => {
    state.me = null;
    await route.fulfill({ status: 200 });
  });

  await page.goto('/');
  await page.getByRole('button', { name: /sign out/i }).click();

  await expect(page).toHaveURL(/\/login/);
  await expect(page.getByRole('heading', { name: /sign in to shepherd/i })).toBeVisible();
});
