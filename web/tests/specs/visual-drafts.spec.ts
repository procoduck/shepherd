// visual-drafts.spec.ts — W5-05: IndexedDB draft autosave and the
// restore-or-discard banner (design §4.4).
import { expect, type Page } from '@playwright/test';
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { basicScenario, pipeline } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

/**
 * Polls for the debounced IndexedDB autosave (subscribeDraftAutosave,
 * draft.ts, production delayMs default 500ms) actually landing for
 * `pipelineId`, instead of a flat page.waitForTimeout comfortably past the
 * debounce (W7-13: waitForTimeout is real-time and either flaky or padded).
 * expect.poll moves on the moment the write lands and still tolerates a
 * loaded CI/dev machine delaying well past 500ms. Reads idb-keyval's
 * default store directly (DB 'keyval-store', object store 'keyval') since
 * that's the actual thing under test — the store instance itself, not a
 * proxy for it.
 */
async function waitForDraftSaved(
  page: Page,
  pipelineId: string,
  // When set, the draft must contain a node of this component — for a spec
  // where an older draft (with other nodes) is already on disk.
  component?: string,
): Promise<void> {
  await expect
    .poll(
      () =>
        page.evaluate(
          ({ key, component }) =>
            new Promise<boolean>((resolve) => {
              const openReq = indexedDB.open('keyval-store');
              openReq.onerror = () => resolve(false);
              openReq.onsuccess = () => {
                const db = openReq.result;
                if (!db.objectStoreNames.contains('keyval')) {
                  resolve(false);
                  return;
                }
                const getReq = db.transaction('keyval', 'readonly').objectStore('keyval').get(key);
                // A draft WITH the node the test added — not just any entry:
                // autosave may first persist the empty doc (the served
                // schema version stamped onto a fresh doc is a doc change),
                // and reloading on that would leave nothing to restore.
                getReq.onsuccess = () => {
                  const nodes =
                    (getReq.result as { nodes?: { component: string }[] } | undefined)?.nodes ?? [];
                  resolve(
                    component ? nodes.some((n) => n.component === component) : nodes.length > 0,
                  );
                };
                getReq.onerror = () => resolve(false);
              };
            }),
          { key: `vb:draft:${pipelineId}`, component },
        ),
      { timeout: 5000 },
    )
    .toBe(true);
}

