/**
 * The visual builder's toolbar collapses its secondary actions into a "More"
 * menu when the row is narrow (#251, Toolbar.tsx `toolbarLayout`): Flow check
 * and History below a full-width row — which includes the suite's default
 * 1280px Desktop Chrome viewport — and Simulate's sandbox run too on a narrow
 * one. The testids are the same either way; this finds the action wherever
 * the current layout put it.
 */
import { expect, type Locator, type Page } from '@playwright/test';

export async function toolbarAction(page: Page, testId: string): Promise<Locator> {
  const action = page.getByTestId(testId);
  if (!(await action.isVisible())) {
    const more = page.getByTestId('toolbar-more');
    if ((await more.getAttribute('aria-expanded')) !== 'true') await more.click();
  }
  await expect(action).toBeVisible();
  return action;
}
