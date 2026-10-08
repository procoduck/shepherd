// editor-diagnostics-replace.spec.ts — M5 (2026-10-08 walkthrough): server
// diagnostics are positions in the text that was validated. Applied to a
// different (shorter) buffer they pointed past its end and CodeMirror threw
// `RangeError: Invalid position 93 in document of length 80`; and after the
// buffer was replaced a stale problem stayed listed until the next validation.
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// 80 characters, six lines; the sixth is 5 long, so 6:15 lies past the end of
// the line AND of the document (75 + 14 = 89).
const shortDoc = `${'abcdefghijklmn\n'.repeat(5)}xxxxx`;

test('a diagnostic past the end of the document does not throw', async ({ page, api }) => {
  const pageErrors: string[] = [];
  page.on('pageerror', (e) => pageErrors.push(e.message));
  // CodeMirror reports an exception in a lint run through console.error.
  page.on('console', (m) => {
    if (m.type() === 'error') pageErrors.push(m.text());
  });
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-m5', org_id: s.org.id, name: 'm5', contents: shortDoc });
  api.seed({
    orgs: [s.org],
    pipelines: [p],
    validateResult: {
      valid: false,
      diagnostics: [{ line: 6, col: 15, message: 'regexp', stage: 2 }],
    },
  });
  await page.goto('/pipelines/pip-m5');
  await expect(page.getByText('6:15')).toBeVisible();
  // The lint gutter marks the clamped position, which proves the linter ran
  // over the diagnostic (the RangeError fired from exactly that run).
  await expect.soft(page.locator('.cm-lint-marker-error')).toHaveCount(1);
  expect(pageErrors).toEqual([]);
});

test('replacing the buffer clears problems reported for the old text', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-m5-fmt',
    org_id: s.org.id,
    name: 'm5-fmt',
    contents: 'prometheus.relabel "a" { regex = "(" }',
  });
  api.seed({
    orgs: [s.org],
    pipelines: [p],
    validateResult: {
      valid: false,
      diagnostics: [{ line: 1, col: 15, message: 'regexp', stage: 2 }],
    },
  });
  await page.goto('/pipelines/pip-m5-fmt');
  await expect(page.getByText('regexp')).toBeVisible();

  // Hold the re-validation Format triggers, so what is on screen between the
  // replacement and its answer is observable.
  let release: () => void = () => undefined;
  const held = new Promise<void>((r) => {
    release = r;
  });
  await page.route('**/shepherd.mgmt.v1.PipelineService/ValidatePipeline', async (route) => {
    await held;
    await route.fallback();
  });
  api.seed({ validateResult: { valid: true, diagnostics: [] } });

  await page.getByTestId('format-btn').click();
  await expect(page.locator('.cm-content')).toContainText('// formatted');
  await expect(page.getByText('regexp')).toHaveCount(0);
  await expect(page.locator('.cm-lint-marker-error')).toHaveCount(0);

  release();
  await expect(page.getByText(/No problems/i)).toBeVisible();
});
