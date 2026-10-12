// follows-wizard.spec.ts — #301 Pipeline.follows_wizard and #309
// Destination.regenerated_pipelines, the two server answers that replaced
// guesses in the UI.
//
// #301: the "Hand-edited" badge and the "Save a hand edit?" question used to
// compare the pipeline's text with its last wizard-written revision. The
// server decides by the render fingerprint, so a pipeline hand-edited and
// then edited back to its wizard text read as still following (no badge, the
// question asked again) while the server refused it as hand-edited. Both now
// read follows_wizard, and the pipelines list shows the badge too.
//
// Red run (before the UI change): the list specs found no badge in any row,
// the edited-back pipeline showed no header badge and asked before saving,
// and the toast specs read "Destination updated" with no count.
import { basicScenario, destination, pipeline } from '../fixtures/factories';
import { orgAdmin, orgEditor, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

const WIZARD_TEXT = 'prometheus.remote_write "metrics" { endpoint { url = "https://old" } }';

test.describe('the pipelines list', () => {
  test('badges a hand-edited wizard pipeline, and no other', async ({ page, api }) => {
    await api.loginAs(reader);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      pipelines: [
        pipeline({ id: 'pip-edited', name: 'edited', source: 'wizard', follows_wizard: false }),
        pipeline({ id: 'pip-follows', name: 'follows', source: 'wizard' }),
        pipeline({ id: 'pip-ui', name: 'plain', source: 'ui' }),
      ],
    });
    await page.goto('/pipelines');

    const edited = page
      .getByTestId('pipeline-row-edited')
      .getByTestId('pipeline-hand-edited-badge');
    await expect(edited).toHaveText('Hand-edited');
    await expect(edited).toHaveAccessibleDescription(/no longer follows destination changes/);
    await expect(page.getByTestId('pipeline-hand-edited-badge')).toHaveCount(1);
    await expect(page.getByTestId('pipeline-row-follows')).toBeVisible();
  });
});

test.describe('a wizard pipeline edited back to its wizard text', () => {
  // The server cleared its fingerprint at the hand edit and refuses it as
  // hand-edited until a wizard revision is restored; the UI now agrees.
  function seedEditedBack(api: { seed: (p: Record<string, unknown>) => void }) {
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      pipelines: [
        pipeline({
          id: 'pip-back',
          name: 'back',
          org_id: s.org.id,
          source: 'wizard',
          contents: WIZARD_TEXT,
          follows_wizard: false,
          revisions: [
            {
              revision: 3,
              changed_by: 'ada@example.com',
              changed_at: '2026-10-03T10:00:00Z',
              change_note: 'updated',
              contents: WIZARD_TEXT,
            },
            {
              revision: 2,
              changed_by: 'ada@example.com',
              changed_at: '2026-10-02T10:00:00Z',
              change_note: 'updated',
              contents: `${WIZARD_TEXT}\n// tuned by hand`,
            },
            {
              revision: 1,
              changed_by: 'grace@example.com',
              changed_at: '2026-10-01T10:00:00Z',
              change_note: 'created',
              contents: WIZARD_TEXT,
            },
          ],
        }),
      ],
    });
  }

  test('is badged in its header', async ({ page, api }) => {
    await api.loginAs(reader);
    seedEditedBack(api);
    await page.goto('/pipelines/pip-back');
    const badge = page.getByTestId('pipeline-hand-edited-badge');
    await expect(badge).toHaveText('Hand-edited');
    await expect(badge).toHaveAccessibleDescription(/revision #1/);
  });

  test('saves a further edit without asking: it no longer follows its wizard', async ({
    page,
    api,
  }) => {
    await api.loginAs(orgEditor);
    seedEditedBack(api);
    await page.goto('/pipelines/pip-back');
    await expect(page.locator('.cm-content')).toContainText('remote_write');
    await page.locator('.cm-content').click();
    await page.keyboard.press('End');
    await page.keyboard.type(' // again');
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await expect.poll(() => api.calls('PipelineService/UpdatePipeline').length).toBe(1);
    await expect(page.getByRole('dialog', { name: 'Save a hand edit?' })).toHaveCount(0);
  });

  test('offers its last wizard version, and restoring it clears the badge', async ({
    page,
    api,
  }) => {
    await api.loginAs(orgEditor);
    seedEditedBack(api);
    await page.goto('/pipelines/pip-back');
    await page
      .getByTestId('restore-wizard-version')
      .getByRole('button', { name: 'Restore last wizard version' })
      .click();
    await page.getByTestId('restore-dialog').getByTestId('confirm-restore-btn').click();
    await expect.poll(() => api.calls('PipelineService/RestoreRevision').length).toBe(1);
    await expect(page.getByTestId('pipeline-hand-edited-badge')).toHaveCount(0);
    await expect(page.getByTestId('restore-wizard-version')).toHaveCount(0);
  });
});

test.describe('the "Destination updated" confirmation', () => {
  function seedDestination(
    api: { seed: (p: Record<string, unknown>) => void },
    users: number,
  ): void {
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      destinations: [destination({ id: 'dst-u', name: 'prom-prod', auth_mode: 'none' })],
      pipelines: Array.from({ length: users }, (_, i) =>
        pipeline({
          id: `pip-u${i}`,
          name: `self-mon-${i}`,
          source: 'wizard',
          wizard_state: { metrics_dest_name: 'prom-prod' },
        }),
      ),
    });
  }

  async function changeURL(page: import('@playwright/test').Page) {
    await page.goto('/destinations');
    await page.getByRole('button', { name: 'Edit prom-prod' }).click();
    const dialog = page.getByRole('dialog', { name: 'Edit prom-prod' });
    const url = dialog.getByLabel('URL', { exact: true });
    await url.clear();
    await url.pressSequentially('https://mimir.example.org/push');
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog).toHaveCount(0);
  }

  const toast = (page: import('@playwright/test').Page) =>
    page.locator('[data-sonner-toast]').filter({ hasText: 'Destination updated' });

  test('counts the pipelines it regenerated', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    seedDestination(api, 3);
    await changeURL(page);
    await expect(toast(page)).toHaveText(/Destination updated — 3 pipelines regenerated/);
  });

  test('uses the singular for one', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    seedDestination(api, 1);
    await changeURL(page);
    await expect(toast(page)).toHaveText(/Destination updated — 1 pipeline regenerated/);
  });

  test('says nothing more when none was regenerated', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    seedDestination(api, 0);
    await changeURL(page);
    await expect(toast(page)).toBeVisible();
    await expect(toast(page)).not.toContainText('regenerated');
  });
});