test.describe('visual builder drafts', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  });

  test('a draft survives a reload and is offered for restore', async ({ page }) => {
    await page.click('[data-testid="palette-item-prometheus.remote_write"]');
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
    await waitForDraftSaved(page, 'new');

    // The existing beforeunload handler (VisualBuilderPage.tsx) turns
    // page.reload() into a confirm dialog since the graph is non-empty;
    // accept it so the reload actually proceeds.
    page.on('dialog', (d) => {
      void d.accept();
    });
    await page.reload();
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });

    // A fresh mount starts from an empty doc; the autosaved draft (1 node)
    // differs from it, so the restore banner offers it.
    await expect(page.getByTestId('draft-restore-banner')).toBeVisible();
    await expect(page.locator('.react-flow__node')).toHaveCount(0);

    await page.getByTestId('draft-restore').click();
    await expect(page.getByTestId('draft-restore-banner')).not.toBeVisible();
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
  });

  test('a draft can be discarded, and no longer offers itself after', async ({ page }) => {
    await page.click('[data-testid="palette-item-prometheus.remote_write"]');
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
    await waitForDraftSaved(page, 'new');

    page.on('dialog', (d) => {
      void d.accept();
    });
    await page.reload();
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('draft-restore-banner')).toBeVisible();

    await page.getByTestId('draft-discard').click();
    await expect(page.getByTestId('draft-restore-banner')).not.toBeVisible();
    await expect(page.locator('.react-flow__node')).toHaveCount(0);

    // Reloading again (nothing to warn about — the canvas is empty) must not
    // resurrect the discarded draft.
    await page.reload();
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('draft-restore-banner')).not.toBeVisible();
  });

  // Opening the builder must not overwrite a pending draft. setSchema stamps the
  // served schema version onto the fresh doc on mount — a doc change — and the
  // autosave used to write that empty doc over the saved draft 500ms later. The
  // banner still showed (it had read the draft first, when it won that race),
  // but the draft on disk was already gone: leave without choosing, come back,
  // and the work was lost. This reload-twice sequence is deterministic.
  test('a pending draft survives leaving without choosing restore or discard', async ({ page }) => {
    await page.click('[data-testid="palette-item-prometheus.remote_write"]');
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
    await waitForDraftSaved(page, 'new');

    page.on('dialog', (d) => {
      void d.accept();
    });
    // Fake timers from here, so the (old) debounced autosave can be fired
    // deterministically instead of slept for.
    await page.clock.install();
    await page.reload();
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('draft-restore-banner')).toBeVisible();
    await page.clock.runFor(2000); // well past the 500ms autosave debounce

    await page.reload();
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('draft-restore-banner')).toBeVisible();
    await page.getByTestId('draft-restore').click();
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
  });

  // #202: an edit made while the banner is still unanswered used to be lost —
  // autosave stayed paused for as long as the banner showed, so the old draft
  // stayed on disk and "Restore draft" on the next visit brought back the
  // graph from before the edit. Editing now counts as continuing from what's
  // on screen: the banner goes away and the edited graph becomes the draft.
  test('an edit made while the restore banner is showing becomes the draft', async ({ page }) => {
    await page.click('[data-testid="palette-item-prometheus.remote_write"]');
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
    await waitForDraftSaved(page, 'new');

    page.on('dialog', (d) => {
      void d.accept();
    });
    await page.reload();
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('draft-restore-banner')).toBeVisible();

    // Edit without answering the banner.
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
    await page.click('[data-testid="palette-item-loki.write"]');
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
    await expect(page.getByTestId('draft-restore-banner')).not.toBeVisible();
    await waitForDraftSaved(page, 'new', 'loki.write');

    await page.reload();
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('draft-restore-banner')).toBeVisible();
    await page.getByTestId('draft-restore').click();
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
    // The restored node is the one added while the banner showed, not the
    // older draft's remote_write.
    await expect(page.locator('.react-flow__node')).toContainText('loki.write');
    await expect(page.locator('.react-flow__node')).not.toContainText('prometheus.remote_write');
  });

  test('no banner appears for a route with no draft', async ({ page }) => {
    await expect(page.getByTestId('draft-restore-banner')).not.toBeVisible();
  });

  test("the 'new' draft is cleared once the create actually succeeds", async ({ page, api }) => {
    api.seed({
      visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
    });
    await page.click('[data-testid="palette-item-prometheus.remote_write"]');
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
    await waitForDraftSaved(page, 'new');

    await page.locator('[data-testid="toolbar-name"]').fill('checkout-metrics');
    const input = page.locator('[data-testid="matcher-input"]');
    await input.fill('cluster="prod-eu-1"');
    await input.press('Enter');
    await page.locator('[data-testid="toolbar-save"]').click();
    await expect(page).toHaveURL(/\/pipelines\/pip-\d+\?from=visual$/, { timeout: 5_000 });

    // A crash mid-authoring of a NEXT new pipeline must not resurrect the
    // just-saved one's draft — it was cleared on the successful create.
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await expect(page.getByTestId('draft-restore-banner')).not.toBeVisible();
  });
});

/** Polls until the draft's name/matchers entry (#319) for `pipelineId` holds `name`. */
async function waitForDraftMeta(page: Page, pipelineId: string, name: string): Promise<void> {
  await expect
    .poll(
      () =>
        page.evaluate(
          (key) =>
            new Promise<string | null>((resolve) => {
              const openReq = indexedDB.open('keyval-store');
              openReq.onerror = () => resolve(null);
              openReq.onsuccess = () => {
                const db = openReq.result;
                if (!db.objectStoreNames.contains('keyval')) {
                  resolve(null);
                  return;
                }
                const getReq = db.transaction('keyval', 'readonly').objectStore('keyval').get(key);
                getReq.onsuccess = () =>
                  resolve((getReq.result as { name?: string } | undefined)?.name ?? null);
                getReq.onerror = () => resolve(null);
              };
            }),
          `vb:draft-meta:${pipelineId}`,
        ),
      { timeout: 5000 },
    )
    .toBe(name);
}

