import { basicScenario, destination, pipeline } from '../fixtures/factories';
import { appAdmin, orgAdmin, orgEditor } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('Save refreshes the revision list and Updated by', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-refresh',
    org_id: s.org.id,
    name: 'refresh-me',
    contents: '// v1',
    updated_by: 'first@example.com',
    revisions: [
      {
        revision: 1,
        changed_by: 'first@example.com',
        changed_at: '2026-08-17T09:00:00Z',
        change_note: 'created',
      },
    ],
  });
  api.seed({ orgs: [s.org], pipelines: [p] });
  await page.goto('/pipelines/pip-refresh');

  await expect(page.getByRole('button', { name: /revision history \(1\)/i })).toBeVisible();
  await expect(page.getByText('Updated by:')).toBeVisible();
  await expect(page.getByText('first@example.com')).toBeVisible();

  // A real change: a save that changes nothing writes no revision (F1's
  // walkthrough nit), on the server and in the mock alike.
  await page.locator('.cm-content').click();
  await page.keyboard.press('End');
  await page.keyboard.type(' // v2');
  await page.getByRole('button', { name: /Save/i }).click();

  // Both must update WITHOUT a reload: the mock UpdatePipeline additively
  // unshifts a revision and sets updated_by, same as RestoreRevision does.
  await expect(page.getByRole('button', { name: /revision history \(2\)/i })).toBeVisible();
  await expect(page.getByText('orgeditor@example.com')).toBeVisible();
  expect(api.calls('/shepherd.mgmt.v1.PipelineService/ListRevisions')).toHaveLength(2);
});

test('new pipeline editor is accessible', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  await page.goto('/pipelines/new');
  await expect(page.locator('.cm-editor')).toBeVisible();
});

test('existing pipeline loads content into editor', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-test', name: 'test-pipe', contents: '// test content' });
  api.seed({ orgs: [s.org], pipelines: [p] });
  await page.goto('/pipelines/pip-test');
  await expect(page.locator('.cm-editor')).toBeVisible();
  await expect(page.getByRole('button', { name: /Save/i })).toBeVisible();
});

// 2026-10-09 re-check: the "×" had no accessible name; the builder's
// equivalent button is labelled "Remove matcher <matcher>".
test('each matcher remove button is named after its matcher', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-m', name: 'm-pipe', matchers: ['env="prod"', 'team="core"'] });
  api.seed({ orgs: [s.org], pipelines: [p] });
  await page.goto('/pipelines/pip-m');
  await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(2);
  await page.getByRole('button', { name: 'Remove matcher team="core"', exact: true }).click();
  await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(1);
  await expect(page.getByTestId('pipeline-matcher-chip')).toContainText('env="prod"');
});

test('save button triggers save mutation', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-save', name: 'save-me', contents: '// ok' });
  api.seed({ orgs: [s.org], pipelines: [p] });
  await page.goto('/pipelines/pip-save');
  await expect(page.getByRole('button', { name: /Save/i })).toBeVisible();
  await page.getByRole('button', { name: /Save/i }).click();
  // Assert the UpdatePipeline RPC call for this pipeline was recorded — the
  // pipeline id now rides in the request body, not the URL path.
  const calls = api
    .calls('/shepherd.mgmt.v1.PipelineService/UpdatePipeline')
    .filter((c) => (c.body as { id?: string } | null)?.id === 'pip-save');
  expect(calls.length).toBeGreaterThan(0);
});

test('validate shows no problems for valid syntax', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: [], validateResult: { valid: true, diagnostics: [] } });
  await page.goto('/pipelines/new');
  await expect(page.getByText(/No problems/i)).toBeVisible();
});

