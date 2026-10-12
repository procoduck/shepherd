/**
 * #307 (2026-10-08 pre-release walkthrough, L14): at a 1024 px window
 *
 * - opening the builder's bottom drawer shrank the canvas under a viewport
 *   fitted for the taller one, and the minimap sat on top of the nodes;
 * - resizing the window never re-fitted the canvas at all;
 * - the pipeline text editor clipped long lines: nothing scrolled sideways
 *   to them and there was no way to wrap them.
 *
 * Geometry is polled (no fixed waits): a refit lands a frame or two after
 * React Flow measures the new pane size.
 */
import { expect, type Page } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { type Box, settledBox, waitForViewportSettled } from '../fixtures/canvas';
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const at = (id: string, component: string, x: number, y: number) => ({
  id,
  component,
  label: id,
  position: { x, y },
  props: {},
  disabled: false,
  notes: '',
});

// A 2x2 block of nodes, tall enough that a fit uses the pane's height.
const graph = {
  kind: 'alloy-graph/v1',
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    at('n1', 'discovery.kubernetes', 0, 0),
    at('n2', 'discovery.relabel', 400, 0),
    at('n3', 'prometheus.scrape', 0, 360),
    at('n4', 'prometheus.remote_write', 400, 360),
  ],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-visual-builder' },
};

const overlaps = (a: Box, b: Box) =>
  a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;

const inside = (a: Box, outer: Box) =>
  a.x >= outer.x - 1 &&
  a.y >= outer.y - 1 &&
  a.x + a.width <= outer.x + outer.width + 1 &&
  a.y + a.height <= outer.y + outer.height + 1;

/** Whether every node is fully on the pane and clear of the minimap. */
async function nodesClear(page: Page): Promise<boolean> {
  const pane = await page.locator('.react-flow__pane').boundingBox();
  const minimap = await page.locator('.react-flow__minimap').boundingBox();
  if (!pane || !minimap) return false;
  const boxes = await page.locator('.react-flow__node').evaluateAll((els) =>
    els.map((el) => {
      const r = el.getBoundingClientRect();
      return { x: r.x, y: r.y, width: r.width, height: r.height };
    }),
  );
  return boxes.length === 4 && boxes.every((b) => inside(b, pane) && !overlaps(b, minimap));
}

async function openBuilder(page: Page, api: { seed: (p: Record<string, unknown>) => void }) {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    schema: schemaFixture,
    pipelines: [
      { ...pipeline({ id: 'pip-lay', name: 'lay', source: 'visual' }), wizard_state: graph },
    ],
  });
  await page.goto('/pipelines/pip-lay/visual');
  await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
  await expect(page.locator('.react-flow__node')).toHaveCount(4);
  await waitForViewportSettled(page);
}

test('at 1024 px, opening the drawer refits the canvas so the minimap covers no node', async ({
  page,
  api,
}) => {
  await page.setViewportSize({ width: 1024, height: 768 });
  await api.loginAs(orgEditor);
  await openBuilder(page, api);
  await expect.poll(() => nodesClear(page), { timeout: 5_000 }).toBe(true);

  await page.getByTestId('drawer-toggle').click();
  await expect(page.getByTestId('drawer-toggle')).toHaveAttribute('aria-expanded', 'true');
  await expect.poll(() => nodesClear(page), { timeout: 5_000 }).toBe(true);
});

