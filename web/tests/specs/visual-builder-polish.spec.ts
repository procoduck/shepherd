/**
 * #251 — visual builder polish (split from the 2026-10-01 walkthrough, #212).
 *
 *  - The toolbar stays one row from 1600px down to 1024px: the pipeline id is
 *    truncated (full id in its tooltip, one-click copy), the matchers keep a
 *    usable, scrollable strip, and the secondary actions collapse into an
 *    overflow menu as the row narrows — every action stays reachable.
 *  - The minimap never covers a node after the initial fit.
 *  - The bottom drawer's tabs are real tabs (role=tab, aria-selected) with a
 *    visible selected state.
 *  - Saving from the builder, and finishing a wizard, land in the text editor
 *    with a banner saying where you are and why.
 */
import { expect, type Locator, type Page } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { waitForViewportSettled } from '../fixtures/canvas';
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor, reader } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';
import { toolbarAction } from '../fixtures/toolbar';

// A real pipeline id is a UUID — the walkthrough's three-line wrap came from one.
const PIPELINE_ID = '3f6c1a2e-8b4d-4c1e-9a7f-2d5e6b8c9a01';
const MATCHERS = ['cluster="prod-eu-west-1"', 'namespace=~"payments-.*"', 'team="observability"'];

const node = (id: string, component: string, x: number, y: number) => ({
  id,
  component,
  label: id,
  position: { x, y },
  props: {},
  disabled: false,
  notes: '',
});

function graphOf(nodes: ReturnType<typeof node>[]) {
  return {
    kind: 'alloy-graph/v1',
    schema_version: currentSchemaVersion(schemaFixture),
    nodes,
    edges: [],
    bindings: [],
    viewport: { x: 0, y: 0, zoom: 1 },
    meta: { created_with: 'shepherd-visual-builder' },
  };
}

const twoNodes = graphOf([
  node('n1', 'discovery.kubernetes', 0, 0),
  node('n2', 'discovery.relabel', 400, 0),
]);

const reEscape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

type SeedApi = { seed: (partial: Record<string, unknown>) => void };

async function openBuilder(page: Page, api: SeedApi, graph = twoNodes) {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    schema: schemaFixture,
    pipelines: [
      {
        ...pipeline({ id: PIPELINE_ID, name: 'payments-scrape', source: 'visual' }),
        matchers: MATCHERS,
        wizard_state: graph,
      },
    ],
  });
  await page.goto(`/pipelines/${PIPELINE_ID}/visual`);
  await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
  await expect(page.locator('.react-flow__node')).toHaveCount(graph.nodes.length);
  await waitForViewportSettled(page);
}

async function expectInside(inner: Locator, outer: Locator, what: string) {
  const a = await inner.boundingBox();
  const b = await outer.boundingBox();
  expect(a, `${what} has a box`).not.toBeNull();
  expect(b).not.toBeNull();
  if (!a || !b) return;
  expect(a.x, `${what} starts inside its container`).toBeGreaterThanOrEqual(b.x - 0.5);
  expect(a.y, `${what} is not pushed below its container`).toBeGreaterThanOrEqual(b.y - 0.5);
  expect(a.x + a.width, `${what} ends inside its container`).toBeLessThanOrEqual(
    b.x + b.width + 0.5,
  );
  expect(a.y + a.height, `${what} is not clipped by its container`).toBeLessThanOrEqual(
    b.y + b.height + 0.5,
  );
}