// #209: a server with no Alloy binary skips Stage 2 and still answers
// valid=true. That is a syntax check, not a pass — the editor must say which
// check ran instead of the green "No problems".
test('validate says alloy validate was skipped instead of "No problems"', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-skip', org_id: s.org.id, name: 'skip', contents: '// ok' });
  api.seed({
    orgs: [s.org],
    pipelines: [p],
    validateResult: { valid: true, diagnostics: [], skipped_stages: [2] },
  });
  await page.goto('/pipelines/pip-skip');
  await expect(page.locator('.cm-editor')).toBeVisible();
  await expect
    .poll(() => api.calls('/shepherd.mgmt.v1.PipelineService/ValidatePipeline').length)
    .toBeGreaterThan(0);

  const note = page.getByTestId('validate-skipped-note');
  await expect(note).toBeVisible();
  await expect(note).toHaveText(
    'Syntax checked — alloy validate skipped (no Alloy binary configured)',
  );
  await expect(page.getByText(/No problems/i)).toHaveCount(0);
  // A skipped stage is not an error: Save stays available.
  await expect(page.getByRole('button', { name: 'Save' })).toBeEnabled();
});

test('validates and shows problems panel for syntax errors', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [],
    validateResult: {
      valid: false,
      diagnostics: [{ line: 1, col: 1, message: 'unexpected token', stage: 1 }],
    },
  });
  await page.goto('/pipelines/new');
  // Advance the debounce
  await page.clock.install();
  const ed = page.locator('.cm-editor');
  await ed.click();
  await page.keyboard.type('x');
  await page.clock.fastForward(801);
  // Assert the diagnostic's own text, not /problem/i — that regex also matches
  // the success state's "No problems", so it passes even when the seeded
  // diagnostics are ignored entirely.
  await expect(page.getByText('unexpected token')).toBeVisible();
  await expect(page.getByText(/No problems/i)).not.toBeVisible();
});

// Format is the user's action, so undo takes it back (a background re-sync
// is not — see 'editor form vs a refetch').
test('undo takes back a Format', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-undo', org_id: s.org.id, name: 'undo', contents: '// loaded' });
  api.seed({ orgs: [s.org], pipelines: [p], validateResult: { valid: true, diagnostics: [] } });
  await page.goto('/pipelines/pip-undo');
  const content = page.locator('.cm-content');
  await expect(content).toContainText('// loaded');

  await page.getByTestId('format-btn').click();
  await expect(content).toContainText('// formatted');
  await content.click();
  await page.keyboard.press('ControlOrMeta+Z');
  await page.keyboard.press('ControlOrMeta+End');
  await page.keyboard.type('!');
  await expect(content).toHaveText('// loaded!');
});

test('Format replaces the buffer with the server-canonicalised source', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-fmt',
    org_id: s.org.id,
    name: 'fmt-me',
    contents: 'prometheus.scrape "app"{}',
  });
  api.seed({ orgs: [s.org], pipelines: [p], validateResult: { valid: true, diagnostics: [] } });
  await page.goto('/pipelines/pip-fmt');
  await expect(page.locator('.cm-editor')).toBeVisible();

  await page.getByTestId('format-btn').click();

  await expect(page.locator('.cm-content')).toContainText('// formatted');
  expect(api.calls('/shepherd.mgmt.v1.PipelineService/FormatPipeline').length).toBeGreaterThan(0);
});

test('Validate button triggers an on-demand validation', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-val', org_id: s.org.id, name: 'val-me', contents: '// ok' });
  api.seed({ orgs: [s.org], pipelines: [p], validateResult: { valid: true, diagnostics: [] } });
  await page.goto('/pipelines/pip-val');
  await expect(page.locator('.cm-editor')).toBeVisible();

  const before = api.calls('/shepherd.mgmt.v1.PipelineService/ValidatePipeline').length;
  await page.getByTestId('validate-btn').click();
  await expect
    .poll(() => api.calls('/shepherd.mgmt.v1.PipelineService/ValidatePipeline').length)
    .toBeGreaterThan(before);
});

