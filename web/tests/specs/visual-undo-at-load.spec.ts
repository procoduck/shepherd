// v0.21.0 final walkthrough: opening a saved visual pipeline, focusing the
// canvas and pressing Cmd/Ctrl+Z without editing anything emptied the canvas
// (nodes and edges to 0) and changed Save's state; redo brought it back. The
// load itself was the first undo step. Loading a document now starts a fresh
// history: undo at load is a no-op and the graph stays clean, while undo after
// a real edit still returns to the loaded graph (and clean again).
import { expect, type Page } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { waitForViewportSettled } from '../fixtures/canvas';
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const loadedGraph = {
  kind: 'alloy-graph/v1',
  // The fixture's own version, so the load is never a schema upgrade.
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    {
      id: 'n1',
      component: 'discovery.kubernetes',
      label: 'k8s',
      position: { x: 0, y: 0 },
      props: {},
      disabled: false,
      notes: '',
    },
    {
      id: 'n2',
      component: 'prometheus.scrape',
      label: 'scrape',
      position: { x: 400, y: 0 },
      props: {},
      disabled: false,
      notes: '',
    },
  ],
  edges: [
    {
      id: 'e1',
      from: { node: 'n1', port: 'targets' },
      to: { node: 'n2', port: 'targets' },
    },
  ],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-visual-builder' },
};

// The unsaved-changes guard is the page's own reading of the store's dirty
// state (visual-unsaved-guard.spec.ts): a cancelable beforeunload is
// prevented only for a graph that differs from what was loaded or saved.
async function isDirty(page: Page): Promise<boolean> {
  return page.evaluate(() => {
    const ev = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(ev);
    return ev.defaultPrevented;
  });
}

test.beforeEach(async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    schema: schemaFixture,
    pipelines: [
      {
        ...pipeline({ id: 'pip-undo', name: 'undo-at-load', source: 'visual' }),
        wizard_state: loadedGraph,
      },
    ],
  });
  await page.goto('/pipelines/pip-undo/visual');
  await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
  await expect(page.locator('.react-flow__node')).toHaveCount(2);
  await expect(page.locator('.react-flow__edge')).toHaveCount(1);
  await waitForViewportSettled(page);
});

test('undo straight after loading a saved pipeline changes nothing', async ({ page }) => {
  const save = page.getByTestId('toolbar-save');
  const saveEnabledAtLoad = await save.isEnabled();
  expect(await isDirty(page)).toBe(false);

  await page.getByTestId('canvas').focus();
  await page.keyboard.press('ControlOrMeta+z');
  await page.keyboard.press('ControlOrMeta+z');

  await expect(page.locator('.react-flow__node')).toHaveCount(2);
  await expect(page.locator('.react-flow__edge')).toHaveCount(1);
  expect(await save.isEnabled()).toBe(saveEnabledAtLoad);
  expect(await isDirty(page)).toBe(false);
});

test('undo after a real edit returns to the loaded graph, clean, and no further', async ({
  page,
}) => {
  await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  await page.click('[data-testid="palette-item-prometheus.remote_write"]');
  await expect(page.locator('.react-flow__node')).toHaveCount(3);
  expect(await isDirty(page)).toBe(true);

  await page.getByTestId('canvas').focus();
  await page.keyboard.press('ControlOrMeta+z');
  await expect(page.locator('.react-flow__node')).toHaveCount(2);
  expect(await isDirty(page)).toBe(false);

  // The loaded graph is the bottom of the history.
  await page.keyboard.press('ControlOrMeta+z');
  await expect(page.locator('.react-flow__node')).toHaveCount(2);
  await expect(page.locator('.react-flow__edge')).toHaveCount(1);
  expect(await isDirty(page)).toBe(false);

  await page.keyboard.press('ControlOrMeta+Shift+z');
  await expect(page.locator('.react-flow__node')).toHaveCount(3);
  expect(await isDirty(page)).toBe(true);
});
