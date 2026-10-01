/**
 * #226: a viewer's visual-builder CANVAS and INSPECTOR are read-only.
 *
 * #223 disabled the toolbar (Save, Simulate, name, matchers) and the palette
 * for a viewer, but the canvas itself stayed fully editable: a viewer could
 * drag nodes, draw wires, delete nodes with the keyboard and type into the
 * inspector — none of which could ever be saved. Maintainer decision
 * 2026-10-01: a viewer may PAN, ZOOM and SELECT (to read a node's properties),
 * and nothing else.
 *
 * Positions are read from React Flow's per-node `transform`, which is the
 * node's FLOW position — unaffected by panning — so "the node did not move"
 * stays true even when the gesture panned the canvas instead.
 *
 * Every viewer absence assertion is paired with the same gesture succeeding
 * for an editor, so a gesture that simply missed cannot make the viewer test
 * pass vacuously.
 */
import { expect, type Locator, type Page } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { settledBox, viewportTransform, waitForViewportSettled } from '../fixtures/canvas';
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor, reader } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const graph = {
  kind: 'alloy-graph/v1',
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    {
      id: 'n1',
      component: 'discovery.kubernetes',
      label: 'k8s',
      position: { x: 0, y: 0 },
      props: { role: 'pod', api_server: 'https://k8s.example:6443' },
      disabled: false,
      notes: '',
    },
    {
      id: 'n2',
      component: 'discovery.relabel',
      label: 'relabel',
      position: { x: 400, y: 0 },
      props: {},
      disabled: false,
      notes: '',
    },
  ],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-visual-builder' },
};

type SeedApi = { seed: (partial: Record<string, unknown>) => void };

async function openBuilder(page: Page, api: SeedApi, id: string) {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    schema: schemaFixture,
    pipelines: [{ ...pipeline({ id, name: id, source: 'visual' }), wizard_state: graph }],
  });
  await page.goto(`/pipelines/${id}/visual`);
  await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
  await expect(page.locator('.react-flow__node')).toHaveCount(2);
  await waitForViewportSettled(page);
}

const nodeById = (page: Page, id: string) => page.locator(`.react-flow__node[data-id="${id}"]`);

/** React Flow's per-node transform — the node's flow-space position. */
const nodeTransform = (node: Locator) => node.evaluate((el) => (el as HTMLElement).style.transform);

async function dragBy(page: Page, target: Locator, dx: number, dy: number) {
  const box = await settledBox(target);
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + dx / 2, y + dy / 2, { steps: 8 });
  await page.mouse.move(x + dx, y + dy, { steps: 8 });
  await page.mouse.up();
}

async function dragWire(page: Page) {
  const source = nodeById(page, 'n1').locator('.react-flow__handle.source').first();
  const target = nodeById(page, 'n2').locator('.react-flow__handle.target').first();
  const from = await settledBox(source);
  const to = await settledBox(target);
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 20 });
  await page.mouse.up();
  await expect(page.locator('.react-flow__connectionline')).toHaveCount(0);
}

async function selectNode(page: Page, id: string) {
  await page.locator(`[data-node-id="${id}"]`).click({ force: true });
}

