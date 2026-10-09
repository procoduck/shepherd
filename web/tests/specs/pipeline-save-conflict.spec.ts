// F1 (2026-10-09 walkthrough): a stale editor tab saved its old copy over a
// newer server state with a 200. Every save now carries the revision the
// editor loaded (UpdatePipelineRequest.expected_revision); the server refuses
// one made from an older revision with `aborted`, and the editor asks what to
// do: Reload (drop the local copy) or Overwrite (resend without the check).
//
// "The other tab" is simulated by changing the mock server's state while the
// page sits on its loaded copy — nothing refetches it in between, exactly
// like a background tab.
import { basicScenario, pipeline, revision } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { expect, test } from '../fixtures/test';

type Obj = Record<string, unknown>;

function seedWizardPipeline(api: { seed: (partial: Record<string, unknown>) => void }) {
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-conflict',
    name: 'conflict-pipe',
    source: 'ui',
    contents: '// loaded copy\n',
    matchers: ['env="prod"'],
    revisions: [
      revision({
        pipeline_id: 'pip-conflict',
        revision: 1,
        contents: '// loaded copy\n',
        matchers: ['env="prod"'],
        change_note: 'created',
      }),
    ],
  });
  api.seed({ orgs: [s.org], pipelines: [p] });
  return p;
}

/** What "tab A" does: saves a newer copy, which is revision 2 on the server. */
function saveFromAnotherTab(api: { state: { pipelines: unknown[] } }, id: string, text: string) {
  const p = api.state.pipelines.find((x) => (x as Obj).id === id) as Obj;
  const revisions = p.revisions as Obj[];
  revisions.unshift(
    revision({ pipeline_id: id, revision: 2, contents: text, matchers: ['env="prod"'] }),
  );
  p.contents = text;
}

function updateBodies(api: { calls: (p: string) => Array<{ body: unknown }> }) {
  return api.calls('PipelineService/UpdatePipeline').map((c) => c.body as Obj);
}

test('a save from a copy the server has moved past is refused, and Reload shows the newer one', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const p = seedWizardPipeline(api);
  await page.goto(`/pipelines/${p.id}`);
  await expect(page.locator('.cm-content')).toContainText('// loaded copy');

  saveFromAnotherTab(api, p.id, '// saved in the other tab\n');

  // No edits at all — the walkthrough's repro: Save on the untouched stale tab.
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  const dialog = page.getByTestId('save-conflict');
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId('save-conflict-message')).toContainText('revision 1, now 2');
  expect(updateBodies(api)[0].expectedRevision).toBe(1);
  // Nothing was written over the other tab's save.
  expect((api.state.pipelines[0] as Obj).contents).toBe('// saved in the other tab\n');

  await page.getByTestId('save-conflict-reload').click();
  await expect(dialog).toHaveCount(0);
  await expect(page.locator('.cm-content')).toContainText('// saved in the other tab');

  // The editor now stands on revision 2: the next save goes through.
  await page.locator('.cm-content').click();
  await page.keyboard.press('End');
  await page.keyboard.type(' edited');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(
    page.locator('[data-sonner-toast]').filter({ hasText: 'Pipeline saved' }),
  ).toBeVisible();
  const bodies = updateBodies(api);
  expect(bodies).toHaveLength(2);
  expect(bodies[1].expectedRevision).toBe(2);
});

