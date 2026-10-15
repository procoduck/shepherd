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

// B-GITSYNC-PRUNE: a sync that refuses to delete most of a link's pipelines
// records a warning, and the reason is what tells the admin what to do.
test('git sync warning shows its reason under the badge', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const reason =
    'this commit would delete 4 of 6 pipelines (more than half, and more than 3) in one sync, so none of them was deleted and 1 new file was not created either; changes to existing files were applied.';
  api.seed({
    orgs: [s.org],
    repoLinks: [
      {
        id: 'rl-0001',
        repo_url: 'https://gitea.example.com/obs/pipelines.git',
        branch: 'main',
        path: 'pipelines/',
        sync_status: 'warning',
        sync_error: reason,
        collector_id: s.collectors[0].id,
      },
    ],
  });
  await page.goto('/git');

  const badge = page.getByTestId('sync-status-warning');
  await expect(badge).toBeVisible();
  await expect(badge).toHaveText('warning');
  const detail = page.getByTestId('sync-error');
  await expect(detail).toHaveText(reason);
  // Read in order with the badge: the reason is text in the badge's own cell.
  await expect(page.getByRole('cell').filter({ has: badge })).toContainText(reason);
});

test('git sync error shows its reason; an ok link shows none', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    repoLinks: [
      {
        id: 'rl-0001',
        repo_url: 'https://gitea.example.com/obs/broken.git',
        branch: 'main',
        path: '',
        sync_status: 'error',
        sync_error: 'fetching files: authentication required',
        collector_id: s.collectors[0].id,
      },
      {
        id: 'rl-0002',
        repo_url: 'https://gitea.example.com/obs/fine.git',
        branch: 'main',
        path: '',
        sync_status: 'ok',
        collector_id: s.collectors[0].id,
      },
    ],
  });
  await page.goto('/git');

  await expect(page.getByTestId('sync-error')).toHaveCount(1);
  await expect(page.getByTestId('sync-error')).toHaveText(
    'fetching files: authentication required',
  );
});