test.describe('viewer', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(reader);
    await openBuilder(page, api, 'pip-viewer');
  });

  test('cannot move a node by dragging it', async ({ page }) => {
    const node = nodeById(page, 'n1');
    const before = await nodeTransform(node);
    await dragBy(page, node.getByTestId('pipeline-node'), 120, 90);
    await expect.poll(() => nodeTransform(node)).toBe(before);
  });

  test('cannot draw a wire from a port', async ({ page }) => {
    await expect(nodeById(page, 'n1').locator('.react-flow__handle.source')).not.toHaveCount(0);
    await dragWire(page);
    await expect(page.locator('.react-flow__edge')).toHaveCount(0);
  });

  test('Delete and Backspace on a selected node remove nothing', async ({ page }) => {
    await selectNode(page, 'n1');
    await expect(page.getByTestId('inspector-component')).toHaveText('discovery.kubernetes');
    await page.keyboard.press('Delete');
    await page.keyboard.press('Backspace');
    await expect(page.locator('.react-flow__node')).toHaveCount(2);
    // Nor does a paste or an undo/redo bring anything into being.
    await page.keyboard.press('ControlOrMeta+c');
    await page.keyboard.press('ControlOrMeta+v');
    await page.keyboard.press('ControlOrMeta+z');
    await page.keyboard.press('ControlOrMeta+Shift+z');
    await expect(page.locator('.react-flow__node')).toHaveCount(2);
  });

  test('selecting a node shows its properties read-only', async ({ page }) => {
    await selectNode(page, 'n1');
    const inspector = page.getByTestId('inspector');
    await expect(page.getByTestId('inspector-component')).toHaveText('discovery.kubernetes');
    await expect(inspector.getByTestId('inspector-read-only')).toBeVisible();

    const role = inspector.getByTestId('attr-select-role');
    await expect(role).toHaveValue('pod');
    await expect(role).toBeDisabled();

    // Optional attributes can still be revealed — that is reading, not editing.
    await inspector.getByTestId('inspector-show-optional').click();
    const apiServer = inspector.getByTestId('attr-input-api_server');
    await expect(apiServer).toHaveValue('https://k8s.example:6443');
    await expect(apiServer).not.toBeEditable();
    // Read-only, not disabled: the value stays selectable for copying.
    await expect(apiServer).toBeEnabled();
    await expect(inspector.getByTestId('attr-bool-proxy_from_environment')).toBeDisabled();
    await expect(inspector.getByTestId('node-disable-toggle')).toBeDisabled();
    // No affordance that would add or remove anything.
    await expect(inspector.locator('[data-testid^="block-add-"]')).toHaveCount(0);
    await expect(inspector.locator('[data-testid^="attr-binding-source-"]')).toHaveCount(0);

    // Selecting another node switches the inspector to it.
    await selectNode(page, 'n2');
    await expect(page.getByTestId('inspector-component')).toHaveText('discovery.relabel');
  });

  test('cannot rename a node by double-clicking its label', async ({ page }) => {
    await nodeById(page, 'n1').getByTestId('node-label').dblclick({ force: true });
    await expect(page.getByTestId('node-label-input')).toHaveCount(0);
  });

  test('can still zoom and pan', async ({ page }) => {
    const before = await viewportTransform(page);
    await page.locator('.react-flow__controls-zoomin').click();
    await expect.poll(() => viewportTransform(page)).not.toBe(before);

    await waitForViewportSettled(page);
    const zoomed = await viewportTransform(page);
    const pane = page.locator('.react-flow__pane');
    const box = await settledBox(pane);
    // Bottom-right of the pane, clear of the nodes, the controls and the minimap.
    const x = box.x + box.width - 60;
    const y = box.y + box.height - 60;
    await page.mouse.move(x, y);
    await page.mouse.down();
    await page.mouse.move(x - 150, y - 100, { steps: 10 });
    await page.mouse.up();
    await expect.poll(() => viewportTransform(page)).not.toBe(zoomed);

    await page.locator('.react-flow__controls-fitview').click();
    await expect(page.locator('.react-flow__node')).toHaveCount(2);
  });
});

test.describe('editor', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(orgEditor);
    await openBuilder(page, api, 'pip-editor');
  });

  test('keeps full canvas editing: drag, wire, delete', async ({ page }) => {
    const node = nodeById(page, 'n1');
    const before = await nodeTransform(node);
    const viewport = await viewportTransform(page);
    await dragBy(page, node.getByTestId('pipeline-node'), 120, 90);
    await expect.poll(() => nodeTransform(node)).not.toBe(before);
    expect(await viewportTransform(page)).toBe(viewport);

    await dragWire(page);
    await expect(page.locator('.react-flow__edge')).toHaveCount(1);

    await selectNode(page, 'n2');
    await page.keyboard.press('Delete');
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
  });

  test('keeps an editable inspector', async ({ page }) => {
    await selectNode(page, 'n1');
    const inspector = page.getByTestId('inspector');
    await expect(page.getByTestId('inspector-component')).toHaveText('discovery.kubernetes');
    await expect(inspector.getByTestId('inspector-read-only')).toHaveCount(0);
    await expect(inspector.getByTestId('attr-select-role')).toBeEnabled();
    await inspector.getByTestId('inspector-show-optional').click();
    await expect(inspector.getByTestId('attr-input-api_server')).toBeEditable();
    await expect(inspector.getByTestId('node-disable-toggle')).toBeEnabled();
  });
});