test('Overwrite, chosen in the conflict dialog, saves the local copy without the check', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const p = seedWizardPipeline(api);
  await page.goto(`/pipelines/${p.id}`);
  await expect(page.locator('.cm-content')).toContainText('// loaded copy');

  await page.locator('.cm-content').click();
  await page.keyboard.press('End');
  await page.keyboard.type(' mine');
  saveFromAnotherTab(api, p.id, '// theirs\n');

  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.getByTestId('save-conflict')).toBeVisible();
  // Cancel keeps the local edits and writes nothing.
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByTestId('save-conflict')).toHaveCount(0);
  await expect(page.locator('.cm-content')).toContainText('mine');
  expect((api.state.pipelines[0] as Obj).contents).toBe('// theirs\n');

  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await page.getByTestId('save-conflict-overwrite').click();
  await expect(page.getByTestId('save-conflict')).toHaveCount(0);
  await expect(
    page.locator('[data-sonner-toast]').filter({ hasText: 'Pipeline saved' }),
  ).toBeVisible();

  const bodies = updateBodies(api);
  expect(bodies).toHaveLength(3);
  expect(bodies[0].expectedRevision).toBe(1);
  expect(bodies[1].expectedRevision).toBe(1);
  expect(bodies[2]).not.toHaveProperty('expectedRevision');
  expect((api.state.pipelines[0] as Obj).contents).toContain('mine');
});

const graph = {
  kind: 'alloy-graph/v1',
  schema_version: 'alloy-v1.18.1',
  nodes: [],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-parser' },
};

test('the visual builder sends the revision it loaded, and offers Reload on a conflict', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-visual-conflict',
    name: 'visual-conflict',
    source: 'visual',
    contents: '// generated\n',
    matchers: ['env="prod"', 'team="core"'],
    revisions: [revision({ pipeline_id: 'pip-visual-conflict', revision: 1 })],
  });
  api.seed({
    orgs: [s.org],
    schema: schemaFixture,
    pipelines: [{ ...p, wizard_state: graph }],
    visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
  });

  await page.goto(`/pipelines/${p.id}/visual`);
  await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
  await expect(page.getByTestId('matcher-chip')).toHaveCount(2);

  // The other tab drops a matcher: revision 2.
  const stored = api.state.pipelines[0] as Obj;
  (stored.revisions as Obj[]).unshift(revision({ pipeline_id: p.id, revision: 2 }));
  stored.matchers = ['env="prod"'];

  await page.getByTestId('matcher-remove-0').click();
  await page.getByTestId('toolbar-save').click();
  await expect(page.getByTestId('save-conflict')).toBeVisible();
  // The builder only reloads; overwriting is the text editor's explicit choice.
  await expect(page.getByTestId('save-conflict-overwrite')).toHaveCount(0);
  expect(updateBodies(api)[0].expectedRevision).toBe(1);
  expect(stored.matchers).toEqual(['env="prod"']);
  await expect(page).toHaveURL(new RegExp(`/pipelines/${p.id}/visual$`));

  const getsBefore = api.calls('PipelineService/GetPipeline').length;
  await page.getByTestId('save-conflict-reload').click();
  await expect(page.getByTestId('save-conflict')).toHaveCount(0);
  await expect
    .poll(() => api.calls('PipelineService/GetPipeline').length)
    .toBeGreaterThan(getsBefore);
  // The server's copy, not the local edit: one matcher, env="prod".
  await expect(page.getByTestId('matcher-chip')).toHaveCount(1);
  await expect(page.getByTestId('matcher-chip')).toContainText('env="prod"');

  // Saving from the reloaded copy carries revision 2 and lands.
  await page.getByTestId('toolbar-save').click();
  await expect(page).toHaveURL(new RegExp(`/pipelines/${p.id}\\?from=visual$`), {
    timeout: 5_000,
  });
  expect(updateBodies(api)[1].expectedRevision).toBe(2);
});

