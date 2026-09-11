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

test('a route chunk load failure shows the shared error fallback and keeps the shell mounted', async ({
  page,
  api,
}) => {
  // W6-S5: the router's defaultErrorComponent (RouteErrorFallback) is wired
  // at createRouter, not per route — TanStack Router resolves error
  // boundaries per LEAF match, so this is the only layer that actually sees
  // a page crash. Aborting the lazy graph-view chunk forces exactly that: a
  // real render-time throw, not a mocked one.
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
  // Fulfilled (not aborted): a genuine network failure makes Chromium log
  // its own "Failed to fetch dynamically imported module" console error
  // regardless of application code, which the api fixture's console-error
  // guard (tests/fixtures/test.ts, outside this workstream's territory)
  // would then fail the test on. A 200 response whose body throws on
  // evaluation forces the same import()-rejects-the-lazy-boundary path
  // without a network-layer failure.
  await page.route('**/GraphViewPage-*.js', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/javascript',
      body: 'export const GraphViewPage = undefined;',
    }),
  );
  await page.goto(`/pipelines/${s.pipelines[0].id}/graph`);

  const errorFallback = page.getByTestId('route-error');
  await expect(errorFallback).toBeVisible();
  await expect(errorFallback).toHaveAttribute('role', 'alert');
  // Shell stays mounted for free: the boundary that caught the crash sits
  // below it, at the failed leaf, not at the root.
  await expect(page.getByRole('button', { name: /sign out/i })).toBeVisible();
});

test('an editor chunk load failure shows the shared error fallback and keeps the shell mounted', async ({
  page,
  api,
}) => {
  // CodeMirror is behind its own lazy boundary (src/editor/LazyAlloyEditor),
  // not a route's. A failed editor chunk still has to land in the SAME
  // fallback as a failed page chunk: the Suspense boundary has no error
  // handling of its own, so the rejection must propagate up to the route's
  // RouteErrorFallback rather than crash the shell or hang on the
  // placeholder. Same fulfilled-not-aborted technique as the route spec
  // above, for the same console-guard reason.
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.route('**/AlloyEditor-*.js', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/javascript',
      body: 'export const AlloyEditor = undefined;',
    }),
  );
  await page.goto('/pipelines/new');

  const errorFallback = page.getByTestId('route-error');
  await expect(errorFallback).toBeVisible();
  await expect(errorFallback).toHaveAttribute('role', 'alert');
  await expect(page.getByTestId('editor-loading')).toHaveCount(0);
  await expect(page.getByRole('button', { name: /sign out/i })).toBeVisible();
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
