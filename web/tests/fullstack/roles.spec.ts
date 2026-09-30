/**
 * Fullstack: RBAC affordances and enforcement for the seeded local
 * editor/viewer personas (W7-03).
 *
 * - editor: logs in through the real login form, sees the New-pipeline
 *   affordance on /pipelines, creates and saves a fresh pipeline through the
 *   real editor UI, and the save actually persists server-side.
 * - viewer: sees no write affordances on /pipelines, and a direct
 *   PipelineService.UpdatePipeline call (with the viewer's own session
 *   cookie) is rejected 403 by the real server — not merely hidden
 *   client-side.
 * - both personas are denied /admin/orgs (app-admin only): the route guard
 *   shows its denial page at that URL (#206 — it used to redirect to '/').
 *
 * Red run (recorded, not re-run by CI):
 *   - Login-level: with dev-reset run before the seed (no `dev seed` step),
 *     editor/viewer do not exist yet and POST /api/auth/local/login for
 *     either returns 401 — loginAs's own error path, proving these tests
 *     exercise a real account rather than an always-succeeding stub.
 *   - Control-level: temporarily drop the `|| o.role === 'editor'` clause
 *     from useCanWrite in web/src/hooks/useOrg.ts, rebuild shepherd:local,
 *     restart the dev stack's shepherd container. The editor-affordance case
 *     below fails (no pipeline-new testid — editor now reads as a viewer).
 *     Restored afterward and rebuilt back to green.
 */
import type { Page } from '@playwright/test';
import { DEV_EDITOR, DEV_VIEWER, expect, getMe, loginAs, rpc, test } from './fixtures';

async function getPlatformOrgId(page: Page): Promise<string> {
  const me = await getMe(page);
  const org = me.orgs.find((o) => o.name === 'platform-org');
  if (!org) throw new Error('dev seed must provide platform-org for the editor/viewer personas');
  return org.id;
}

/** A denial is RequireRole's visible [data-testid="route-denied"] page,
 * shown at the URL that was asked for (#206: it no longer redirects to '/',
 * which read as a broken link). The page component itself is never mounted. */
async function expectRouteDenied(page: Page, route: string) {
  await page.goto(route);
  await expect(page.getByTestId('route-denied')).toBeVisible({ timeout: 5000 });
  expect(new URL(page.url()).pathname).toBe(route);
}

test.describe('roles: editor', () => {
  test('editor logs in via the real form, sees New pipeline, creates and saves one', async ({
    page,
  }) => {
    // The real login form, not the loginAs() API shortcut — this is the one
    // case in this file the task calls out as needing the actual browser
    // journey (auth-journey.spec.ts exercises the same form for admin).
    await page.goto('/');
    await expect(page).toHaveURL(/\/login/);
    await page.getByTestId('local-username').fill(DEV_EDITOR.username);
    await page.getByTestId('local-password').fill(DEV_EDITOR.password);
    await page.getByTestId('local-login-submit').click();
    await expect(page).toHaveURL('/');

    await page.goto('/pipelines');
    await page.waitForLoadState('networkidle');
    // Editor is org-editor on platform-org (useCanWrite: admin OR editor) —
    // the write affordance must be visible, not just present in the DOM.
    await expect(page.getByTestId('pipeline-new')).toBeVisible();

    const orgId = await getPlatformOrgId(page);
    // Alloy component block labels must be valid identifiers even quoted —
    // a dash is rejected ("expected block label to be a valid identifier"),
    // which is exactly why every existing fullstack spec that embeds a
    // generated name as a block label (pipelines.spec.ts's fs_pipe_...) uses
    // underscores. The pipeline's own `name` field has no such restriction;
    // this test's contents reuse it as the block label, so it needs one too.
    const name = `fs_role_editor_${Date.now()}`;

    await page.getByTestId('pipeline-new').click();
    await expect(page).toHaveURL('/pipelines/new');
    await page.getByPlaceholder('my-pipeline').fill(name);

    const editor = page.locator('.cm-editor');
    await expect(editor).toBeVisible();
    await editor.click();
    const validateResponse = page.waitForResponse((r) =>
      r.url().includes('PipelineService/ValidatePipeline'),
    );
    await page.keyboard.type(`prometheus.exporter.self "${name}" { }`);
    // The debounced validate() fires 800ms after the last keystroke; wait for
    // that actual round trip rather than the "No problems" text, which is
    // also the indicator's default (diagnostics starts as []) and would
    // otherwise pass vacuously before real validation ever ran.
    await validateResponse;
    await expect(page.getByText(/No problems/i)).toBeVisible({ timeout: 5000 });

    const saveButton = page.getByRole('button', { name: /Save/i });
    await expect(saveButton).toBeEnabled();
    await saveButton.click();

    // A successful create navigates to /pipelines/$id (see
    // PipelineEditorPage's saveMutation.onSuccess) — the real proof the save
    // round-tripped through the server, not just that the button was clicked.
    await expect(page).toHaveURL(/\/pipelines\/[0-9a-f-]{36}$/, { timeout: 10000 });
    const pipelineId = page.url().split('/pipelines/')[1];

    // Confirm it actually persisted server-side, then clean it up.
    const getResp = await rpc(page, 'PipelineService', 'GetPipeline', { orgId, id: pipelineId });
    expect(getResp.status()).toBe(200);
    const saved = (await getResp.json()) as { name: string };
    expect(saved.name).toBe(name);

    await rpc(page, 'PipelineService', 'DeletePipeline', { orgId, id: pipelineId });
  });
});

