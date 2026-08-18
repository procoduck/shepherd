import type { Page } from '@playwright/test';

export const editor = (page: Page, nth = 0) => {
  const root = page.locator('.cm-editor').nth(nth);
  const content = root.locator('.cm-content');
  return {
    root,
    content,
    async focus() {
      await content.click();
    },
    async type(text: string) {
      await this.focus();
      await page.keyboard.type(text);
    },
    async setValue(text: string) {
      await this.focus();
      await page.keyboard.press('ControlOrMeta+a');
      await page.keyboard.type(text);
    },
    async openCompletion() {
      await page.keyboard.press('ControlOrMeta+Space');
    },
    completions: page.locator('.cm-tooltip-autocomplete li'),
    async accept(label: string) {
      await page.locator('.cm-tooltip-autocomplete li', { hasText: label }).click();
    },
    diagnostics: root.locator('.cm-lintRange-error'),
    async text(): Promise<string> {
      // DECISION: innerText may include zero-width spaces from CodeMirror; strip them
      return (await content.innerText()).replace(/\u200b/g, '');
    },
  };
};
