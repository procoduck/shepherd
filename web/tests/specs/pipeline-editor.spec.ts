import { basicScenario, pipeline } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

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
