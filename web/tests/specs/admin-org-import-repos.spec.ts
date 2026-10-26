// admin-org-import-repos.spec.ts — strict tenant mode lets an org's pipelines
// use import.git pinned to a full commit SHA from the repositories an app
// admin allows. Admin → Organisations → Edit lists them and adds or removes
// one, each through its own app-admin RPC (AddOrgImportRepository /
// RemoveOrgImportRepository), independent of Save. A refusal — an ambiguous
// URL, a second spelling of an entry, a removal while a pipeline imports from
// it — shows the server's message.
import { expect } from '@playwright/test';
import { appAdmin } from '../fixtures/personas';
import { test } from '../fixtures/test';

const org = {
  id: 'org-0001',
  name: 'prod-org',
  display_name: 'Production Org',
  admin_group_id: 'admins',
  viewer_group_id: '',
  created_at: '2026-09-01T09:00:00Z',
  updated_at: '2026-09-01T09:00:00Z',
  tenants: [{ tenant: 'acme', kind: 'exact', is_default: true }],
};

const credentials = [
  { id: 'cred-1', org_id: 'org-0001', name: 'modules-deploy-key', kind: 'ssh' },
  { id: 'cred-2', org_id: 'org-0002', name: 'other-org-token', kind: 'pat' },
];

test('lists the allowed repositories with their credential', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    gitCredentials: credentials,
    orgs: [
      {
        ...org,
        import_repositories: [
          {
            id: 'ir-1',
            repository: 'https://GitHub.com/acme/modules.git',
            normalized: 'https://github.com/acme/modules',
            created_by: 'root',
          },
          {
            id: 'ir-2',
            repository: 'git@github.com:acme/private.git',
            normalized: 'ssh://git@github.com/acme/private',
            credential_id: 'cred-1',
            created_by: 'root',
          },
        ],
      },
    ],
  });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-import-repos');

  const pub = editor.getByTestId('org-import-repo-row-https://github.com/acme/modules');
  await expect(pub).toContainText('https://GitHub.com/acme/modules.git');
  await expect(pub).toContainText('matches https://github.com/acme/modules');
  await expect(pub.getByTestId('org-import-repo-credential')).toHaveText('public');
  const priv = editor.getByTestId('org-import-repo-row-ssh://git@github.com/acme/private');
  await expect(priv.getByTestId('org-import-repo-credential')).toHaveText('modules-deploy-key');

  // Only the org's own credentials are offered.
  const select = editor.getByTestId('org-import-repo-new-credential');
  await expect(select.locator('option')).toHaveText(['None (public)', 'modules-deploy-key']);
});

test('an app admin adds a repository with a credential, and a duplicate is refused', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ gitCredentials: credentials, orgs: [{ ...org, import_repositories: [] }] });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-import-repos');
  await expect(editor.getByTestId('org-import-repos-empty')).toBeVisible();

  // Caught before a round trip: not a git remote at all.
  await editor.getByTestId('org-import-repo-new').pressSequentially('/srv/modules.git');
  await editor.getByTestId('org-import-repo-add').click();
  await expect(editor.getByTestId('org-import-repo-error')).toContainText('Use https://');
  expect(api.calls('AdminService/AddOrgImportRepository')).toHaveLength(0);

  await editor.getByTestId('org-import-repo-new').fill('');
  await editor
    .getByTestId('org-import-repo-new')
    .pressSequentially('https://github.com/acme/modules.git');
  await editor.getByTestId('org-import-repo-new-credential').selectOption({
    label: 'modules-deploy-key',
  });
  await editor.getByTestId('org-import-repo-new').press('Enter'); // adds, does not submit the dialog
  const row = editor.getByTestId('org-import-repo-row-https://github.com/acme/modules');
  await expect(row.getByTestId('org-import-repo-credential')).toHaveText('modules-deploy-key');
  await expect(page.getByRole('dialog')).toBeVisible();
  expect(api.calls('AdminService/AddOrgImportRepository')[0].body).toMatchObject({
    orgId: 'org-0001',
    repository: 'https://github.com/acme/modules.git',
    credentialId: 'cred-1',
  });

  // Another spelling of the same repository: the server refuses it.
  await editor
    .getByTestId('org-import-repo-new')
    .pressSequentially('HTTPS://GITHUB.COM/acme/modules/');
  await editor.getByTestId('org-import-repo-add').click();
  await expect(editor.getByTestId('org-import-repo-error')).toContainText('already allowed');

  // An ambiguous spelling: refused too.
  await editor.getByTestId('org-import-repo-new').fill('');
  await editor
    .getByTestId('org-import-repo-new')
    .pressSequentially('https://github.com/acme/%6dodules.git');
  await editor.getByTestId('org-import-repo-add').click();
  await expect(editor.getByTestId('org-import-repo-error')).toContainText('ambiguous');
  expect(api.calls('AdminService/UpdateOrg')).toHaveLength(0);
});

test('a removal the server refuses names the pipelines; an unused one goes', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [
      {
        ...org,
        import_repositories: [
          {
            id: 'ir-1',
            repository: 'https://github.com/acme/modules.git',
            normalized: 'https://github.com/acme/modules',
            in_use_by: ['shared-otlp'],
          },
          {
            id: 'ir-2',
            repository: 'https://github.com/acme/unused.git',
            normalized: 'https://github.com/acme/unused',
          },
        ],
      },
    ],
  });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-import-repos');

  await editor.getByTestId('org-import-repo-remove-https://github.com/acme/modules').click();
  await expect(editor.getByTestId('org-import-repo-error')).toContainText('"shared-otlp"');
  await expect(
    editor.getByTestId('org-import-repo-row-https://github.com/acme/modules'),
  ).toBeVisible();

  await editor.getByTestId('org-import-repo-remove-https://github.com/acme/unused').click();
  await expect(
    editor.getByTestId('org-import-repo-row-https://github.com/acme/unused'),
  ).toHaveCount(0);
  await expect(editor.getByTestId('org-import-repo-error')).toHaveCount(0);
  expect(api.calls('AdminService/RemoveOrgImportRepository').map((c) => c.body)).toEqual([
    expect.objectContaining({ orgId: 'org-0001', id: 'ir-1' }),
    expect.objectContaining({ orgId: 'org-0001', id: 'ir-2' }),
  ]);
});
