import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// F14: the text editor's contenteditable had no EditorView.contentAttributes,
// so CodeMirror inherited the browser's default spellcheck/autocorrect
// behaviour — every Alloy identifier got a red squiggle. AlloyEditor.tsx must
// turn spellcheck, autocorrect and autocapitalize off explicitly.
test('editor turns off the browser spell-check underline', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [basicScenario().org] });
  await page.goto('/pipelines/new');
  await page.waitForSelector('.cm-editor', { timeout: 5000 });
  const content = page.locator('.cm-content');
  await expect(content).toHaveAttribute('spellcheck', 'false');
  await expect(content).toHaveAttribute('autocorrect', 'off');
  await expect(content).toHaveAttribute('autocapitalize', 'off');
});