// H1 follow-through: the form follows the server copy until it is edited. A
// refetch bringing newer data replaces an untouched form (the editor used to
// seed once per id and ignore it, so a stale first copy stuck), and never
// replaces one with edits in it. The enable switch invalidates the pipeline
// query, which is the refetch on demand here.
test.describe('editor form vs a refetch', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    const p = pipeline({
      id: 'pip-sync',
      org_id: s.org.id,
      name: 'sync-me',
      contents: '// v1',
      matchers: ['env="prod"', 'team="core"'],
      enabled: false,
    });
    api.seed({ orgs: [s.org], pipelines: [p] });
    await page.goto('/pipelines/pip-sync');
    await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(2);
    // Someone else changes the matchers on the server.
    Object.assign(api.state.pipelines[0] as Record<string, unknown>, {
      matchers: ['env="prod"'],
      contents: '// v2 from the server',
    });
  });

  test('an untouched form takes the newer server copy', async ({ page }) => {
    await page.getByRole('switch', { name: /Enabled: sync-me/ }).click();
    await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(1);
  });

  const refetchAndWait = async (page: import('@playwright/test').Page) => {
    await page.getByRole('switch', { name: /Enabled: sync-me/ }).click();
    // The switch reflects the refetched pipeline, so the refetch has landed.
    await expect(page.getByRole('switch', { name: /Enabled: sync-me/ })).toHaveAttribute(
      'aria-checked',
      'true',
    );
  };

  test('an untouched form also takes the newer contents, outside undo history', async ({
    page,
  }) => {
    await page.getByRole('switch', { name: /Enabled: sync-me/ }).click();
    const content = page.locator('.cm-content');
    await expect(content).toContainText('// v2 from the server');
    // The re-sync is not an edit: undo must not bring the old copy back.
    await content.click();
    await page.keyboard.press('ControlOrMeta+Z');
    // Keys are handled in order: once the marker shows, the undo has run.
    await page.keyboard.press('ControlOrMeta+End');
    await page.keyboard.type('!');
    await expect(content).toHaveText('// v2 from the server!');
  });

  test('an edited name survives a refetch', async ({ page }) => {
    const nameInput = page.getByPlaceholder('my-pipeline');
    await nameInput.fill('renamed');
    await refetchAndWait(page);
    await expect(nameInput).toHaveValue('renamed');
    await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(2);
    await expect(page.locator('.cm-content')).toContainText('// v1');
  });

  test('edited contents survive a refetch', async ({ page }) => {
    await page.locator('.cm-content').click();
    await page.keyboard.press('ControlOrMeta+End');
    await page.keyboard.type(' typed here');
    await refetchAndWait(page);
    await expect(page.locator('.cm-content')).toContainText('// v1 typed here');
    await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(2);
  });

  test('edited matchers survive a refetch', async ({ page }) => {
    const input = page.getByTestId('pipeline-matcher-input');
    await input.pressSequentially('region="eu"');
    await input.press('Enter');
    await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(3);
    await refetchAndWait(page);
    await expect(page.getByTestId('pipeline-matcher-chip')).toHaveCount(3);
    await expect(page.locator('.cm-content')).toContainText('// v1');
  });
});

// A destination change re-renders the wizard pipelines that ship to it on the
// SERVER (destination_rerender.go). Returning to such a pipeline's editor
// within the 30s staleTime showed the pre-change contents, and a Save wrote
// them back over the re-render.
test('the editor shows a server-side re-render after a destination change', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-wiz',
    org_id: s.org.id,
    name: 'wiz-pipe',
    source: 'wizard',
    contents: '// rendered for https://old.example.org',
  });
  api.seed({
    orgs: [s.org],
    pipelines: [p],
    destinations: [destination({ id: 'dst-1', name: 'prom-prod', auth_mode: 'none' })],
  });
  await page.goto('/pipelines/pip-wiz');
  await expect(page.locator('.cm-content')).toContainText('old.example.org');

  await page.getByRole('link', { name: 'Destinations' }).click();
  await page.getByRole('button', { name: 'Edit prom-prod' }).click();
  const dialog = page.getByRole('dialog', { name: 'Edit prom-prod' });
  const url = dialog.getByLabel('URL', { exact: true });
  await url.clear();
  await url.pressSequentially('https://new.example.org/push');
  // What the server's re-render does to the wizard pipeline on this update.
  Object.assign(api.state.pipelines[0] as Record<string, unknown>, {
    contents: '// rendered for https://new.example.org',
  });
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);

  await page.goBack();
  await expect(page.locator('.cm-content')).toContainText('new.example.org');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect.poll(() => api.calls('PipelineService/UpdatePipeline').length).toBe(1);
  expect(
    (api.calls('PipelineService/UpdatePipeline')[0].body as Record<string, unknown>).contents,
  ).toBe('// rendered for https://new.example.org');
});