test('resizing the window to 1024 px refits the canvas', async ({ page, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await api.loginAs(orgEditor);
  await openBuilder(page, api);
  await expect.poll(() => nodesClear(page), { timeout: 5_000 }).toBe(true);

  await page.setViewportSize({ width: 1024, height: 640 });
  await expect.poll(() => nodesClear(page), { timeout: 5_000 }).toBe(true);
});

test('a canvas the user panned is not refitted under them', async ({ page, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await api.loginAs(orgEditor);
  await openBuilder(page, api);

  // Zoom out with the canvas's own control: a user's choice of viewport.
  await page.locator('.react-flow__controls-zoomout').click();
  await waitForViewportSettled(page);
  const chosen = await page
    .locator('.react-flow__viewport')
    .evaluate((el) => (el as HTMLElement).style.transform);

  const wide = (await page.locator('.react-flow__pane').boundingBox())!.width;
  await page.setViewportSize({ width: 1300, height: 900 });
  await expect
    .poll(async () => (await page.locator('.react-flow__pane').boundingBox())?.width)
    .toBeLessThan(wide);
  await waitForViewportSettled(page);
  const after = await page
    .locator('.react-flow__viewport')
    .evaluate((el) => (el as HTMLElement).style.transform);
  expect(after).toBe(chosen);
});

// Review of #326: React Flow's auto-pan (dragging a node to the pane's edge)
// moves the viewport with no DOM event of its own; it is still the user's
// view, and a later resize must not refit over it.
test('a canvas auto-panned by a node drag is not refitted on resize', async ({ page, api }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await api.loginAs(orgEditor);
  await openBuilder(page, api);

  const viewport = page.locator('.react-flow__viewport');
  const transform = () => viewport.evaluate((el) => (el as HTMLElement).style.transform);
  const fitted = await transform();
  const pane = await settledBox(page.locator('.react-flow__pane'));
  const node = await settledBox(page.locator('.react-flow__node[data-id="n2"]'));
  await page.mouse.move(node.x + node.width / 2, node.y + 12);
  await page.mouse.down();
  await page.mouse.move(pane.x + pane.width - 8, node.y + 12, { steps: 20 });
  // Held at the edge, React Flow keeps panning.
  await expect.poll(transform, { timeout: 5_000 }).not.toBe(fitted);
  await page.mouse.up();
  await waitForViewportSettled(page);
  const panned = await transform();

  await page.setViewportSize({ width: 1300, height: 900 });
  await expect
    .poll(async () => (await page.locator('.react-flow__pane').boundingBox())?.width)
    .toBeLessThan(pane.width);
  await waitForViewportSettled(page);
  expect(await transform()).toBe(panned);
});

const LONG_LINE = `prometheus.scrape "default" { targets = [${Array.from(
  { length: 30 },
  (_, i) => `{"__address__" = "host-${i}.example.internal:9100"}`,
).join(', ')}] }`;
// Long enough that the editor scrolls vertically too: a horizontal scrollbar
// at the bottom of the whole document, 80 lines down, is no scrollbar at all.
const CONTENTS = [LONG_LINE, ...Array.from({ length: 80 }, (_, i) => `// line ${i + 2}`)].join(
  '\n',
);

test('at 1024 px the text editor scrolls to the end of a long line, or wraps it', async ({
  page,
  api,
}) => {
  await page.setViewportSize({ width: 1024, height: 768 });
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [pipeline({ id: 'pip-long', name: 'long', org_id: s.org.id, contents: CONTENTS })],
  });
  await page.goto('/pipelines/pip-long');
  const editor = page.locator('.cm-editor');
  await expect(editor).toBeVisible();

  // The editor stays inside the window…
  const box = await editor.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x + box!.width).toBeLessThanOrEqual(1024);
  // …and its scroller reaches past the visible width, so the rest of the line
  // is a sideways scroll away instead of cut off — with the scroller (and so
  // its horizontal scrollbar) ending inside the window, not 80 lines below it.
  const scroller = page.locator('.cm-scroller');
  const scrollerBox = await scroller.boundingBox();
  expect(scrollerBox!.y + scrollerBox!.height).toBeLessThanOrEqual(768);
  const overflow = await scroller.evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(overflow).toBeGreaterThan(0);
  await scroller.evaluate((el) => {
    el.scrollLeft = el.scrollWidth;
  });
  await expect(page.getByText('host-29.example.internal:9100', { exact: false })).toBeInViewport();

  // Wrapping instead: the whole line fits the width, with no sideways scroll.
  const wrap = page.getByRole('button', { name: 'Wrap lines' });
  await expect(wrap).toHaveAttribute('aria-pressed', 'false');
  await wrap.click();
  await expect(wrap).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.cm-content')).toHaveClass(/cm-lineWrapping/);
  await expect
    .poll(() => scroller.evaluate((el) => el.scrollWidth - el.clientWidth))
    .toBeLessThanOrEqual(0);
  // The choice is remembered.
  await page.reload();
  await expect(page.getByRole('button', { name: 'Wrap lines' })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
});

// Review of #326: the editor's fill-the-container style is AlloyEditor's own,
// not the shared theme — the revision diff's MergeView keeps its own layout,
// and neither collapses.
test('the editor fills its pane and the revision diff still shows both sides', async ({
  page,
  api,
}) => {
  await page.setViewportSize({ width: 1024, height: 768 });
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const rev = (revision: number, contents: string) => ({
    revision,
    changed_by: 'a@example.com',
    changed_at: `2026-08-17T1${revision}:00:00Z`,
    change_note: revision === 1 ? 'created' : 'edited',
    contents,
    matchers: [],
    enabled: true,
  });
  api.seed({
    orgs: [s.org],
    pipelines: [
      pipeline({
        id: 'pip-diff',
        name: 'diff',
        org_id: s.org.id,
        contents: CONTENTS,
        revisions: [rev(1, '// first'), rev(2, CONTENTS)],
      }),
    ],
  });
  await page.goto('/pipelines/pip-diff');
  const editor = await settledBox(page.locator('.cm-editor'));
  // The pane below the 44px toolbar, not a sliver and not the 80-line content.
  expect(editor.height).toBeGreaterThan(300);
  expect(editor.y + editor.height).toBeLessThanOrEqual(768);

  await page.getByRole('button', { name: /revision history/i }).click();
  await page.locator('[data-testid="view-revision-btn"][data-revision="1"]').click();
  const sides = page.getByTestId('revision-diff').locator('.cm-editor');
  await expect(sides).toHaveCount(2);
  for (const side of await sides.all()) {
    expect((await settledBox(side)).height).toBeGreaterThan(20);
  }
  await expect(page.getByTestId('revision-diff')).toContainText('// first');
});