// A local draft is the other way a stale graph reaches Save: it is restored
// over a freshly loaded server copy. The draft keeps the revision its edits
// started from, and restoring it makes that the revision the save is checked
// against — a draft made at revision 5 cannot be saved over revision 6.
test('a restored draft saves against the revision it was edited from', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-draft-conflict',
    name: 'draft-conflict',
    source: 'visual',
    contents: '// generated\n',
    matchers: ['env="prod"'],
    revisions: [5, 4, 3, 2, 1].map((n) =>
      revision({ pipeline_id: 'pip-draft-conflict', revision: n }),
    ),
  });
  api.seed({
    orgs: [s.org],
    schema: schemaFixture,
    pipelines: [{ ...p, wizard_state: graph }],
    visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
  });

  await page.goto(`/pipelines/${p.id}/visual`);
  await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });
  await page.click('[data-testid="palette-item-prometheus.remote_write"]');
  await expect(page.locator('.react-flow__node')).toHaveCount(1);
  // The autosaved draft carries the base revision it was edited from.
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          new Promise<unknown>((resolve) => {
            const open = indexedDB.open('keyval-store');
            open.onerror = () => resolve(null);
            open.onsuccess = () => {
              const get = open.result
                .transaction('keyval', 'readonly')
                .objectStore('keyval')
                .get('vb:draft-base:pip-draft-conflict');
              get.onsuccess = () => resolve(get.result ?? null);
              get.onerror = () => resolve(null);
            };
          }),
      ),
    )
    .toBe(5);

  // Another tab saves: the server is at revision 6.
  const stored = api.state.pipelines[0] as Obj;
  (stored.revisions as Obj[]).unshift(revision({ pipeline_id: p.id, revision: 6 }));

  page.on('dialog', (d) => {
    void d.accept();
  });
  await page.reload();
  await expect(page.getByTestId('draft-restore-banner')).toBeVisible();
  // A draft that knows its base needs no "may be older" warning.
  await expect(page.getByTestId('draft-restore-age-warning')).toHaveCount(0);
  await page.getByTestId('draft-restore').click();
  await expect(page.locator('.react-flow__node')).toHaveCount(1);

  await page.getByTestId('toolbar-save').click();
  await expect(page.getByTestId('save-conflict')).toBeVisible();
  await expect(page.getByTestId('save-conflict-message')).toContainText('revision 5, now 6');
  expect(updateBodies(api).at(-1)?.expectedRevision).toBe(5);
});

// A draft saved before base revisions were recorded cannot say how old it is:
// the banner warns, and restoring it is the person's explicit call.
test('a draft with no recorded base revision is restored only with a warning', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-legacy-draft',
    name: 'legacy-draft',
    source: 'visual',
    contents: '// generated\n',
    matchers: ['env="prod"'],
    revisions: [revision({ pipeline_id: 'pip-legacy-draft', revision: 3 })],
  });
  api.seed({
    orgs: [s.org],
    schema: schemaFixture,
    pipelines: [{ ...p, wizard_state: graph }],
    visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
  });
  await page.goto(`/pipelines/${p.id}/visual`);
  await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
  // The old shape: the graph alone under vb:draft:<id>, no base key.
  await page.evaluate(
    (doc) =>
      new Promise<void>((resolve, reject) => {
        const open = indexedDB.open('keyval-store');
        open.onupgradeneeded = () => open.result.createObjectStore('keyval');
        open.onerror = () => reject(open.error);
        open.onsuccess = () => {
          const tx = open.result.transaction('keyval', 'readwrite');
          tx.objectStore('keyval').put(doc, 'vb:draft:pip-legacy-draft');
          tx.oncomplete = () => resolve();
          tx.onerror = () => reject(tx.error);
        };
      }),
    {
      ...graph,
      nodes: [
        {
          id: 'n1',
          component: 'prometheus.remote_write',
          label: 'old draft',
          position: { x: 0, y: 0 },
          props: {},
          disabled: false,
          notes: '',
        },
      ],
    },
  );
  await page.reload();
  await expect(page.getByTestId('draft-restore-banner')).toBeVisible();
  await expect(page.getByTestId('draft-restore-age-warning')).toBeVisible();
  await expect(page.getByTestId('draft-restore')).toHaveText('Restore draft anyway');
  await page.getByTestId('draft-restore').click();
  await expect(page.locator('.react-flow__node')).toHaveCount(1);
});
