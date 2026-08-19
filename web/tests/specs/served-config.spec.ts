import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

/*
 * 'contributing pipeline links are visible' was removed rather than fixed: the
 * collector detail page has no contributing-pipelines UI, and the test asserted
 * only that some heading existed, so it passed without touching the feature it
 * was named for. Recorded as F-CONTRIB in docs/project-status.md.
 */

test('served config renders as read-only text, not an editor', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    collectors: [s.collectors[0]],
    servedConfig: {
      content: 'prometheus.scrape "demo" {\n  targets = []\n}',
      hash: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    },
  });
  await page.goto(`/collectors/${s.collectors[0].id}`);

  // Served config is what the agent is being handed; it must never look editable.
  // It renders as a <pre>, so read-only is structural — the earlier version of
  // this test looked for a CodeMirror instance that was never there and fell
  // through to asserting a heading.
  const pre = page.locator('pre').filter({ hasText: 'prometheus.scrape' });
  await expect(pre).toBeVisible();
  await expect(page.locator('.cm-editor')).toHaveCount(0);
  await expect(page.locator('[contenteditable="true"]')).toHaveCount(0);
});

test('copying the served-config hash confirms with a toast', async ({ page, api, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    collectors: [s.collectors[0]],
    servedConfig: {
      content: '// served',
      hash: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    },
  });
  await page.goto(`/collectors/${s.collectors[0].id}`);

  await page.getByTestId('copy-hash-btn').click();
  await expect(page.locator('[data-sonner-toast]').first()).toContainText(/copied/i);
});
