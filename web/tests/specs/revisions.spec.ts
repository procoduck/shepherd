import { basicScenario } from '../fixtures/factories';
import { appAdmin, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

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

// Seeds one org and one pipeline with a single revision whose contents
// differ from the pipeline's current contents — the shape "diff renders",
// "restore calls the RPC and refreshes" and "reader sees the diff but no
// Restore" all need. Returns the pipeline id so callers can navigate to it.
function seedTwoRevisions(api: { seed: (partial: Record<string, unknown>) => void }): string {
  const s = basicScenario();
  const pipelineId = 'pip-diff';
  api.seed({
    orgs: [s.org],
    pipelines: [
      {
        id: pipelineId,
        org_id: s.org.id,
        name: 'diffable',
        contents: 'current-only-line\nshared-line',
        matchers: [`cluster="prod-eu-1"`],
        enabled: true,
        source: 'ui',
        created_by: 'test@example.com',
        updated_by: 'test@example.com',
        created_at: '2026-08-17T09:00:00Z',
        updated_at: '2026-08-17T09:00:00Z',
        revisions: [
          {
            revision: 1,
            changed_by: 'grace@example.com',
            changed_at: '2026-08-17T10:00:00Z',
            change_note: 'created',
            contents: 'old-only-line\nshared-line',
            matchers: [`cluster="prod-eu-1"`],
            enabled: true,
          },
        ],
      },
    ],
  });
  return pipelineId;
}

test('diff renders', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const pipelineId = seedTwoRevisions(api);
  await page.goto(`/pipelines/${pipelineId}`);

  await page.getByRole('button', { name: /revision history \(1\)/i }).click();
  await page.getByTestId('view-revision-btn').click();

  await expect(page.getByTestId('revision-diff')).toBeVisible();
  await expect(page.locator('.cm-mergeView')).toBeVisible();
  await expect(page.getByText('old-only-line')).toBeVisible();
  await expect(page.getByText('current-only-line')).toBeVisible();

  const calls = api.calls('/shepherd.mgmt.v1.PipelineService/GetRevision');
  expect(calls).toHaveLength(1);
  expect((calls[0]?.body as { revision?: number } | null)?.revision).toBe(1);
});

test('restore calls the RPC and refreshes', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const pipelineId = seedTwoRevisions(api);
  await page.goto(`/pipelines/${pipelineId}`);

  await page.getByRole('button', { name: /revision history \(1\)/i }).click();
  await page.getByTestId('view-revision-btn').click();
  await expect(page.getByTestId('revision-diff')).toBeVisible();

  await page.getByTestId('restore-btn').click();
  await expect(page.getByTestId('restore-dialog')).toBeVisible();
  await page.getByTestId('confirm-restore-btn').click();

  const calls = api.calls('/shepherd.mgmt.v1.PipelineService/RestoreRevision');
  expect(calls).toHaveLength(1);
  expect(calls[0]?.body).toMatchObject({ id: pipelineId, revision: 1 });

  await expect(page.getByTestId('restore-dialog')).toHaveCount(0);
  await expect(page.getByTestId('revision-diff')).toHaveCount(0);
  await expect(page.locator('.cm-content')).toContainText('old-only-line');
  await expect(page.getByRole('button', { name: /revision history \(2\)/i })).toBeVisible();
});

test('git-sourced warning', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [
      {
        id: 'pip-git',
        org_id: s.org.id,
        name: 'git-pipe',
        contents: 'current',
        matchers: [],
        enabled: true,
        source: 'git',
        created_by: 'test@example.com',
        updated_by: 'test@example.com',
        created_at: '2026-08-17T09:00:00Z',
        updated_at: '2026-08-17T09:00:00Z',
        revisions: [
          {
            revision: 1,
            changed_by: 'grace@example.com',
            changed_at: '2026-08-17T10:00:00Z',
            change_note: 'created',
            contents: 'old',
            matchers: [],
            enabled: true,
          },
        ],
      },
      {
        id: 'pip-ui',
        org_id: s.org.id,
        name: 'ui-pipe',
        contents: 'current',
        matchers: [],
        enabled: true,
        source: 'ui',
        created_by: 'test@example.com',
        updated_by: 'test@example.com',
        created_at: '2026-08-17T09:00:00Z',
        updated_at: '2026-08-17T09:00:00Z',
        revisions: [
          {
            revision: 1,
            changed_by: 'grace@example.com',
            changed_at: '2026-08-17T10:00:00Z',
            change_note: 'created',
            contents: 'old',
            matchers: [],
            enabled: true,
          },
        ],
      },
    ],
  });

  // Git-sourced: no Save, but Restore is present, and the dialog warns.
  await page.goto('/pipelines/pip-git');
  await expect(page.getByRole('button', { name: /save/i })).toHaveCount(0);
  await page.getByRole('button', { name: /revision history \(1\)/i }).click();
  await page.getByTestId('view-revision-btn').click();
  await expect(page.getByTestId('restore-btn')).toBeVisible();
  await page.getByTestId('restore-btn').click();
  await expect(page.getByTestId('restore-dialog')).toBeVisible();
  await expect(page.getByTestId('restore-git-warning')).toBeVisible();
  await page.getByRole('button', { name: /cancel/i }).click();

  // UI-sourced: dialog has no git warning.
  await page.goto('/pipelines/pip-ui');
  await page.getByRole('button', { name: /revision history \(1\)/i }).click();
  await page.getByTestId('view-revision-btn').click();
  await page.getByTestId('restore-btn').click();
  await expect(page.getByTestId('restore-dialog')).toBeVisible();
  await expect(page.getByTestId('restore-git-warning')).toHaveCount(0);
});

test('reader sees the diff but no Restore', async ({ page, api }) => {
  await api.loginAs(reader);
  const pipelineId = seedTwoRevisions(api);
  await page.goto(`/pipelines/${pipelineId}`);

  await page.getByRole('button', { name: /revision history \(1\)/i }).click();
  await page.getByTestId('view-revision-btn').click();

  await expect(page.getByTestId('revision-diff')).toBeVisible();
  await expect(page.getByTestId('restore-btn')).toHaveCount(0);
  expect(api.calls('/shepherd.mgmt.v1.PipelineService/RestoreRevision')).toHaveLength(0);
});
