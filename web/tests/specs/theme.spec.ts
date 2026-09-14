import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { expect, test } from '../fixtures/test';

// D8 (light mode, W6-S1). Contract: with no stored choice,
// prefers-color-scheme decides (pure CSS, no login/JS needed — exercised on
// the unauthenticated /login route below); the toggle writes an explicit
// "light"|"dark" to localStorage['theme'] and sets html.light/html.dark,
// which overrides the system preference and survives a reload.

async function bodyBackground(page: import('@playwright/test').Page): Promise<string> {
  return page.evaluate(() => getComputedStyle(document.body).backgroundColor);
}

test.describe('system preference decides with no stored choice', () => {
  test('emulated light and dark color schemes render different body backgrounds', async ({
    page,
    api,
  }) => {
    // No loginAs(): default mocked state has me:null, so /login renders with
    // no Shell (and no JS theme logic) involved — this is pure index.css.
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/login');
    await expect(page.getByRole('heading', { name: /sign in to shepherd/i })).toBeVisible();
    const darkBg = await bodyBackground(page);

    await page.emulateMedia({ colorScheme: 'light' });
    // Force a fresh evaluation of the media query: a navigation is not
    // required for emulateMedia to take effect, but re-asserting visibility
    // proves the page is still alive.
    await expect(page.getByRole('heading', { name: /sign in to shepherd/i })).toBeVisible();
    const lightBg = await bodyBackground(page);

    expect(lightBg).not.toEqual(darkBg);
    // Nothing was ever stored — the switch is driven purely by the media
    // query, not by any leftover localStorage state from a prior test.
    const stored = await page.evaluate(() => localStorage.getItem('theme'));
    expect(stored).toBeNull();
  });
});

test.describe('explicit toggle overrides the system preference and persists', () => {
  test('clicking the toggle sets an explicit theme that survives reload and beats the OS preference', async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org] });

    // Start dark (matches playwright.config.ts's project default), with no
    // stored choice yet.
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/');
    const html = page.locator('html');
    await expect(html).not.toHaveClass(/light/);
    const beforeBg = await bodyBackground(page);

    const themeBtn = page.getByRole('button', { name: /toggle theme/i });
    await expect(themeBtn).toBeVisible();
    await themeBtn.click();

    await expect(html).toHaveClass(/light/);
    await expect(html).not.toHaveClass(/dark/);
    const afterBg = await bodyBackground(page);
    expect(afterBg).not.toEqual(beforeBg);
    expect(await page.evaluate(() => localStorage.getItem('theme'))).toBe('light');

    // Reload with the OS still reporting dark: the explicit 'light' choice
    // must win over prefers-color-scheme, and survive the reload.
    await page.reload();
    await expect(html).toHaveClass(/light/);
    await expect(html).not.toHaveClass(/dark/);
    expect(await bodyBackground(page)).toEqual(afterBg);
  });
});

// W5/W6-D: PipelineNode and CanvasPane read wire/category colours through
// schemaAdapter's theme-aware getThemedWireColor/getThemedCategoryColor
// (added by W6-S1) instead of the dark-only getWireColor/getCategoryColor —
// so the canvas itself, not just chrome around it, follows the toggle.
test.describe('visual canvas colours follow the theme', () => {
  test("a placed node's port wire colour changes with the theme toggle", async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });

    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });

    // prometheus.scrape's `targets` port (a data-kind argument, D1's one
    // "accepts" exception — see PipelineNode's own comment) uses the
    // 'targets' wire type, which carries a distinct overlay `color_light`
    // (internal/schema/artifacts/overlay.json) — the port handle's colour is
    // set unconditionally (unlike the node's category-colour left border,
    // which a fresh unconnected node's own required-argument errors
    // override with a fixed red), so it's the reliable signal here.
    await page.click('[data-component="prometheus.scrape"]');
    const node = page.locator('[data-testid="pipeline-node"]').first();
    await expect(node).toBeVisible();
    const handle = node.locator('.react-flow__handle').first();
    await expect(handle).toBeVisible();

    const darkHandle = await handle.evaluate((el) => getComputedStyle(el).backgroundColor);

    const themeBtn = page.getByRole('button', { name: /toggle theme/i });
    await themeBtn.click();
    await expect(page.locator('html')).toHaveClass(/light/);

    // The toggle only changes Shell's own local state — this node is
    // `memo`-wrapped and receives no new props from that, so the colour
    // functions themselves being theme-aware isn't enough on its own; wait
    // for the value to actually change rather than reading once.
    await expect
      .poll(async () => handle.evaluate((el) => getComputedStyle(el).backgroundColor))
      .not.toBe(darkHandle);
  });
});

