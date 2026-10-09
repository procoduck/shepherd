// The "Leave site?" guard must fire only for a graph that differs from what
// was loaded or last saved. Before this spec the guard fired for any
// non-empty graph, so opening an existing pipeline and leaving it untouched
// still produced the prompt.
import { expect } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const loadedGraph = {
  kind: 'alloy-graph/v1',
  // The fixture's own version, so loading this graph never counts as a
  // schema upgrade (that would itself be an unsaved change).
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    {
      id: 'n1',
      component: 'prometheus.exporter.self',
      label: 'self',
      position: { x: 100, y: 100 },
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

// Dispatching a cancelable beforeunload is how the guard is observed without
// leaving the page: the handler calls preventDefault only when it would show
// the browser dialog.
async function guardWouldPrompt(page: import('@playwright/test').Page): Promise<boolean> {
  return page.evaluate(() => {
    const ev = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(ev);
    return ev.defaultPrevented;
  });
}

test('an untouched loaded graph does not trigger the unsaved-changes guard', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const visualPipe = {
    ...pipeline({ id: 'pip-guard', name: 'guarded', source: 'visual' }),
    wizard_state: loadedGraph,
  };
  api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [visualPipe] });

  await page.goto(`/pipelines/${visualPipe.id}/visual`);
  await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
  await expect(page.locator('.react-flow__node')).toHaveCount(1);

  expect(await guardWouldPrompt(page)).toBe(false);

  // A real edit makes the graph differ from the loaded one.
  await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  await page.click('[data-testid="palette-item-prometheus.remote_write"]');
  await expect(page.locator('.react-flow__node')).toHaveCount(2);

  expect(await guardWouldPrompt(page)).toBe(true);
});

// B4 (2026-10-09 walkthrough): a brand-new builder asked "leave page?" on
// reload with nothing edited. Stamping the served schema version onto the
// fresh graph read as an edit; it is not one.
test('an untouched new graph does not trigger the unsaved-changes guard', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [] });

  await page.goto('/pipelines/visual/new');
  await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
  // The palette renders from the loaded schema, and setSchema stamps the
  // served version onto the fresh graph in the same update — so once it is
  // here, the stamp that used to read as an edit has happened.
  await page.waitForSelector('[data-testid="palette-item-prometheus.remote_write"]', {
    timeout: 8_000,
  });

  expect(await guardWouldPrompt(page)).toBe(false);

  await page.click('[data-testid="palette-item-prometheus.remote_write"]');
  await expect(page.locator('.react-flow__node')).toHaveCount(1);
  expect(await guardWouldPrompt(page)).toBe(true);
});
