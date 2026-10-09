/**
 * Fullstack: editing a pipeline's matchers in the classic editor moves it
 * from one dev collector's pipeline list to the other (W7-10).
 *
 * "Pipeline list" is proven via PipelineService.PreviewMatches — the same merge-matching logic the
 * server uses to decide which collectors a pipeline's config is served to,
 * not a client-side re-implementation of matcher evaluation (which
 * docs/frontend-testing.md §8 rules out for this layer).
 *
 * Red run (recorded, not re-run by CI):
 *   Control-level: in web/src/pages/PipelineEditorPage.tsx, drop `matchers`
 *   from saveMutation's body (`{ orgId, name, contents }` instead of
 *   `{ orgId, name, contents, matchers }`), rebuild, restart the dev
 *   stack's shepherd container (the SPA is baked into the image).
 *   The generated client still sends `matchers: []` for the omitted field
 *   (proto3 default), so the edited pipeline's matcher list is cleared
 *   instead of updated: preview-matches after save returns NO collectors at
 *   all — the assertion that it now names the logs collector fails loudly,
 *   not silently. Reverted and rebuilt back to green afterward.
 */
import { expect, getMe, loginAsAdmin, rpc, test } from './fixtures';

interface MatchedCollector {
  cluster: string;
  role: string;
  id: string;
}

test.describe('matcher-edit', () => {
  test('editing matchers moves a pipeline from the metrics collector to the logs collector', async ({
    page,
  }) => {
    await loginAsAdmin(page);

    const me = await getMe(page);
    const org = me.orgs.find((o) => o.name === 'platform-org');
    if (!org) throw new Error('dev seed must provide platform-org');
    const orgId = org.id;

    // Alloy component block labels must be valid identifiers even quoted (a
    // dash is rejected by `alloy validate`), and this pipeline's contents
    // reuse `name` as its own block label below — underscores only, matching
    // pipelines.spec.ts's fs_pipe_... convention.
    const name = `fs_matcher_edit_${Date.now()}`;
    const createResp = await rpc(page, 'PipelineService', 'CreatePipeline', {
      orgId,
      name,
      contents: `prometheus.exporter.self "${name}" { }`,
      matchers: [`cluster="prod-eu-1"`, `role="metrics"`],
    });
    expect(createResp.status()).toBe(200);
    const pipeline = (await createResp.json()) as { id: string };

    try {
      const before = await rpc(page, 'PipelineService', 'PreviewMatches', {
        orgId,
        id: pipeline.id,
      });
      expect(before.status()).toBe(200);
      // protojson omits an empty collectors list — absent means none matched.
      const beforeMatches =
        ((await before.json()) as { collectors?: MatchedCollector[] }).collectors ?? [];
      expect(beforeMatches.some((c) => c.role === 'metrics')).toBe(true);
      expect(beforeMatches.some((c) => c.role === 'logs')).toBe(false);

      await page.goto(`/pipelines/${pipeline.id}`);
      // The admin belongs to more than one org, and with no persisted
      // selection in this fresh context, useOrg falls back to orgs[0] —
      // GetMe sorts by name, so that is data-eng, not platform-org, and
      // this pipeline's own org-scoped queries would 404 without switching
      // (walkthrough.spec.ts hits the identical default, W7-10).
      await page.getByTestId('org-switcher').selectOption({ label: 'Platform Engineering' });
      await page.waitForLoadState('networkidle');
      await expect(page.locator('.cm-editor')).toBeVisible();

      // Remove the role="metrics" matcher chip, then add role="logs".
      const metricsMatcher = page.getByText('role="metrics"', { exact: true });
      await expect(metricsMatcher).toBeVisible();
      await page.getByRole('button', { name: 'Remove matcher role="metrics"' }).click();
      await expect(metricsMatcher).toHaveCount(0);

      const matcherInput = page.getByPlaceholder(/Enter to add/);
      await matcherInput.fill('role="logs"');
      await matcherInput.press('Enter');
      await expect(page.getByText('role="logs"', { exact: true })).toBeVisible();

      const saveButton = page.getByRole('button', { name: /Save/i });
      await expect(saveButton).toBeEnabled();
      await saveButton.click();
      await expect(page.getByText(/Pipeline saved/i)).toBeVisible({ timeout: 10000 });

      const after = await rpc(page, 'PipelineService', 'PreviewMatches', {
        orgId,
        id: pipeline.id,
      });
      expect(after.status()).toBe(200);
      const afterMatches =
        ((await after.json()) as { collectors?: MatchedCollector[] }).collectors ?? [];
      expect(afterMatches.some((c) => c.role === 'logs')).toBe(true);
      expect(afterMatches.some((c) => c.role === 'metrics')).toBe(false);
    } finally {
      await rpc(page, 'PipelineService', 'DeletePipeline', { orgId, id: pipeline.id });
    }
  });
});
