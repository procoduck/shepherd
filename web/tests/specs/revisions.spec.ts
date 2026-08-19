import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

/*
 * Two sibling specs were removed here rather than fixed, because neither could
 * ever pass and neither tested what its name claimed:
 *
 *   'diff view is visible when two revisions exist' asserted the same locator as
 *   the test below (/revision|diff|history/i), so it was a duplicate wearing a
 *   different name. A real diff is also impossible today — see below.
 *
 *   'restore creates a new revision' ended by asserting the editor was visible,
 *   which says nothing about a revision being created. Restore is a stub: the
 *   button raises "Revision contents are not exposed by the API yet" and there
 *   is no RestoreRevision RPC.
 *
 * shepherd.mgmt.v1.PipelineRevision carries only revision/changed_by/changed_at/
 * change_note — no contents — so neither diff nor restore can be built until the
 * proto exposes them. Recorded as F-REVISIONS in docs/project-status.md.
 */

test('revision history lists each revision with its author and note', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [
      {
        ...s.pipelines[0],
        revisions: [
          {
            revision: 2,
            changed_by: 'ada@example.com',
            changed_at: '2026-08-18T10:00:00Z',
            change_note: 'tighten scrape interval',
          },
          {
            revision: 1,
            changed_by: 'grace@example.com',
            changed_at: '2026-08-17T10:00:00Z',
            change_note: 'created',
          },
        ],
      },
    ],
  });
  await page.goto(`/pipelines/${s.pipelines[0].id}`);

  // The panel is collapsed by default and its toggle carries the count.
  const toggle = page.getByRole('button', { name: /revision history \(2\)/i });
  await expect(toggle).toBeVisible();
  await toggle.click();

  await expect(page.getByText('#2')).toBeVisible();
  await expect(page.getByText('ada@example.com')).toBeVisible();
  await expect(page.getByText('tighten scrape interval')).toBeVisible();
  await expect(page.getByText('#1')).toBeVisible();
  await expect(page.getByText('grace@example.com')).toBeVisible();
});
