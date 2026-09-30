import { basicScenario } from '../fixtures/factories';
import { appAdmin, localAdmin, nobody, orgAdmin, orgEditor, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('appAdmin sees admin nav group', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByText(/Admin/i)).toBeVisible();
});

test('orgAdmin does not see admin nav group', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Orgs' })).not.toBeVisible();
});

test('appAdmin sees New pipeline button', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByRole('link', { name: /New pipeline/i })).toBeVisible();
});

test('local admin persona sees Admin nav group', async ({ page, api }) => {
  await api.loginAs(localAdmin);
  await page.goto('/');
  await expect(page.getByText('Admin')).toBeVisible();
});

// Reader negatives: write affordances are hidden, not disabled. Each test
// asserts a positive control first — a page that failed to render would
// otherwise make every absence assertion pass vacuously.
test('reader sees the pipelines list without New pipeline or the Enabled switch', async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByRole('link', { name: 'ui-enabled' })).toBeVisible();
  await expect(page.getByRole('link', { name: /New pipeline/i })).toHaveCount(0);
  await expect(page.getByRole('link', { name: /Visual builder/i })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^(Enable|Disable)$/ })).toHaveCount(0);
});

test('reader sees the pipeline editor without a Save button', async ({ page, api }) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
  await page.goto('/pipelines/pip-0001');
  await expect(page.locator('.cm-editor')).toBeVisible();
  await expect(page.getByRole('button', { name: /Save/i })).toHaveCount(0);
});

// Nav group visibility (Shell.tsx: the Admin group and its adminOnly items
// are filtered on me.isAppAdmin; org-scoped links on routeManifest's
// requiredRole in the selected org — see nav-gating.spec.ts) for every persona
// that hasn't exercised it yet. appAdmin/orgAdmin/localAdmin above cover the
// positive and one negative case; these round out the matrix.
test('orgEditor does not see admin nav group', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Orgs' })).not.toBeVisible();
});

test('reader does not see admin nav group', async ({ page, api }) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Orgs' })).not.toBeVisible();
});

test('nobody does not see admin nav group', async ({ page, api }) => {
  // nobody belongs to zero orgs — the overview page's queries are all
  // enabled: !!orgId, so it renders the zero-state stats, not a crash.
  await api.loginAs(nobody);
  api.seed({});
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Orgs' })).not.toBeVisible();
});

// Every persona still sees the non-admin nav groups — only the Admin group
// (and its adminOnly rows) is gated. A page that failed to render would make
// the negative nav assertions above pass vacuously; this is the positive
// control for each of them.
test('orgEditor sees the Fleet, Delivery and Access nav groups', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Collectors' })).toBeVisible();
  // Git is org-admin, so an editor's Delivery group shows Destinations instead
  // (nav-gating.spec.ts covers the hidden links).
  await expect(page.getByRole('link', { name: 'Destinations' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Teams' })).toBeVisible();
});

test('reader sees the Fleet, Delivery and Access nav groups', async ({ page, api }) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Collectors' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Destinations' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Teams' })).toBeVisible();
});

test('orgAdmin sees the Fleet, Delivery and Access nav groups', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/');
  await expect(page.getByRole('link', { name: 'Collectors' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Git' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Teams' })).toBeVisible();
});

// TeamsPage.canManage is org role "admin" (or isAppAdmin) alone — editor and
// reader authorship stops short of who the team even is.
test('orgAdmin can manage teams', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/teams');
  await expect(page.getByTestId('team-new')).toBeVisible();
});

test('orgEditor cannot manage teams', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/teams');
  await expect(page.getByRole('heading', { name: 'Teams' })).toBeVisible();
  await expect(page.getByTestId('team-new')).toHaveCount(0);
});

test('reader cannot manage teams', async ({ page, api }) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/teams');
  await expect(page.getByRole('heading', { name: 'Teams' })).toBeVisible();
  await expect(page.getByTestId('team-new')).toHaveCount(0);
});

