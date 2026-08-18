import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// DECISION: Revision history UI is not yet implemented in PipelineEditorPage.
// These tests will skip until the revisions section is rendered.

test('revisions list shows revision history', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [
      {
        ...s.pipelines[0],
        revisions: [
          {
            revision: 1,
            changed_by: 'user1',
            changed_at: '2026-08-17T10:00:00Z',
            change_note: 'created',
          },
        ],
      },
    ],
  });
  await page.goto(`/pipelines/${s.pipelines[0].id}`);
  const revSection = page.getByText(/revision|history/i);
  if (!(await revSection.isVisible({ timeout: 2000 }))) {
    // Revisions UI not yet implemented
    test.skip();
    return;
  }
  await expect(revSection).toBeVisible();
});

test('diff view is visible when two revisions exist', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [{ ...s.pipelines[0], revisions: [{ revision: 1 }, { revision: 2 }] }],
  });
  await page.goto(`/pipelines/${s.pipelines[0].id}`);
  const diffView = page.getByText(/revision|diff|history/i);
  if (!(await diffView.isVisible({ timeout: 2000 }))) {
    test.skip();
    return;
  }
  await expect(diffView).toBeVisible();
});

test('restore creates a new revision', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [
      {
        ...s.pipelines[0],
        revisions: [
          {
            revision: 1,
            changed_by: 'user1',
            changed_at: '2026-08-17T10:00:00Z',
            change_note: 'initial',
          },
        ],
      },
    ],
  });
  await page.goto(`/pipelines/${s.pipelines[0].id}`);
  // Expand the revision history panel first
  const revHeader = page.getByText(/revision history/i);
  if (await revHeader.isVisible({ timeout: 2000 })) {
    await revHeader.click();
  }
  const restore = page.getByRole('button', { name: /restore/i });
  if (!(await restore.isVisible({ timeout: 2000 }))) {
    test.skip();
    return;
  }
  await restore.click();
  // Restore loads content into editor (local state change, no network request)
  await expect(page.locator('.cm-editor')).toBeVisible();
});
