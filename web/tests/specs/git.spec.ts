import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('git pipeline is read-only and has no Save button', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  // git-pipe is s.pipelines[2] with source='git'
  api.seed({ orgs: [s.org], pipelines: [s.pipelines[2]] });
  await page.goto(`/pipelines/${s.pipelines[2].id}`);

  await expect(page.getByText(/managed by git/i)).toBeVisible();
  // A git-sourced pipeline is owned by the repo: offering Save would let the UI
  // write changes the next sync silently reverts.
  await expect(page.getByRole('button', { name: /^save$/i })).toHaveCount(0);
});

test('git sync error is surfaced on the repo link', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  // Sync status belongs to the repo link, which is rendered on the Git page —
  // an earlier version of this test looked for it on /pipelines, where it has
  // never been shown, and skipped itself when it (of course) found nothing.
  api.seed({
    orgs: [s.org],
    repoLinks: [
      {
        id: 'rl-0001',
        repo_url: 'https://gitea.example.com/obs/pipelines.git',
        branch: 'main',
        path: 'pipelines/',
        sync_status: 'error',
        collector_id: s.collectors[0].id,
      },
    ],
  });
  await page.goto('/git');

  const indicator = page.getByTestId('sync-status-error');
  await expect(indicator).toBeVisible();
  await expect(indicator).toContainText(/error/i);
});