// #319: a draft used to hold only the graph and its base revision, so an
// unsaved name or matcher edit was never offered back — only the beforeunload
// warning covered it. The draft now keeps both, and restoring it restores them.
test.describe('visual builder drafts keep the name and matchers', () => {
  const chips = (page: Page) => page.getByTestId('matcher-chip');

  test('a new pipeline draft restores its name and matchers with the graph', async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaFixture });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });

    await page.click('[data-testid="palette-item-prometheus.remote_write"]');
    await page.getByTestId('toolbar-name').fill('draft-name');
    const input = page.getByTestId('matcher-input');
    await input.fill('cluster="draft-1"');
    await input.press('Enter');
    await waitForDraftSaved(page, 'new');
    await waitForDraftMeta(page, 'new', 'draft-name');

    page.on('dialog', (d) => {
      void d.accept();
    });
    await page.reload();
    await expect(page.getByTestId('draft-restore-banner')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('toolbar-name')).toHaveValue('');
    await expect(chips(page)).toHaveCount(0);

    await page.getByTestId('draft-restore').click();
    await expect(page.locator('.react-flow__node')).toHaveCount(1);
    await expect(page.getByTestId('toolbar-name')).toHaveValue('draft-name');
    await expect(chips(page)).toContainText(['cluster="draft-1"']);
  });

  test('a name and matcher edit alone, on a saved pipeline, is offered and restored', async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    const visual = {
      ...pipeline({ id: 'pip-draft-meta', name: 'saved-name', source: 'visual' }),
      matchers: ['env="prod"'],
      wizard_state: {
        kind: 'alloy-graph/v1',
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
      },
    };
    api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [visual] });
    await page.goto(`/pipelines/${visual.id}/visual`);
    await expect(page.locator('.react-flow__node')).toHaveCount(1, { timeout: 10_000 });
    await expect(page.getByTestId('toolbar-name')).toHaveValue('saved-name');

    await page.getByTestId('toolbar-name').fill('edited-name');
    const input = page.getByTestId('matcher-input');
    await input.fill('team="draft"');
    await input.press('Enter');
    await waitForDraftMeta(page, visual.id, 'edited-name');

    page.on('dialog', (d) => {
      void d.accept();
    });
    await page.reload();
    await expect(page.getByTestId('draft-restore-banner')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('toolbar-name')).toHaveValue('saved-name');

    await page.getByTestId('draft-restore').click();
    await expect(page.getByTestId('draft-restore-banner')).not.toBeVisible();
    await expect(page.getByTestId('toolbar-name')).toHaveValue('edited-name');
    await expect(chips(page)).toContainText(['env="prod"', 'team="draft"']);
  });
});

// #324 review: drafts live in this browser's IndexedDB, not per account, so
// the next person to sign in here was offered the last one's unsaved work.
// Signing out clears them.
test('signing out clears every builder draft from this browser', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], schema: schemaFixture });
  await page.goto('/pipelines/visual/new');
  await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });
  await page.click('[data-testid="palette-item-prometheus.remote_write"]');
  await page.getByTestId('toolbar-name').fill('mine');
  await waitForDraftSaved(page, 'new');
  await waitForDraftMeta(page, 'new', 'mine');

  page.on('dialog', (d) => {
    void d.accept();
  });
  await page.getByRole('button', { name: 'Sign out' }).click();
  // The mock keeps the session, so /login bounces on; leaving the builder is enough.
  await expect(page).not.toHaveURL(/\/visual\/new/);

  const draftKeys = () =>
    page.evaluate(
      () =>
        new Promise<string[]>((resolve) => {
          const openReq = indexedDB.open('keyval-store');
          openReq.onerror = () => resolve([]);
          openReq.onsuccess = () => {
            const db = openReq.result;
            if (!db.objectStoreNames.contains('keyval')) {
              resolve([]);
              return;
            }
            const req = db.transaction('keyval', 'readonly').objectStore('keyval').getAllKeys();
            req.onsuccess = () =>
              resolve(req.result.map(String).filter((k) => k.startsWith('vb:draft')));
            req.onerror = () => resolve([]);
          };
        }),
    );
  await expect.poll(draftKeys).toEqual([]);
});
