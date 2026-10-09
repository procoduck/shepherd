// visual-save-editor-cache.spec.ts — H1 (2026-10-08 walkthrough): saving in
// the visual builder lands on the text editor (?from=visual), and that editor
// must show what was JUST saved. It used to seed its form from the react-query
// cache entry the builder's breadcrumb had filled on the way in — the
// pre-save pipeline — and its seed-once guard then ignored the refetch, so the
// editor showed the old matchers, and a plain Save there wrote them back.
import { expect } from '@playwright/test';
import { basicScenario, pipeline } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const mockGraph = {
  kind: 'alloy-graph/v1',
  schema_version: 'alloy-v1.18.1',
  nodes: [],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-parser' },
};

test.describe('visual builder save → text editor landing', () => {
  test('the editor shows the saved matchers, and saving there keeps them', async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    const demo = pipeline({
      id: 'pip-demo-visual',
      name: 'demo-visual',
      source: 'visual',
      contents: '// generated\n',
      matchers: ['env="prod"', 'team="core"'],
      updated_by: 'seed',
    });
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [demo],
      graphViewResult: { graph: mockGraph, opaque: false, warning: '' },
      visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
    });

    await page.goto(`/pipelines/${demo.id}/visual`);
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('matcher-chip')).toHaveCount(2);

    // Remove team="core" in the builder and save.
    await page.getByTestId('matcher-remove-1').click();
    await expect(page.getByTestId('matcher-chip')).toHaveCount(1);
    await page.getByTestId('toolbar-save').click();
    await expect(page).toHaveURL(new RegExp(`/pipelines/${demo.id}\\?from=visual$`), {
      timeout: 5_000,
    });
    const builderSave = api.calls('PipelineService/UpdatePipeline');
    expect(builderSave).toHaveLength(1);
    expect((builderSave[0].body as Record<string, unknown>).matchers).toEqual(['env="prod"']);

    // The landing editor shows the NEW matchers and who saved them.
    const chips = page.getByTestId('pipeline-matcher-chip');
    await expect(chips).toHaveCount(1);
    await expect(chips).toContainText('env="prod"');
    await expect(page.getByText(`Updated by: ${appAdmin.email}`)).toBeVisible();

    // A plain Save in the editor must write the NEW matchers, not the old.
    const save = page.getByRole('button', { name: 'Save', exact: true });
    await expect(save).toBeEnabled();
    await save.click();
    await expect.poll(() => api.calls('PipelineService/UpdatePipeline').length).toBe(2);
    const editorSave = api.calls('PipelineService/UpdatePipeline')[1].body as Record<
      string,
      unknown
    >;
    expect(editorSave.matchers).toEqual(['env="prod"']);
  });

  test('a pipeline already viewed in the editor is not shown stale after a builder save', async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    const demo = pipeline({
      id: 'pip-demo-visual',
      name: 'demo-visual',
      source: 'visual',
      contents: '// generated\n',
      matchers: ['env="prod"', 'team="core"'],
      updated_by: 'seed',
    });
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [demo],
      graphViewResult: { graph: mockGraph, opaque: false, warning: '' },
      visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
    });

    // Editor first (fills the cache and the editor's seed), then the builder
    // through the in-app link — a client-side navigation, so the cache lives.
    await page.goto(`/pipelines/${demo.id}`);
    await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(2);
    await page.getByTestId('editor-open-visual').click();
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('matcher-chip')).toHaveCount(2);

    await page.getByTestId('matcher-remove-0').click();
    await page.getByTestId('toolbar-save').click();
    await expect(page).toHaveURL(new RegExp(`/pipelines/${demo.id}\\?from=visual$`), {
      timeout: 5_000,
    });

    const chips = page.getByTestId('pipeline-matcher-chip');
    await expect(chips).toHaveCount(1);
    await expect(chips).toContainText('team="core"');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect.poll(() => api.calls('PipelineService/UpdatePipeline').length).toBe(2);
    expect(
      (api.calls('PipelineService/UpdatePipeline')[1].body as Record<string, unknown>).matchers,
    ).toEqual(['team="core"']);
  });

  // A GetPipeline the breadcrumb sent BEFORE the save (here: a window-focus
  // refetch) can answer AFTER the cache was primed with the save's response.
  // Unless it is cancelled it overwrites the primed entry with the pre-save
  // copy, and the editor — whose untouched form follows server data — shows
  // the old matchers again.
  test('a pre-save fetch answering late does not bring the old copy back', async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    const demo = pipeline({
      id: 'pip-demo-visual',
      org_id: s.org.id,
      name: 'demo-visual',
      source: 'visual',
      contents: '// generated\n',
      matchers: ['env="prod"', 'team="core"'],
      updated_by: 'seed',
    });
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [demo],
      graphViewResult: { graph: mockGraph, opaque: false, warning: '' },
      visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
    });

    // Every GetPipeline answers with the server copy as it was when the
    // request ARRIVED; while `hold` is armed the answer is also kept back.
    let hold = false;
    let held = false;
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => {
      release = r;
    });
    api.override('POST', '/shepherd.mgmt.v1.PipelineService/GetPipeline', async (route, _p, st) => {
      const p = (st.pipelines as Record<string, unknown>[])[0];
      const wire = {
        id: p.id,
        orgId: p.org_id,
        name: p.name,
        contents: p.contents,
        matchers: [...(p.matchers as string[])],
        source: p.source,
        enabled: p.enabled,
        updatedBy: p.updated_by,
        // What the server reports to an app admin (Pipeline.can_edit, F3).
        canEdit: true,
      };
      if (hold) {
        hold = false;
        held = true;
        await gate;
      }
      await route
        .fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(wire) })
        .catch(() => undefined); // the page may have aborted it
    });

    await page.clock.install();
    await page.goto(`/pipelines/${demo.id}/visual`);
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('matcher-chip')).toHaveCount(2);

    // Make the breadcrumb's cached copy stale and refocus the window: it
    // refetches, and that (pre-save) answer is held.
    hold = true;
    await page.clock.fastForward(31_000);
    await page.evaluate(() => window.dispatchEvent(new Event('visibilitychange')));
    await expect.poll(() => held).toBe(true);

    await page.getByTestId('matcher-remove-1').click();
    await page.getByTestId('toolbar-save').click();
    await expect(page).toHaveURL(new RegExp(`/pipelines/${demo.id}\\?from=visual$`), {
      timeout: 5_000,
    });
    const chips = page.getByTestId('pipeline-matcher-chip');
    await expect(chips).toHaveCount(1);

    // Now the pre-save answer arrives.
    release();
    await expect(page.getByText(`Updated by: ${appAdmin.email}`)).toBeVisible();
    // Give the late answer every chance to land, then check it did not win.
    await expect
      .poll(() => api.calls('PipelineService/GetPipeline').length)
      .toBeGreaterThanOrEqual(2);
    await page.clock.runFor(1_000);
    await expect(chips).toHaveCount(1);
    await expect(chips).toContainText('env="prod"');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect.poll(() => api.calls('PipelineService/UpdatePipeline').length).toBe(2);
    expect(
      (api.calls('PipelineService/UpdatePipeline')[1].body as Record<string, unknown>).matchers,
    ).toEqual(['env="prod"']);
  });
});
