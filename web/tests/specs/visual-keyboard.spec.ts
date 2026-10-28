/**
 * #426: everything the mouse does on the canvas, from the keyboard —
 * useCanvasKeyboard.ts's model, documented in canvasA11y.ts's CANVAS_SHORTCUTS.
 * Each spec drives the page with key presses only (no click on the canvas),
 * and asserts the document through the rendered wires and the live region.
 */
import { expect, type Page } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { waitForViewportSettled } from '../fixtures/canvas';
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const node = (id: string, component: string, label: string, x: number, y: number) => ({
  id,
  component,
  label,
  position: { x, y },
  props: {},
  disabled: false,
  notes: '',
});

// discovery → scrape is wired; scrape → remote_write is not, so a spec can
// draw it from the keyboard.
const graph = {
  kind: 'alloy-graph/v1',
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    node('n1', 'discovery.kubernetes', 'k8s', 0, 0),
    node('n2', 'prometheus.scrape', 'app', 320, 0),
    node('n3', 'prometheus.remote_write', 'sink', 640, 0),
    node('n4', 'loki.write', 'logs', 640, 240),
  ],
  edges: [{ id: 'e1', from: { node: 'n1', port: 'targets' }, to: { node: 'n2', port: 'targets' } }],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-visual-builder' },
};

const focused = (page: Page) => page.locator('*:focus');
const announcer = (page: Page) => page.getByTestId('canvas-announcer');