// 1600: the full, all-inline row. 1440 and 1280 (the suite's default
// Desktop Chrome width): Flow check and History in "More". 1024: Simulate too.
for (const viewport of [
  { width: 1600, height: 900 },
  { width: 1440, height: 900 },
  { width: 1280, height: 720 },
  { width: 1024, height: 768 },
]) {
  test.describe(`toolbar at ${viewport.width}px`, () => {
    test.use({ viewport });

    test('stays one row: id on one line, matchers usable, every action reachable', async ({
      page,
      api,
    }) => {
      await api.loginAs(orgEditor);
      await openBuilder(page, api);
      const toolbar = page.getByTestId('toolbar');

      // Flow check on — its result text is what used to push the row over.
      await (await toolbarAction(page, 'flow-check-toggle')).click();
      await expect(page.getByTestId('flow-check-result')).toBeVisible();
      await page.keyboard.press('Escape');

      // One row: nothing scrolls inside the toolbar, and the bar is its own height.
      const overflow = await toolbar.evaluate((el) => el.scrollWidth - el.clientWidth);
      expect(overflow, 'toolbar content wider than the toolbar').toBeLessThanOrEqual(0);
      const bar = await toolbar.boundingBox();
      expect(bar?.height ?? 0).toBeLessThanOrEqual(45);

      // The id is a single truncated line, with the full id one hover or click away.
      const id = toolbar.getByText(PIPELINE_ID, { exact: true });
      await expect(id).toBeVisible();
      const idBox = await id.boundingBox();
      expect(idBox?.height ?? 0, 'pipeline id wraps').toBeLessThanOrEqual(20);
      await expect(page.getByTestId('toolbar-pipeline-id')).toHaveAttribute(
        'title',
        new RegExp(PIPELINE_ID),
      );

      for (const testId of [
        'toolbar-name',
        'toolbar-pipeline-id',
        'toolbar-id-copy',
        'matcher-input',
        'toolbar-validity',
        'flow-check-result',
        'toolbar-save',
      ]) {
        const el = page.getByTestId(testId);
        await expect(el, testId).toBeVisible();
        await expectInside(el, toolbar, testId);
      }

      // The matchers strip has real room, and anything past it scrolls rather
      // than being clipped away.
      const strip = page.getByTestId('toolbar-matcher-chips');
      const { clientWidth, scrollWidth, overflowX } = await strip.evaluate((el) => ({
        clientWidth: el.clientWidth,
        scrollWidth: el.scrollWidth,
        overflowX: getComputedStyle(el).overflowX,
      }));
      expect(clientWidth, 'matcher strip width').toBeGreaterThanOrEqual(120);
      if (scrollWidth > clientWidth) {
        expect(['auto', 'scroll']).toContain(overflowX);
        // ...and the row says there is more than shows, naming every matcher.
        const more = page.getByTestId('toolbar-matchers-more');
        await expect(more).toBeVisible();
        for (const m of MATCHERS)
          await expect(more).toHaveAttribute('title', new RegExp(reEscape(m)));
      }
      await expect(page.getByTestId('matcher-chip')).toHaveCount(MATCHERS.length);
      const last = page.getByTestId('matcher-chip').last();
      await last.scrollIntoViewIfNeeded();
      await expectInside(last, strip, 'last matcher chip');

      // Every secondary action is still reachable, inline or in the overflow menu.
      await (await toolbarAction(page, 'toolbar-history')).click();
      await expect(page.getByTestId('revision-compare')).toBeVisible();
    });

    test('copy button puts the full pipeline id on the clipboard', async ({
      page,
      api,
      context,
    }) => {
      await context.grantPermissions(['clipboard-read', 'clipboard-write']);
      await api.loginAs(orgEditor);
      await openBuilder(page, api);
      await page.getByTestId('toolbar-id-copy').click();
      await expect
        .poll(() => page.evaluate(() => navigator.clipboard.readText()))
        .toBe(PIPELINE_ID);
    });
  });
}

test.describe('toolbar overflow menu at 1280px', () => {
  test.use({ viewport: { width: 1280, height: 720 } });

  test('holds Flow check and History; Simulate stays on the row', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    await openBuilder(page, api);
    await expect(page.getByTestId('simulate-menu-trigger')).toBeVisible();
    await page.getByTestId('toolbar-more').click();
    const menu = page.getByRole('menu');
    await expect(menu.getByRole('menuitemcheckbox', { name: /flow check/i })).toHaveAttribute(
      'aria-checked',
      'false',
    );
    await menu.getByRole('menuitemcheckbox', { name: /flow check/i }).click();
    await expect(menu).toHaveCount(0);
    await expect(page.getByTestId('flow-check-active')).toBeAttached();
    await page.getByTestId('toolbar-more').click();
    await expect(page.getByRole('menuitemcheckbox', { name: /flow check/i })).toHaveAttribute(
      'aria-checked',
      'true',
    );
    await expect(page.getByRole('menuitem', { name: /sandbox run/i })).toHaveCount(0);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('menu')).toHaveCount(0);
    await expect(page.getByTestId('toolbar-more')).toBeFocused();
  });
});

test.describe('toolbar overflow menu at 1024px', () => {
  test.use({ viewport: { width: 1024, height: 768 } });

  test('collapses the secondary actions; the sandbox run starts from it', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    await openBuilder(page, api);
    const more = page.getByTestId('toolbar-more');
    await expect(more).toBeVisible();
    await expect(more).toHaveAttribute('aria-expanded', 'false');
    await more.click();
    await expect(more).toHaveAttribute('aria-expanded', 'true');
    const menu = page.getByRole('menu');
    await expect(menu.getByRole('menuitemcheckbox', { name: /flow check/i })).toBeVisible();
    await expect(menu.getByRole('menuitem', { name: /history/i })).toBeVisible();
    await menu.getByRole('menuitem', { name: /sandbox run/i }).click();
    await expect(page.getByTestId('sandbox-run-dialog')).toBeVisible();
  });

  test("a viewer's overflow menu keeps the sandbox run disabled with the reason", async ({
    page,
    api,
  }) => {
    await api.loginAs(reader);
    await openBuilder(page, api);
    await expect(page.getByTestId('toolbar-read-only')).toBeVisible();
    await expect(page.getByTestId('toolbar-save')).toBeDisabled();
    await page.getByTestId('toolbar-more').click();
    const run = page.getByRole('menuitem', { name: /sandbox run/i });
    await expect(run).toBeDisabled();
    await expect(run).toHaveAttribute('title', /viewers can't change pipelines/i);
  });
});

