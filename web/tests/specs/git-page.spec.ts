import { basicScenario } from '../fixtures/factories';
import { orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// Coverage for the GitPage CRUD flows ledger B2 records as entirely
// missing (no create affordance for credentials or repo links at all) and
// F9 step 5 builds: a kind-switching credential form, a repo-link form,
// delete for both, and the Test button surfacing TestCredential's result.

test('empty state offers create affordances for both credentials and repo links', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto('/git');

  await expect(page.getByText(/no credentials configured/i)).toBeVisible();
  await expect(page.getByRole('button', { name: /new credential/i })).toBeVisible();
  await expect(page.getByText(/no repository links configured/i)).toBeVisible();
  // No credential exists yet, so the repo-link create button is disabled
  // rather than opening a form with an empty, unusable credential picker.
  await expect(page.getByRole('button', { name: /new repository link/i })).toBeDisabled();
});

test('creates a PAT credential and it appears in the list', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto('/git');

  await page.getByRole('button', { name: /new credential/i }).click();
  await page.getByLabel('Name', { exact: true }).fill('gitea-pat');
  // Kind defaults to "pat" — username + token fields already showing.
  await page.getByRole('textbox', { name: 'Username' }).fill('oauth2');
  await page.getByRole('textbox', { name: 'Token' }).fill('ghp_abc123');
  await page.getByRole('dialog').getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByRole('cell', { name: 'gitea-pat', exact: true })).toBeVisible();
  const calls = api.calls('GitOpsService/CreateCredential');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({
    name: 'gitea-pat',
    kind: 'pat',
    clientSecret: 'ghp_abc123',
  });
});

test('credential form switches fields by kind (github_app shows app id/installation id)', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto('/git');

  await page.getByRole('button', { name: /new credential/i }).click();
  await expect(page.getByLabel(/app id/i)).toHaveCount(0);

  await page.getByLabel('Kind').selectOption('github_app');
  await expect(page.getByLabel(/app id/i)).toBeVisible();
  await expect(page.getByLabel(/installation id/i)).toBeVisible();
  await expect(page.getByLabel(/private key/i)).toBeVisible();

  await page.getByLabel('Kind').selectOption('ssh');
  await expect(page.getByLabel(/known hosts/i)).toBeVisible();
  await expect(page.getByLabel(/app id/i)).toHaveCount(0);
});

test('creates a repo link targeting a collector with an existing credential', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    collectors: s.collectors,
    gitCredentials: [{ id: 'cred-0001', name: 'gitea-pat', kind: 'pat', username: 'oauth2' }],
  });
  await page.goto('/git');

  await page.getByRole('button', { name: /new repository link/i }).click();
  await page
    .getByRole('textbox', { name: /clone url/i })
    .fill('https://gitea.internal/team/configs.git');
  await page.getByRole('combobox', { name: /target collector/i }).selectOption('col-0001');
  await page.getByRole('combobox', { name: 'Credential' }).selectOption('cred-0001');
  await page.getByRole('dialog').getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText('https://gitea.internal/team/configs.git')).toBeVisible();
  const calls = api.calls('GitOpsService/CreateRepoLink');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({
    repoUrl: 'https://gitea.internal/team/configs.git',
    collectorId: 'col-0001',
    credentialId: 'cred-0001',
  });
});

test('Test button surfaces a reachable result', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    collectors: s.collectors,
    gitCredentials: [{ id: 'cred-0001', name: 'gitea-pat', kind: 'pat', username: 'oauth2' }],
  });
  await page.goto('/git');

  await page.getByRole('button', { name: /test gitea-pat/i }).click();
  await page.getByLabel(/repository url/i).fill('https://gitea.internal/team/configs.git');
  await page
    .getByRole('dialog')
    .getByRole('button', { name: /run test/i })
    .click();

  const result = page.getByTestId('test-credential-result');
  await expect(result).toBeVisible();
  await expect(result).toContainText(/reachable/i);
});

test('Test button surfaces an unreachable result with the underlying error', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    collectors: s.collectors,
    gitCredentials: [{ id: 'cred-0001', name: 'gitea-pat', kind: 'pat', username: 'oauth2' }],
  });
  await page.goto('/git');

  await page.getByRole('button', { name: /test gitea-pat/i }).click();
  await page.getByLabel(/repository url/i).fill('https://unreachable.example/x.git');
  await page
    .getByRole('dialog')
    .getByRole('button', { name: /run test/i })
    .click();

  const result = page.getByTestId('test-credential-result');
  await expect(result).toBeVisible();
  await expect(result).toContainText(/unreachable/i);
  await expect(result).toContainText(/connection refused/i);
});

test('deletes a credential after confirmation', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    collectors: s.collectors,
    gitCredentials: [{ id: 'cred-0001', name: 'gitea-pat', kind: 'pat', username: 'oauth2' }],
  });
  await page.goto('/git');

  await expect(page.getByRole('cell', { name: 'gitea-pat', exact: true })).toBeVisible();
  await page.getByRole('button', { name: /delete gitea-pat/i }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete', exact: true }).click();

  await expect(page.getByRole('cell', { name: 'gitea-pat', exact: true })).toHaveCount(0);
  const calls = api.calls('GitOpsService/DeleteCredential');
  expect(calls).toHaveLength(1);
});

test('deletes a repo link after confirmation', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    collectors: s.collectors,
    gitCredentials: [{ id: 'cred-0001', name: 'gitea-pat', kind: 'pat', username: 'oauth2' }],
    repoLinks: [
      {
        id: 'rl-0001',
        repo_url: 'https://gitea.internal/team/configs.git',
        branch: 'main',
        path: '/',
        collector_id: 'col-0001',
        credential_id: 'cred-0001',
        sync_status: 'ok',
      },
    ],
  });
  await page.goto('/git');

  await expect(page.getByText('https://gitea.internal/team/configs.git')).toBeVisible();
  await page.getByRole('button', { name: /delete repository link/i }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete', exact: true }).click();

  await expect(page.getByText('https://gitea.internal/team/configs.git')).toHaveCount(0);
  const calls = api.calls('GitOpsService/DeleteRepoLink');
  expect(calls).toHaveLength(1);
});
