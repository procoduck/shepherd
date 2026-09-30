import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// #201: a validation for an older buffer that answers after a newer one used
// to win — the editor showed "1 problem: 8:1 expected }, got EOF" for text
// that no longer had eight lines, kept Save disabled and the gutter red, and
// nothing re-validated until Validate was clicked. Only the newest request's
// answer may land.
test('a slow validation of an older buffer does not overwrite a newer result', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-race', org_id: s.org.id, name: 'race', contents: '// ok' });
  api.seed({ orgs: [s.org], pipelines: [p] });

  // The broken buffer's answer takes 2.5s; the fixed one answers at once.
  let staleServed = false;
  api.override('POST', '/shepherd.mgmt.v1.PipelineService/ValidatePipeline', async (route) => {
    const req = route.request().postDataJSON() as { contents?: string };
    const broken = (req.contents ?? '').includes('broken {');
    if (broken) await new Promise((r) => setTimeout(r, 2_500));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(
        broken
          ? {
              valid: false,
              diagnostics: [{ line: 8, col: 1, message: 'expected }, got EOF', stage: 1 }],
            }
          : { valid: true, diagnostics: [] },
      ),
    });
    if (broken) staleServed = true;
  });

  await page.goto('/pipelines/pip-race');
  const editor = page.locator('.cm-content');
  await expect(editor).toBeVisible();
  await expect(page.getByText('No problems')).toBeVisible();

  const calls = () => api.calls('/shepherd.mgmt.v1.PipelineService/ValidatePipeline').length;
  const before = calls();
  await editor.click();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.type('broken {');
  // The broken buffer's validation is in flight...
  await expect.poll(calls).toBeGreaterThan(before);
  // ...when the text is fixed, and the fixed buffer's validation answers first.
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.type('// fixed');
  await expect.poll(calls).toBeGreaterThan(before + 1);

  // Let the stale answer arrive, then check it was ignored.
  await expect.poll(() => staleServed, { timeout: 10_000 }).toBe(true);
  await expect(page.getByText('No problems')).toBeVisible();
  await expect(page.getByText('expected }, got EOF')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Save' })).toBeEnabled();
  await expect(page.locator('.cm-lint-marker-error')).toHaveCount(0);
});