// WCAG relative luminance (sRGB -> linear -> the standard 0.2126/0.7152/0.0722
// weights), applied to a `getComputedStyle(...).color`-style "rgb(r, g, b)"
// string. Used below to assert that light-mode text is actually DARK, not
// merely "a different shade of near-white" — a color could change and still
// fail contrast.
function relativeLuminance(rgb: string): number {
  const m = rgb.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
  if (!m) throw new Error(`unparseable color: "${rgb}"`);
  const [r, g, b] = [m[1], m[2], m[3]].map((c) => {
    const s = Number(c) / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

// F2 (2026-09-14 walkthrough): the app's only base foreground is the raw
// `text-zinc-100` utility on <body> (index.html) and the Shell root, and
// index.css's light overrides redefined only surface/muted tokens — so
// every label that inherits colour (palette names, node titles, the
// toolbar name field, dialog titles) stayed near-white on the light
// background. This spec proves the builder's inherited text actually
// flips, not just that *some* colour value differs.
test.describe('visual builder text follows the theme', () => {
  test('palette, node title, toolbar and sandbox dialog text are dark in light mode', async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });

    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });

    await page.click('[data-component="prometheus.scrape"]');
    const node = page.locator('[data-testid="pipeline-node"]').first();
    await expect(node).toBeVisible();

    const paletteName = page.locator(
      '[data-testid="palette-item-prometheus.scrape"] span.font-mono',
    );
    const nodeTitle = node.locator('.font-mono span');
    const toolbarName = page.getByTestId('toolbar-name');

    await page.getByTestId('simulate-menu-trigger').click();
    await page.getByTestId('simulate-menu-sandbox-run').click();
    const dialogTitle = page.locator('[data-testid="sandbox-run-dialog"] h2');
    await expect(dialogTitle).toBeVisible();

    const darkPalette = await paletteName.evaluate((el) => getComputedStyle(el).color);
    const darkNodeTitle = await nodeTitle.evaluate((el) => getComputedStyle(el).color);
    const darkToolbarName = await toolbarName.evaluate((el) => getComputedStyle(el).color);
    const darkDialogTitle = await dialogTitle.evaluate((el) => getComputedStyle(el).color);

    // Close the dialog before toggling — the Modal traps focus/keydown and
    // a stray Escape after the toggle would be ambiguous about which state
    // it landed in.
    await page.getByRole('button', { name: 'Close' }).click();
    await expect(dialogTitle).not.toBeVisible();

    const themeBtn = page.getByRole('button', { name: /toggle theme/i });
    await themeBtn.click();
    await expect(page.locator('html')).toHaveClass(/light/);

    await expect
      .poll(async () => paletteName.evaluate((el) => getComputedStyle(el).color))
      .not.toBe(darkPalette);
    await expect
      .poll(async () => nodeTitle.evaluate((el) => getComputedStyle(el).color))
      .not.toBe(darkNodeTitle);
    await expect
      .poll(async () => toolbarName.evaluate((el) => getComputedStyle(el).color))
      .not.toBe(darkToolbarName);

    const lightPalette = await paletteName.evaluate((el) => getComputedStyle(el).color);
    const lightNodeTitle = await nodeTitle.evaluate((el) => getComputedStyle(el).color);
    const lightToolbarName = await toolbarName.evaluate((el) => getComputedStyle(el).color);

    expect(relativeLuminance(lightPalette)).toBeLessThan(0.3);
    expect(relativeLuminance(lightNodeTitle)).toBeLessThan(0.3);
    expect(relativeLuminance(lightToolbarName)).toBeLessThan(0.3);

    // Re-open the sandbox dialog in light mode for the same check — its
    // title inherits colour too and never got its own light-mode assertion
    // above (it was closed before the toggle).
    await page.getByTestId('simulate-menu-trigger').click();
    await page.getByTestId('simulate-menu-sandbox-run').click();
    await expect(dialogTitle).toBeVisible();
    const lightDialogTitle = await dialogTitle.evaluate((el) => getComputedStyle(el).color);
    expect(lightDialogTitle).not.toBe(darkDialogTitle);
    expect(relativeLuminance(lightDialogTitle)).toBeLessThan(0.3);
  });
});

// F2 review finding: bg-[#131f17] -> bg-emerald-500/10 made the snapped-drop
// tint a 10%-alpha colour that REPLACES bg-card (both are background-color;
// bg-emerald-500/10 sorts after bg-card in the compiled CSS) instead of
// layering over it, so a snapped node's body — including its title and port
// rows — is ~90% transparent and the canvas dot grid shows through, in BOTH
// themes. data-drop-state alone (visual-drag-highlight.spec.ts) can't catch
// this since it never reads a computed style. This proves the snapped node's
// background stays fully opaque (alpha 1) while still visibly tinted, in
// dark mode and in light mode.
// Handles both the legacy `rgba(r, g, b, a)` comma syntax and the modern
// slash syntax any color function (oklab, oklch, color-mix's own output, …)
// serializes to — `bg-emerald-500/10` computes as
// "oklab(0.696 -0.162114 0.0511765 / 0.1)", not an rgb string. A function
// with no alpha component (fully opaque) is alpha 1.
