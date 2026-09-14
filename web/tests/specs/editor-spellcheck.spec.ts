import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// F14: pin that the editor's .cm-content carries spellcheck="false",
// autocorrect="off" and autocapitalize="off", which AlloyEditor.tsx now sets
// explicitly via EditorView.contentAttributes. This was already green before
// that change — the pinned @codemirror/view (6.43.11) defaults contenteditable
// to the same values in updateAttrs() — so the spec guards against an
// upstream default ever changing, not a spell-check bug actually seen in
// v0.6.0. (The walkthrough's underline was most likely CodeMirror's own lint
// decoration on a diagnosed identifier, not the browser's native spell-checker.)
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