test.describe('roles: viewer', () => {
  test('viewer sees no write affordances, and a direct UpdatePipeline is rejected 403', async ({
    page,
  }) => {
    await loginAs(page, DEV_VIEWER.username, DEV_VIEWER.password);
    const orgId = await getPlatformOrgId(page);

    await page.goto('/pipelines');
    await page.waitForLoadState('networkidle');
    // Positive control first: the list rendered real rows for the viewer, so
    // the absences below are about role gating, not a blank or errored page.
    await expect(page.getByTestId(/^pipeline-row-/).first()).toBeVisible();
    // Neither authoring affordance renders for a viewer (useCanWrite false).
    await expect(page.getByTestId('pipeline-new')).toHaveCount(0);
    await expect(page.getByTestId('pipeline-visual-builder')).toHaveCount(0);
    // The enabled/disabled column falls back to plain text, not a toggle
    // button, for a non-writer (PipelinesPage's pipelineColumns).
    await expect(
      page.locator('button[aria-label="Enable"], button[aria-label="Disable"]'),
    ).toHaveCount(0);

    const listResp = await rpc(page, 'PipelineService', 'ListPipelines', { orgId });
    expect(listResp.status()).toBe(200);
    // protojson omits empty fields: a pipeline with no matchers has no
    // `matchers` key at all.
    const list = (await listResp.json()) as {
      items?: Array<{ id: string; name: string; contents?: string; matchers?: string[] }>;
    };
    const target = list.items?.[0];
    if (!target) throw new Error('dev seed must provide at least one pipeline on platform-org');

    // page.request shares the browser context's cookies — this really is
    // the viewer's own session, not an admin bypass (W7-03's whole point).
    const putResp = await rpc(page, 'PipelineService', 'UpdatePipeline', {
      orgId,
      id: target.id,
      name: target.name,
      contents: target.contents ?? '',
      matchers: target.matchers ?? [],
    });
    expect(putResp.status()).toBe(403);
    const body = (await putResp.json()) as { code: string };
    expect(body.code).toBe('permission_denied');
  });
});

test.describe('roles: /admin/orgs is app-admin only', () => {
  test('editor is denied /admin/orgs and lands on /', async ({ page }) => {
    await loginAs(page, DEV_EDITOR.username, DEV_EDITOR.password);
    await expectRouteDenied(page, '/admin/orgs');
  });

  test('viewer is denied /admin/orgs and lands on /', async ({ page }) => {
    await loginAs(page, DEV_VIEWER.username, DEV_VIEWER.password);
    await expectRouteDenied(page, '/admin/orgs');
  });
});
