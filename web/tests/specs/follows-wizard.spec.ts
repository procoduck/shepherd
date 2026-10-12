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
  // Review of #332: the list has no revisions, so it cannot tell a hand edit
  // from an older wizard version's text; it says only what is always true.
  // Red run: the row badge read "Hand-edited".
  test('badges a wizard pipeline that no longer follows its wizard, and no other', async ({
    page,
    api,
  }) => {
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
    await expect(edited).toHaveText('Not following wizard');
    await expect(edited).toHaveAccessibleDescription(
      'Destination changes no longer regenerate this pipeline; open it to see why.',
    );
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

  /** Changes the URL and saves; returns the dialog (closed unless refused). */
  async function changeURL(page: import('@playwright/test').Page) {
    await page.goto('/destinations');
    await page.getByRole('button', { name: 'Edit prom-prod' }).click();
    const dialog = page.getByRole('dialog', { name: 'Edit prom-prod' });
    const url = dialog.getByLabel('URL', { exact: true });
    await url.clear();
    await url.pressSequentially('https://mimir.example.org/push');
    await dialog.getByRole('button', { name: 'Save' }).click();
    return dialog;
  }

  const toast = (page: import('@playwright/test').Page) =>
    page.locator('[data-sonner-toast]').filter({ hasText: 'Destination updated' });

  test('counts the pipelines it regenerated', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    seedDestination(api, 3);
    await expect(await changeURL(page)).toHaveCount(0);
    await expect(toast(page)).toHaveText(/Destination updated — 3 pipelines regenerated/);
  });

  test('uses the singular for one', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    seedDestination(api, 1);
    await expect(await changeURL(page)).toHaveCount(0);
    await expect(toast(page)).toHaveText(/Destination updated — 1 pipeline regenerated/);
  });

  test('says nothing more when none was regenerated', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    seedDestination(api, 0);
    await expect(await changeURL(page)).toHaveCount(0);
    await expect(toast(page)).toBeVisible();
    await expect(toast(page)).not.toContainText('regenerated');
  });

  test('counts a change to the extra settings too', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    seedDestination(api, 2);
    await page.goto('/destinations');
    await page.getByRole('button', { name: 'Edit prom-prod' }).click();
    const dialog = page.getByRole('dialog', { name: 'Edit prom-prod' });
    await dialog.getByRole('button', { name: /TLS options/ }).click();
    await dialog.getByLabel(/Server name/).pressSequentially('mimir.internal.example');
    const url = dialog.getByLabel('URL', { exact: true });
    await url.clear();
    await url.pressSequentially('https://mimir.example.org/push');
    await dialog.getByRole('button', { name: 'Save' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(toast(page)).toHaveText(/Destination updated — 2 pipelines regenerated/);
  });

  test('is refused while a wizard pipeline using it is hand-edited', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      destinations: [destination({ id: 'dst-h', name: 'prom-prod', auth_mode: 'none' })],
      pipelines: [
        pipeline({
          id: 'pip-h',
          name: 'self-mon',
          source: 'wizard',
          follows_wizard: false,
          wizard_state: { metrics_dest_name: 'prom-prod' },
        }),
      ],
    });
    const dialog = await changeURL(page);
    await expect(dialog.getByTestId('form-error')).toContainText('edited by hand');
    await expect(toast(page)).toHaveCount(0);
  });
});

// Review of #332: a pipeline written before Shepherd recorded what its wizard
// wrote (migration 0030), by a wizard whose output has since changed (#289),
// no longer follows its wizard although nobody edited it: its current
// revision is the wizard's own. Red run: the badge said "Hand-edited" and no
// restore was offered (the last wizard revision was the current one).
test.describe('a wizard pipeline an older version of its wizard generated', () => {
  function seedOlderTemplate(api: { seed: (p: Record<string, unknown>) => void }) {
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      pipelines: [
        pipeline({
          id: 'pip-old',
          name: 'old',
          org_id: s.org.id,
          source: 'wizard',
          contents: WIZARD_TEXT,
          follows_wizard: false,
          revisions: [
            {
              revision: 1,
              changed_by: 'grace@example.com',
              changed_at: '2026-09-01T10:00:00Z',
              change_note: 'created',
              contents: WIZARD_TEXT,
            },
          ],
        }),
      ],
    });
  }

  test('is not called hand-edited, and says why it no longer follows', async ({ page, api }) => {
    await api.loginAs(reader);
    seedOlderTemplate(api);
    await page.goto('/pipelines/pip-old');
    const badge = page.getByTestId('pipeline-hand-edited-badge');
    await expect(badge).toHaveText('Not following wizard');
    await expect(badge).toHaveAccessibleDescription(/older version of its wizard/);
  });

  test('offers its current wizard revision, and restoring it re-attaches the pipeline', async ({
    page,
    api,
  }) => {
    await api.loginAs(orgEditor);
    seedOlderTemplate(api);
    await page.goto('/pipelines/pip-old');
    const offer = page.getByTestId('restore-wizard-version');
    await expect(offer).toContainText('older version');
    await offer.getByRole('button', { name: 'Restore last wizard version' }).click();
    const dialog = page.getByTestId('restore-dialog');
    await expect(dialog.getByTestId('restore-wizard-note')).toContainText('re-attaches');
    await dialog.getByTestId('confirm-restore-btn').click();
    await expect.poll(() => api.calls('PipelineService/RestoreRevision').length).toBe(1);
    expect(api.calls('PipelineService/RestoreRevision')[0].body).toMatchObject({ revision: 1 });
    await expect(page.getByTestId('pipeline-hand-edited-badge')).toHaveCount(0);
  });
});

// Review of #332: the restore's reply is the pipeline as it now is. Red run
// (cache only invalidated): while the refetch was in flight the page still
// held follows_wizard=false, and a quick Save after the restore hand-edited
// the re-attached pipeline without asking.
test('a Save right after restoring the wizard version asks first', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const edited = `${WIZARD_TEXT}\n// tuned by hand`;
  api.seed({
    orgs: [s.org],
    pipelines: [
      pipeline({
        id: 'pip-q',
        name: 'quick',
        org_id: s.org.id,
        source: 'wizard',
        contents: edited,
        follows_wizard: false,
        revisions: [
          {
            revision: 2,
            changed_by: 'ada@example.com',
            changed_at: '2026-10-02T10:00:00Z',
            change_note: 'updated',
            contents: edited,
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
  await page.goto('/pipelines/pip-q');
  const offer = page.getByTestId('restore-wizard-version');
  await expect(offer).toBeVisible();
  // From here on the page's refetch of the pipeline is slow.
  api.delay('POST', '/shepherd.mgmt.v1.PipelineService/GetPipeline', 5_000);
  await offer.getByRole('button', { name: 'Restore last wizard version' }).click();
  await page.getByTestId('restore-dialog').getByTestId('confirm-restore-btn').click();
  await expect.poll(() => api.calls('PipelineService/RestoreRevision').length).toBe(1);
  await expect(page.getByTestId('restore-dialog')).toHaveCount(0);

  await page.locator('.cm-content').click();
  await page.keyboard.press('End');
  await page.keyboard.type(' // again');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'Save a hand edit?' })).toBeVisible();
  expect(api.calls('PipelineService/UpdatePipeline')).toHaveLength(0);
});