test.describe('minimap', () => {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 1024, height: 768 },
  ]) {
    test(`does not cover any node after the initial fit at ${viewport.width}px`, async ({
      page,
      api,
    }) => {
      await page.setViewportSize(viewport);
      await api.loginAs(orgEditor);
      // Laid out on the builder's own click-placement grid (300 x 150 flow
      // units, CanvasPane's PLACE_COL_W / PLACE_ROW_H): the fit centres it, and
      // the bottom-left node lands in the corner the minimap sits in.
      await openBuilder(
        page,
        api,
        graphOf([
          node('a', 'discovery.kubernetes', 0, 0),
          node('b', 'discovery.relabel', 300, 0),
          node('c', 'prometheus.scrape', 0, 150),
          node('d', 'prometheus.remote_write', 300, 150),
          node('e', 'discovery.kubernetes', 0, 300),
          node('f', 'discovery.relabel', 300, 300),
          node('g', 'prometheus.scrape', 0, 450),
        ]),
      );
      const minimap = await page.locator('.react-flow__minimap').boundingBox();
      expect(minimap).not.toBeNull();
      if (!minimap) return;
      const nodes = page.locator('.react-flow__node');
      for (let i = 0; i < (await nodes.count()); i++) {
        const b = await nodes.nth(i).boundingBox();
        if (!b) continue;
        const overlaps =
          b.x < minimap.x + minimap.width &&
          b.x + b.width > minimap.x &&
          b.y < minimap.y + minimap.height &&
          b.y + b.height > minimap.y;
        expect(overlaps, `node ${i} is under the minimap`).toBe(false);
      }
    });
  }
});

test.describe('bottom drawer tabs', () => {
  test('are a tablist with one selected tab and a labelled panel', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    await openBuilder(page, api);
    const drawer = page.getByTestId('bottom-drawer');
    const tablist = drawer.getByRole('tablist');
    await expect(tablist).toBeVisible();
    const problems = page.getByTestId('drawer-tab-problems');
    const code = page.getByTestId('drawer-tab-code');
    const simulate = page.getByTestId('drawer-tab-simulate');
    for (const t of [problems, code, simulate]) await expect(t).toHaveAttribute('role', 'tab');

    await code.click();
    await expect(code).toHaveAttribute('aria-selected', 'true');
    await expect(problems).toHaveAttribute('aria-selected', 'false');
    await expect(simulate).toHaveAttribute('aria-selected', 'false');
    const panel = drawer.getByRole('tabpanel');
    await expect(panel).toBeVisible();
    await expect(panel).toHaveAttribute('aria-labelledby', (await code.getAttribute('id')) ?? '');
    await expect(panel.getByTestId('code-tab-content')).toBeVisible();

    // The selected tab looks selected: it differs visibly from its siblings.
    const look = (l: Locator) =>
      l.evaluate((el) => {
        const s = getComputedStyle(el);
        return `${s.borderBottomColor}|${s.borderBottomWidth}|${s.color}|${s.backgroundColor}`;
      });
    expect(await look(code)).not.toBe(await look(simulate));

    // Arrow keys move between tabs, as a tablist should.
    await code.focus();
    await page.keyboard.press('ArrowRight');
    await expect(simulate).toHaveAttribute('aria-selected', 'true');
    await expect(simulate).toBeFocused();

    // The simulation sub-tabs are tabs too.
    const relabel = page.getByTestId('simulate-relabel-tab');
    const logs = page.getByTestId('simulate-logs-tab');
    await expect(relabel).toHaveAttribute('role', 'tab');
    await logs.click();
    await expect(logs).toHaveAttribute('aria-selected', 'true');
    await expect(relabel).toHaveAttribute('aria-selected', 'false');
  });
});

test.describe('where Save and the wizard land', () => {
  test('saving from the builder says the editor shows the generated config, with a way back', async ({
    page,
    api,
  }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
    });
    await page.goto('/pipelines/visual/new');
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await page.getByTestId('toolbar-name').fill('from-the-builder');
    await page.getByTestId('matcher-input').fill('cluster="prod"');
    await page.getByTestId('matcher-input').press('Enter');
    await page.getByTestId('toolbar-save').click();

    await expect(page).toHaveURL(/\/pipelines\/[^/]+$/);
    const banner = page.getByTestId('editor-landing-banner');
    await expect(banner).toBeVisible();
    await expect(banner).toContainText(/saved from the visual builder/i);
    await expect(banner).toContainText(/text editor/i);
    const back = banner.getByTestId('editor-landing-open-visual');
    await back.click();
    await expect(page).toHaveURL(/\/pipelines\/[^/]+\/visual$/);
    await expect(page.getByTestId('visual-builder')).toBeVisible();
  });

  test('a visual pipeline opened directly in the editor offers the builder, with no banner', async ({
    page,
    api,
  }) => {
    await api.loginAs(reader);
    await openBuilder(page, api);
    await page.goto(`/pipelines/${PIPELINE_ID}`);
    await expect(page.getByTestId('editor-open-visual')).toBeVisible();
    await expect(page.getByTestId('editor-landing-banner')).toHaveCount(0);
  });
});
