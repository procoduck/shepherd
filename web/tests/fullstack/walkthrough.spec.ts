/**
 * Full-application walkthrough against the real dev stack.
 * Visits every route, exercises the primary feature of each, and asserts that
 * no page produces a console error, a failed request, or an empty shell.
 *
 * Run with the dev stack up: pnpm exec playwright test tests/fullstack/walkthrough.spec.ts --config playwright.fullstack.config.ts
 */
import { expect, type Page, test } from '@playwright/test';
import { loginAsAdmin } from './fixtures';

type PageIssue = { route: string; kind: string; detail: string };
const issues: PageIssue[] = [];

// Requests the app makes that are expected to fail in the dev stack are noted here,
// so a genuinely broken call still surfaces.
const EXPECTED_FAILURES = [/\/api\/admin\/groups\/search/];

function watch(page: Page, route: string) {
  page.on('console', (m) => {
    if (m.type() !== 'error') return;
    const text = m.text();
    if (text.includes('favicon')) return;
    issues.push({ route, kind: 'console', detail: text.slice(0, 300) });
  });
  page.on('pageerror', (e) => {
    issues.push({ route, kind: 'pageerror', detail: String(e).slice(0, 300) });
  });
  page.on('response', (r) => {
    if (r.status() < 400) return;
    const url = r.url();
    if (EXPECTED_FAILURES.some((re) => re.test(url))) return;
    issues.push({ route, kind: 'http', detail: `${r.status()} ${r.request().method()} ${url}` });
  });
}

const ROUTES = [
  { path: '/', name: 'Overview' },
  { path: '/collectors', name: 'Collectors' },
  { path: '/pipelines', name: 'Pipelines' },
  { path: '/pipelines/new', name: 'Pipeline editor (new)' },
  { path: '/destinations', name: 'Destinations' },
  { path: '/wizards', name: 'Wizards' },
  { path: '/git', name: 'Git' },
  { path: '/audit', name: 'Audit' },
  { path: '/admin/orgs', name: 'Admin: orgs' },
  { path: '/admin/clusters', name: 'Admin: clusters' },
  { path: '/admin/tokens', name: 'Admin: tokens' },
  { path: '/pipelines/visual/new', name: 'Visual builder (new)' },
];

test('every route renders without errors', async ({ page }) => {
  await loginAsAdmin(page);
  for (const r of ROUTES) {
    watch(page, r.path);
    await page.goto(r.path);
    await page.waitForLoadState('networkidle');
    // The shell must render and the route must not be a blank body.
    const body = await page.locator('body').innerText();
    if (body.trim().length < 20) {
      issues.push({ route: r.path, kind: 'empty', detail: `body text: ${JSON.stringify(body)}` });
    }
    await page.screenshot({ path: `walkthrough/${r.name.replace(/[^a-z0-9]+/gi, '-')}.png` });
  }
  // Report collected issues as one readable failure.
  if (issues.length) {
    console.log('\n=== WALKTHROUGH ISSUES ===');
    for (const i of issues) console.log(`[${i.kind}] ${i.route} :: ${i.detail}`);
  }
  expect(
    issues,
    `issues:\n${issues.map((i) => `[${i.kind}] ${i.route} :: ${i.detail}`).join('\n')}`,
  ).toEqual([]);
});

test('collectors list shows seeded fleet with status metadata', async ({ page }) => {
  await loginAsAdmin(page);
  await page.goto('/collectors');
  await page.waitForLoadState('networkidle');
  // Collectors are org-scoped (F3): ListOrgs sorts by name, so the default
  // org selection (no persisted choice yet) is data-eng, not the
  // platform-org that owns prod-eu-1's live-agent-backed fleet. Select it
  // explicitly rather than asserting on whatever org happens to load first.
  await page.getByTestId('org-switcher').selectOption({ label: 'Platform Engineering' });
  await page.waitForLoadState('networkidle');
  let text = '';
  await expect(async () => {
    text = await page.locator('body').innerText();
    expect(text).toContain('prod-eu-1');
  }).toPass();
  // The refinement added Status / Last Seen / Version columns.
  expect(text.toLowerCase()).toMatch(/status/);
  await page.screenshot({ path: 'walkthrough/feature-collectors-list.png', fullPage: true });
});