test.describe('canvas keyboard (#426)', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [
        { ...pipeline({ id: 'pip-kb', name: 'kb', source: 'visual' }), wizard_state: graph },
      ],
    });
    await page.goto('/pipelines/pip-kb/visual');
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('.react-flow__node')).toHaveCount(4);
    await waitForViewportSettled(page);
    await page.getByTestId('canvas').focus();
  });

  test('navigates node to node with the arrow keys; Space selects', async ({ page }) => {
    await page.keyboard.press('ArrowRight');
    // An unwired discovery reports its L1 problem in its name too.
    await expect(focused(page)).toHaveAccessibleName(
      /^discovery\.kubernetes "k8s", 0 inputs, 1 output, \d+ errors?$/,
    );
    await page.keyboard.press('ArrowRight');
    await expect(focused(page)).toHaveAccessibleName(
      /^prometheus\.scrape "app", 1 input, 1 output/,
    );
    // Space selects it: the inspector shows the scrape.
    await page.keyboard.press(' ');
    await expect(page.getByTestId('inspector-component')).toHaveText('prometheus.scrape');
    await page.keyboard.press('ArrowRight');
    await expect(focused(page)).toHaveAttribute('data-id', 'n3');
    await page.keyboard.press('ArrowDown');
    await expect(focused(page)).toHaveAttribute('data-id', 'n4');
    await page.keyboard.press('Home');
    await expect(focused(page)).toHaveAttribute('data-id', 'n1');
    await page.keyboard.press('End');
    await expect(focused(page)).toHaveAttribute('data-id', 'n4');
    await page.keyboard.press('ArrowRight');
    await expect(announcer(page)).toContainText('No node to the right');
  });

  test('creates a wire from the keyboard, skipping incompatible ports', async ({ page }) => {
    const wires = page.locator('.react-flow__edge');
    await expect(wires).toHaveCount(1);
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await expect(focused(page)).toHaveAttribute('data-id', 'n2');

    await page.keyboard.press('Enter');
    await expect(focused(page)).toHaveAccessibleName('targets, input, targets, 1 wire');
    await page.keyboard.press('ArrowDown');
    await expect(focused(page)).toHaveAccessibleName('forward_to, output, prom.metrics, 0 wires');

    await page.keyboard.press('Enter');
    // Only remote_write's receiver takes prom.metrics: loki.write's receiver
    // (loki.logs) is skipped, so the only stop is the sink.
    await expect(announcer(page)).toContainText('1 compatible port');
    await expect(focused(page)).toHaveAccessibleName('receiver, input, prom.metrics, 0 wires');
    await expect(focused(page)).toHaveAttribute('data-nodeid', 'n3');
    await page.keyboard.press('ArrowDown');
    await expect(focused(page)).toHaveAttribute('data-nodeid', 'n3');

    await page.keyboard.press('Enter');
    await expect(wires).toHaveCount(2);
    await expect(announcer(page)).toContainText(
      'Connected forward_to on prometheus.scrape "app" to receiver on prometheus.remote_write "sink"',
    );
    await expect(
      page.getByRole('group', {
        name: 'Wire from prometheus.scrape "app" forward_to to prometheus.remote_write "sink" receiver',
      }),
    ).toHaveCount(1);
    await expect(focused(page)).toHaveAccessibleName('receiver, input, prom.metrics, 1 wire');

    // One undo step, like the mouse gesture.
    await page.keyboard.press('Escape');
    await page.keyboard.press('Control+Z');
    await expect(wires).toHaveCount(1);
  });

  test('Escape cancels a wire in progress', async ({ page }) => {
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('Enter');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
    await expect(focused(page)).toHaveAttribute('data-nodeid', 'n3');
    await page.keyboard.press('Escape');
    await expect(announcer(page)).toContainText('Wiring cancelled');
    await expect(focused(page)).toHaveAccessibleName('forward_to, output, prom.metrics, 0 wires');
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);
  });

  test('deletes a wire from its focused port, and a focused wire', async ({ page }) => {
    const wires = page.locator('.react-flow__edge');
    // From the port: the scrape's targets input holds the one wire.
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('ArrowRight');
    await page.keyboard.press('Enter');
    await expect(focused(page)).toHaveAccessibleName('targets, input, targets, 1 wire');
    await page.keyboard.press('Delete');
    await expect(wires).toHaveCount(0);
    await expect(announcer(page)).toContainText('Deleted 1 wire on targets');
    await page.keyboard.press('Control+Z');
    await expect(wires).toHaveCount(1);

    // A wire is a Tab stop of its own; Delete removes it.
    await page.getByTestId('canvas').focus();
    let onWire = false;
    for (let i = 0; i < 12 && !onWire; i++) {
      await page.keyboard.press('Tab');
      onWire = (await focused(page).getAttribute('aria-roledescription')) === 'edge';
    }
    expect(onWire).toBe(true);
    await expect(focused(page)).toHaveAccessibleName(
      'Wire from discovery.kubernetes "k8s" targets to prometheus.scrape "app" targets',
    );
    await page.keyboard.press('Backspace');
    await expect(wires).toHaveCount(0);
    await expect(announcer(page)).toContainText('Deleted wire from discovery.kubernetes "k8s"');
  });

  test('moves and deletes the focused node', async ({ page }) => {
    await page.keyboard.press('End');
    await expect(focused(page)).toHaveAttribute('data-id', 'n4');
    await page.keyboard.press('Shift+ArrowDown');
    await expect(announcer(page)).toContainText('Moved to 640, 248');
    await page.keyboard.press('Delete');
    await expect(page.locator('.react-flow__node')).toHaveCount(3);
    await expect(announcer(page)).toContainText('Deleted loki.write "logs"');
  });

  test('shows a visible focus ring in the focus token, in both themes', async ({ page }) => {
    const setTheme = (theme: 'dark' | 'light') =>
      page.evaluate((t) => {
        document.documentElement.classList.remove('dark', 'light');
        document.documentElement.classList.add(t);
      }, theme);
    await setTheme('dark');
    await page.keyboard.press('ArrowRight');
    const ring = () =>
      focused(page).evaluate((el) => {
        const s = getComputedStyle(el);
        return { style: s.outlineStyle, width: s.outlineWidth, color: s.outlineColor };
      });
    // Dark (#818cf8) and light (#4338ca): the --color-focus token, which
    // theme.test.ts holds to 3:1 against every canvas surface.
    expect(await ring()).toEqual({ style: 'solid', width: '2px', color: 'rgb(129, 140, 248)' });
    await setTheme('light');
    expect(await ring()).toEqual({ style: 'solid', width: '2px', color: 'rgb(67, 56, 202)' });

    await page.keyboard.press('Enter');
    expect((await ring()).style).toBe('solid');
  });

  test('? opens the shortcut sheet; the canvas button does too', async ({ page }) => {
    await page.keyboard.press('?');
    const sheet = page.getByRole('dialog', { name: 'Canvas keyboard shortcuts' });
    await expect(sheet).toBeVisible();
    await expect(sheet.getByText('Start a wire from the focused port')).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(sheet).toHaveCount(0);
    await page.getByRole('button', { name: 'Keyboard shortcuts' }).click();
    await expect(sheet).toBeVisible();
  });
});