// Org-independent admin pages (AdminOrgsPage gates purely on
// me.isAppAdmin, with no useOrgId in the mix at all) are exactly where a
// local admin — who, unlike appAdmin's fixture, carries no org membership
// at all — can be exercised through the UI. Anything org-scoped (teams,
// collectors, pipelines with an org-dependent early return) is NOT safe for
// this persona: see the finding recorded for W7-08.
test('local admin sees the New organisation button, not just an OIDC app admin', async ({
  page,
  api,
}) => {
  await api.loginAs(localAdmin);
  api.seed({ orgs: [] });
  await page.goto('/admin/orgs');
  await expect(page.getByRole('button', { name: /New organisation/i })).toBeVisible();
});

test('local admin can reach the clusters admin page', async ({ page, api }) => {
  await api.loginAs(localAdmin);
  api.seed({ orgs: [] });
  await page.goto('/admin/clusters');
  await expect(page.getByRole('heading', { name: 'Clusters' })).toBeVisible();
});

test('orgAdmin can also change where telemetry ships, unlike orgEditor', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/destinations');
  await expect(page.getByRole('button', { name: /new destination/i })).toBeVisible();
});

test('nobody sees zero orgs on the overview page', async ({ page, api }) => {
  await api.loginAs(nobody);
  api.seed({});
  await page.goto('/');
  const orgsTile = page.getByText('Orgs').locator('..');
  await expect(orgsTile).toContainText('0');
});

test('local admin can reach the users admin page', async ({ page, api }) => {
  await api.loginAs(localAdmin);
  api.seed({ orgs: [] });
  await page.goto('/admin/users');
  await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
});

test('local admin can reach the agent tokens admin page', async ({ page, api }) => {
  await api.loginAs(localAdmin);
  api.seed({ orgs: [] });
  await page.goto('/admin/tokens');
  await expect(page.getByRole('heading', { name: 'Agent Tokens' })).toBeVisible();
});

test('local admin can reach the single sign-on admin page', async ({ page, api }) => {
  await api.loginAs(localAdmin);
  api.seed({ orgs: [] });
  await page.goto('/admin/auth');
  await expect(page.getByRole('heading', { name: 'Single sign-on' })).toBeVisible();
});

test('orgEditor is denied the single sign-on admin page by direct navigation', async ({
  page,
  api,
}) => {
  // W6-S7: routeManifest's requiredRole ('app-admin' for admin/*) denies the
  // direct navigation before AdminAuthPage ever mounts, so its own
  // 'sso-forbidden' banner is unreachable now — the guard redirects to '/'
  // (and shows a route-denied element on the way) and the page's privileged
  // RPC never fires, org-independent so safe for a persona with no org
  // membership too. Distinct from the adminOnly nav-link tests above (this
  // checks the page itself, not just the link's absence from the sidebar).
  // See route-guard.spec.ts for the full persona x route denial matrix.
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/admin/auth');
  await expect(async () => {
    const onRoot = new URL(page.url()).pathname === '/';
    const hasDeniedBanner = await page
      .getByTestId('route-denied')
      .isVisible()
      .catch(() => false);
    expect(onRoot || hasDeniedBanner).toBe(true);
  }).toPass({ timeout: 5000 });
  expect(api.calls('AdminService/GetOidcSettings')).toHaveLength(0);
});

test('reader is denied the single sign-on admin page by direct navigation', async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/admin/auth');
  await expect(async () => {
    const onRoot = new URL(page.url()).pathname === '/';
    const hasDeniedBanner = await page
      .getByTestId('route-denied')
      .isVisible()
      .catch(() => false);
    expect(onRoot || hasDeniedBanner).toBe(true);
  }).toPass({ timeout: 5000 });
  expect(api.calls('AdminService/GetOidcSettings')).toHaveLength(0);
});
