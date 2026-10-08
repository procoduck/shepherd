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

// The newest-request rule above is not enough: an answer for text the user
// has typed past since — before the debounce sent anything newer — used to
// land too, showing problems for text that no longer exists and keeping
// Save disabled on them. An answer only lands for the buffer it was asked of.
test('a validation answer for text typed past since does not land', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({ id: 'pip-past', org_id: s.org.id, name: 'past', contents: '// ok' });
  api.seed({ orgs: [s.org], pipelines: [p] });

  // `broken` answers when released; every other buffer's answer is held
  // for the rest of the test, so nothing newer can mask the stale one.
  let release: () => void = () => undefined;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  const never = new Promise<void>(() => undefined);
  let staleServed = false;
  api.override('POST', '/shepherd.mgmt.v1.PipelineService/ValidatePipeline', async (route) => {
    const req = route.request().postDataJSON() as { contents?: string };
    if (req.contents !== 'broken') {
      await never;
      return;
    }
    await gate;
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        valid: false,
        diagnostics: [{ line: 1, col: 8, message: 'unexpected identifier', stage: 1 }],
      }),
    });
    staleServed = true;
  });

  await page.goto('/pipelines/pip-past');
  const editor = page.locator('.cm-content');
  await expect(editor).toBeVisible();
  const calls = () => api.calls('/shepherd.mgmt.v1.PipelineService/ValidatePipeline').length;
  const before = calls();
  await editor.click();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.type('broken');
  await expect.poll(calls).toBeGreaterThan(before);
  // Type on, then let the answer for the old text arrive at once — inside
  // the 800ms debounce, so no newer request has been sent.
  await page.keyboard.type(' no more');
  release();
  await expect.poll(() => staleServed).toBe(true);

  await expect(page.getByText('unexpected identifier')).toHaveCount(0);
  await expect(page.locator('.cm-lint-marker-error')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Save' })).toBeEnabled();
});