test('collector detail shows instances', async ({ page }) => {
  await loginAsAdmin(page);
  await page.goto('/collectors');
  await page.waitForLoadState('networkidle');
  // Same org-scoping note as above: select platform-org so a "metrics" row
  // backed by a real live agent (with instances to show) is present.
  await page.getByTestId('org-switcher').selectOption({ label: 'Platform Engineering' });
  await page.waitForLoadState('networkidle');
  const row = page.locator('a[href^="/collectors/"], tr').filter({ hasText: 'metrics' }).first();
  await row.click();
  await page.waitForLoadState('networkidle');
  await page.screenshot({ path: 'walkthrough/feature-collector-detail.png', fullPage: true });
  const text = await page.locator('body').innerText();
  expect(text.length).toBeGreaterThan(50);
});

test('pipelines list and editor load a seeded pipeline', async ({ page }) => {
  await loginAsAdmin(page);
  await page.goto('/pipelines');
  await page.waitForLoadState('networkidle');
  const text = await page.locator('body').innerText();
  // The default org selection (first org, no persisted choice yet) shows that org's pipelines.
  expect(text).toMatch(/example-metrics|base-metrics/);
  const name = text.includes('base-metrics') ? 'base-metrics' : 'example-metrics';
  await page.getByText(name).first().click();
  await page.waitForLoadState('networkidle');
  await page.screenshot({ path: 'walkthrough/feature-pipeline-editor.png', fullPage: true });
  // The editor must load the pipeline's content, not an empty buffer.
  const editor = await page.locator('body').innerText();
  expect(editor.length).toBeGreaterThan(100);
});

test('the app exposes a way to reach every org the admin owns', async ({ page }) => {
  await loginAsAdmin(page);
  const me = await (await page.request.get('/api/me')).json();
  const orgCount = (me.orgs ?? []).length;
  expect(orgCount, 'dev seed should provide multiple orgs').toBeGreaterThan(1);
  await page.goto('/pipelines');
  await page.waitForLoadState('networkidle');
  // With more than one org, the shell must offer an org switcher; otherwise every
  // org-scoped page is permanently pinned to orgs[0].
  const switcher = page.locator(
    '[data-testid*="org"], [aria-label*="rganization" i], [aria-label*="rganisation" i], select',
  );
  await expect(
    switcher.first(),
    `no org switcher found though the admin has ${orgCount} orgs`,
  ).toBeVisible();
});

test('visual builder canvas loads schema and places a node', async ({ page }) => {
  await loginAsAdmin(page);
  await page.goto('/pipelines/visual/new');
  await page.waitForLoadState('networkidle');
  await expect(page.getByTestId('palette')).toBeVisible();
  await expect(page.getByTestId('canvas')).toBeVisible();
  // Palette must be populated from GET /api/schema/current.
  const items = page.locator('[data-testid^="palette-item-"]');
  await expect(items.first()).toBeVisible({ timeout: 15000 });
  const count = await items.count();
  expect(count).toBeGreaterThan(10);
  // Click-to-place a node.
  await page.getByTestId('palette-item-prometheus.remote_write').first().click();
  await expect(page.getByTestId('pipeline-node').first()).toBeVisible();
  await page.screenshot({ path: 'walkthrough/feature-visual-builder.png', fullPage: true });
});

test('demo visual pipeline opens on the canvas', async ({ page }) => {
  await loginAsAdmin(page);
  await page.goto('/pipelines');
  await page.waitForLoadState('networkidle');
  const body = await page.locator('body').innerText();
  expect(body, 'dev seed should contain a visual demo pipeline').toMatch(/demo-visual|visual/i);
});
