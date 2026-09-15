/**
 * Fullstack: revision contents and restore (F-REVISIONS).
 *
 * Three scenarios share one pipeline, created by the first test and reused
 * (in declared order — playwright.fullstack.config.ts runs this suite with
 * fullyParallel: false, workers: 1) by the two that follow, because the
 * revision counts and contents each scenario asserts on are exactly what
 * the previous scenario's actions produced:
 *
 *   1. REST round trip  — create (rev 1, contents A), PUT (rev 2, contents
 *      B), GetRevision(1), RestoreRevision(1) (-> rev 3, contents A again),
 *      ListRevisions, GetPipeline, and the audit trail.
 *   2. UI restore        — the pipeline now holds revisions [3, 2, 1]
 *      (newest first — ListPipelineRevisions orders by revision DESC), so
 *      "View diff" on the *second* row is revision #2, whose contents are
 *      B. Restoring it through the editor UI creates revision 4.
 *   3. Viewer refused    — a reader-tier session may not restore revision 1
 *      (still present, contents A, untouched by either restore above).
 *
 * Red run (recorded here, not re-run by CI — see the note at D-6 in
 * docs/archive/plans/2026-09-11-f-revisions.md): temporarily change the asserted
 * change_note in scenario 1 from 'Restored from revision 1' to 'Restored
 * from revision 2' — the round-trip test fails on the real note the server
 * wrote. Revert immediately after capturing the failure text.
 *
 * NOT RUN by this session: `make test-fullstack` builds the stack from
 * this branch (feat/revisions-docs-tests), which carries only the
 * foundation commit — GetRevision/RestoreRevision are still `Unimplemented`
 * stubs, their REST routes do not exist in router.go yet, and the pipeline
 * editor's View-diff/Restore-dialog UI is not built (all three land from
 * the parallel backend/web packages). Running against this branch would
 * fail on missing routes/markup, not on a defect in this spec. The
 * orchestrator must run `make test-fullstack` after backend, web and
 * docs-tests have all merged into `feat/revisions` (see the plan's §6
 * integration gate) before this suite can go green, and the ledger must
 * not claim fullstack coverage for F-REVISIONS until it has.
 */
import { DEV_VIEWER, expect, loginAs, loginAsAdmin, test } from './fixtures';

interface Me {
  orgs: Array<{ id: string; name: string }>;
}

const MARKER_A = 'marker-revisions-a';
const MARKER_B = 'marker-revisions-b';

test.describe
  .serial('revisions: contents and restore', () => {
    let orgId = '';
    let pipelineId = '';
    const pipeName = `fs_revisions_${Date.now()}`;
    const contentsA = `prometheus.exporter.self "${pipeName}" { }\n// ${MARKER_A}`;
    const contentsB = `prometheus.exporter.self "${pipeName}" { }\n// ${MARKER_B}`;

    test('REST round trip', async ({ page }) => {
      await loginAsAdmin(page);

      const meResp = await page.request.get('/api/me', {
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
      });
      const me = (await meResp.json()) as Me;
      const org = me.orgs.find((o) => o.name === 'platform-org');
      if (!org) throw new Error('dev seed must provide at least one org');
      orgId = org.id;

      // Create — revision 1, contents A.
      const createResp = await page.request.post(`/api/orgs/${orgId}/pipelines`, {
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        data: { name: pipeName, contents: contentsA, matchers: [] },
      });
      expect(createResp.status()).toBe(201);
      const created = (await createResp.json()) as { id: string; contents: string };
      pipelineId = created.id;
      expect(created.contents).toBe(contentsA);

      // Update — revision 2, contents B.
      const updateResp = await page.request.put(`/api/orgs/${orgId}/pipelines/${pipelineId}`, {
        headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
        data: { name: pipeName, contents: contentsB, matchers: [] },
      });
      expect(updateResp.status()).toBe(200);
      const updated = (await updateResp.json()) as { contents: string };
      expect(updated.contents).toBe(contentsB);

      // GetRevision(1) — the old row's own contents, never mutated by the
      // update above.
      const rev1Resp = await page.request.get(
        `/api/orgs/${orgId}/pipelines/${pipelineId}/revisions/1`,
        { headers: { 'X-Requested-With': 'XMLHttpRequest' } },
      );
      expect(rev1Resp.status()).toBe(200);
      const rev1 = (await rev1Resp.json()) as { revision: number; contents: string };
      expect(rev1.revision).toBe(1);
      expect(rev1.contents).toBe(contentsA);

      // RestoreRevision(1) — writes a NEW revision (3) from revision 1's
      // contents; the response is the updated Pipeline.
      const restoreResp = await page.request.post(
        `/api/orgs/${orgId}/pipelines/${pipelineId}/revisions/1/restore`,
        { headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' } },
      );
      expect(restoreResp.status()).toBe(200);
      const restored = (await restoreResp.json()) as { contents: string };
      expect(restored.contents).toBe(contentsA);

      // ListRevisions — 3 items, newest first, and the newest one's
      // change_note names the revision it was restored from. This is the
      // red-run assertion described at the top of the file: change the
      // expected string below to 'Restored from revision 2' to see it fail
      // on the server's real note.
      const revsResp = await page.request.get(
        `/api/orgs/${orgId}/pipelines/${pipelineId}/revisions`,
        { headers: { 'X-Requested-With': 'XMLHttpRequest' } },
      );
      expect(revsResp.status()).toBe(200);
      const revs = (await revsResp.json()) as {
        items: Array<{ revision: number; change_note: string }>;
      };
      expect(revs.items.length).toBe(3);
      expect(revs.items[0].change_note).toBe('Restored from revision 1');

      // GetPipeline reflects the restore.
      const getResp = await page.request.get(`/api/orgs/${orgId}/pipelines/${pipelineId}`, {
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
      });
      expect(getResp.status()).toBe(200);
      const got = (await getResp.json()) as { contents: string };
      expect(got.contents).toBe(contentsA);

      // The restore wrote a pipeline.restore audit row for this pipeline.
      const auditResp = await page.request.get(`/api/orgs/${orgId}/audit?action=pipeline.restore`, {
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
      });
      expect(auditResp.status()).toBe(200);
      const audit = (await auditResp.json()) as {
        items: Array<{ action: string; resource_id: string }>;
      };
      expect(
        audit.items.some((i) => i.action === 'pipeline.restore' && i.resource_id === pipelineId),
      ).toBe(true);
    });

    test('UI restore', async ({ page }) => {
      await loginAsAdmin(page);
      // The admin's ambient org is whichever the switcher last stored (the
      // dev seed's first org is Data Engineering); the pipeline lives in
      // Platform Engineering, and the editor page loads it under the ambient
      // org, so select it first — same as matcher-edit and rollout do.
      await page.goto('/pipelines');
      await page.getByTestId('org-switcher').selectOption({ label: 'Platform Engineering' });
      await page.goto(`/pipelines/${pipelineId}`);
      await page.waitForLoadState('networkidle');

      // Revisions after scenario 1: [3 (restored from 1, contents A),
      // 2 (contents B), 1 (contents A)] — newest first.
      const toggle = page.getByRole('button', { name: /revision history \(3\)/i });
      await expect(toggle).toBeVisible();
      await toggle.click();

      const viewDiffButtons = page.getByTestId('view-revision-btn');
      await expect(viewDiffButtons).toHaveCount(3);
      // The second row (index 1) is revision #2, contents B.
      await viewDiffButtons.nth(1).click();

      const diff = page.getByTestId('revision-diff');
      await expect(diff).toBeVisible();
      await expect(page.getByText(/revision #2/i)).toBeVisible();

      // Restore this revision -> confirm dialog -> Restore. Pin the index to
      // revision #2 by count (so a control added/removed elsewhere shifts
      // this assertion instead of silently restoring the wrong revision) and
      // by the dialog's own wording, not by position alone.
      // The web package ships ONE restore control, in the diff pane's
      // header, for the revision currently being viewed — not one per row.
      // The row's "View diff" click above is what pins this to revision #2;
      // the dialog wording below re-asserts it.
      const restoreButtons = page.getByTestId('restore-btn');
      await expect(restoreButtons).toHaveCount(1);
      await restoreButtons.first().click();
      const dialog = page.getByTestId('restore-dialog');
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText(/restore revision #2/i);
      await dialog.getByRole('button', { name: 'Restore', exact: true }).click();

      // Wait for the diff pane to close (the mutation's onSuccess clears the
      // selected revision) BEFORE asserting on `.cm-content`.
      // @codemirror/merge's MergeView renders two EditorViews of its own, so
      // while the diff is still mounted `.cm-content` matches more than one
      // element — a strict-mode violation — and its left pane holds the old
      // revision's MARKER_B text regardless of whether the restore actually
      // reseeded the editor. Confirming the diff is gone first makes
      // `.cm-content` resolve to exactly the real editor pane, so the
      // following assertion is both non-flaky and actually proves the
      // editor was reseeded.
      await expect(diff).toBeHidden();
      await expect(page.locator('.cm-content')).toContainText(MARKER_B);
      await expect(page.getByRole('button', { name: /revision history \(4\)/i })).toBeVisible();
    });

    test('viewer refused', async ({ page }) => {
      await loginAs(page, DEV_VIEWER.username, DEV_VIEWER.password);

      // page.request shares the browser context's cookies — this really is
      // the viewer's own session, not an admin bypass.
      const resp = await page.request.post(
        `/api/orgs/${orgId}/pipelines/${pipelineId}/revisions/1/restore`,
        { headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' } },
      );
      expect(resp.status()).toBe(403);
    });

    test.afterAll(async ({ browser }) => {
      if (!pipelineId || !orgId) return;
      const page = await browser.newPage();
      await loginAsAdmin(page);
      await page.request.delete(`/api/orgs/${orgId}/pipelines/${pipelineId}`, {
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
      });
      await page.close();
    });
  });
