/**
 * Form errors render inline, in sentence case (#249).
 *
 * A refused submit used to surface only as a transient toast carrying the
 * server's lowercase Go error text, gone before it could be read and nowhere
 * near the field that caused it. Every form now shows the refusal inside the
 * form (data-testid="form-error", role="alert") and leaves the toast to
 * actions that have no form to show it in.
 *
 * Each test refuses one form's RPC with a lowercase message and asserts the
 * sentence-cased text appears inside that form, with no toast carrying it.
 */
import type { Locator, Page } from '@playwright/test';
import { basicScenario, collector, destination, org } from '../fixtures/factories';
import { appAdmin, orgAdmin, orgEditor } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';
import type { Handler } from '../mocks/router';

type Api = { override: (method: string, path: string, handler: Handler) => void };

/** Make one Connect procedure refuse with a lowercase, Go-style message. */
function refuse(api: Api, procedure: string, message: string, code = 'invalid_argument') {
  api.override('POST', `/shepherd.mgmt.v1.${procedure}`, (route) =>
    route.fulfill({
      status: 400,
      contentType: 'application/json',
      body: JSON.stringify({ code, message }),
    }),
  );
}

/** The refusal is inside `scope`, sentence-cased, and not (also) a toast. */
async function expectInline(page: Page, scope: Locator, text: string) {
  const inline = scope.getByTestId('form-error');
  await expect(inline).toHaveText(text);
  await expect(inline).toHaveAttribute('role', 'alert');
  await expect(page.locator('[data-sonner-toast]').filter({ hasText: text })).toHaveCount(0);
}

const dialog = (page: Page) => page.getByRole('dialog');

test.describe('tenant routes', () => {
  test('an empty gateway name says what is missing instead of only focusing the field', async ({
    page,
    api,
  }) => {
    const s = basicScenario();
    api.seed({ orgs: [s.org], tenantRoutes: [] });
    await api.loginAs(orgAdmin);
    await page.goto('/tenant-routes');

    await page.getByTestId('route-new').click();
    await page.getByRole('button', { name: 'Create route' }).click();

    await expect(dialog(page).getByTestId('route-gateway-name-error')).toHaveText(
      'Enter a gateway name: the Gateway Shepherd creates for this route.',
    );
    await expect(page.getByTestId('route-gateway-name')).toHaveAttribute('aria-invalid', 'true');
    expect(api.calls('TenantRouteService/CreateTenantRoute')).toHaveLength(0);

    // Typing clears it, and the operator-mode wording names the other Gateway.
    await page.getByTestId('route-gateway-mode').selectOption('operator');
    await page.getByTestId('route-gateway-name').pressSequentially('gw');
    await expect(dialog(page).getByTestId('route-gateway-name-error')).toHaveCount(0);
    await page.getByTestId('route-gateway-name').fill('');
    await page.getByRole('button', { name: 'Create route' }).click();
    await expect(dialog(page).getByTestId('route-gateway-name-error')).toHaveText(
      'Enter a gateway name: your existing Gateway this route attaches to.',
    );
  });

  test('a refused create shows the server reason in the dialog', async ({ page, api }) => {
    const s = basicScenario();
    api.seed({ orgs: [s.org], tenantRoutes: [] });
    refuse(api, 'TenantRouteService/CreateTenantRoute', 'gateway_namespace "x y" is not valid');
    await api.loginAs(orgAdmin);
    await page.goto('/tenant-routes');

    await page.getByTestId('route-new').click();
    await page.getByTestId('route-gateway-name').pressSequentially('shepherd-gw');
    await page.getByRole('button', { name: 'Create route' }).click();

    await expectInline(page, dialog(page), 'Gateway_namespace "x y" is not valid');
  });

  test('a refused gateway name or namespace shows under that field (M6)', async ({ page, api }) => {
    const s = basicScenario();
    api.seed({ orgs: [s.org], tenantRoutes: [] });
    refuse(
      api,
      'TenantRouteService/CreateTenantRoute',
      'gateway name "Bad Name!" is not a valid Kubernetes object name',
    );
    await api.loginAs(orgAdmin);
    await page.goto('/tenant-routes');

    await page.getByTestId('route-new').click();
    await page.getByTestId('route-gateway-name').pressSequentially('Bad Name!');
    await page.getByRole('button', { name: 'Create route' }).click();

    const nameError = dialog(page).getByTestId('route-gateway-name-error');
    await expect(nameError).toHaveText(
      'Gateway name "Bad Name!" is not a valid Kubernetes object name',
    );
    await expect(nameError).toHaveAttribute('role', 'alert');
    await expect(page.getByTestId('route-gateway-name')).toHaveAttribute('aria-invalid', 'true');
    // Shown once, beside the field — not repeated above the buttons.
    await expect(dialog(page).getByTestId('form-error')).toHaveCount(0);

    refuse(
      api,
      'TenantRouteService/CreateTenantRoute',
      'gateway namespace "gw.system" is not a valid Kubernetes namespace',
    );
    await page.getByTestId('route-gateway-name').fill('edge');
    await page.getByTestId('route-gateway-namespace').fill('gw.system');
    await page.getByRole('button', { name: 'Create route' }).click();
    await expect(dialog(page).getByTestId('route-gateway-namespace-error')).toHaveText(
      'Gateway namespace "gw.system" is not a valid Kubernetes namespace',
    );
    await expect(page.getByTestId('route-gateway-namespace')).toHaveAttribute(
      'aria-invalid',
      'true',
    );
    await expect(dialog(page).getByTestId('route-gateway-name-error')).toHaveCount(0);
    await expect(dialog(page).getByTestId('form-error')).toHaveCount(0);
  });

  test('a refused rotate and revoke stay in their dialogs', async ({ page, api }) => {
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      tenantRoutes: [
        {
          id: 'tr-1',
          org_id: s.org.id,
          tenant_id: 'tenant-x',
          kind: 'otlp',
          segment: 'otlp-abc123',
          status: 'active',
          gateway_mode: 'managed',
          gateway_name: 'gw',
          gateway_namespace: '',
          created_at: '2026-09-17T09:00:00Z',
          updated_at: '2026-09-17T09:00:00Z',
        },
      ],
    });
    refuse(
      api,
      'TenantRouteService/RotateTenantRoute',
      'route is not active',
      'failed_precondition',
    );
    refuse(api, 'TenantRouteService/RevokeTenantRoute', 'route is locked', 'failed_precondition');
    await api.loginAs(orgAdmin);
    await page.goto('/tenant-routes');

    await page.getByTestId('route-rotate-otlp-abc123').click();
    await page.getByRole('button', { name: 'Rotate', exact: true }).click();
    await expectInline(page, dialog(page), 'Route is not active');
    // Closing and reopening starts clean rather than showing the old refusal.
    await page.getByRole('button', { name: 'Cancel' }).click();
    await page.getByTestId('route-rotate-otlp-abc123').click();
    await expect(dialog(page).getByTestId('form-error')).toHaveCount(0);
    await page.getByRole('button', { name: 'Cancel' }).click();

    await page.getByTestId('route-revoke-otlp-abc123').click();
    await page.getByRole('button', { name: /^Revoke$/ }).click();
    await expectInline(page, dialog(page), 'Route is locked');
  });
});

test('destinations: a refused create shows the reason in the dialog', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  refuse(api, 'DestinationService/CreateDestination', 'destination name "prom" already exists');
  await page.goto('/destinations');

  await page.getByRole('button', { name: 'New destination' }).click();
  await dialog(page).getByPlaceholder('prom-prod').pressSequentially('prom');
  await dialog(page)
    .getByRole('textbox', { name: 'URL' })
    .pressSequentially('https://m.example/push');
  await dialog(page).getByRole('button', { name: 'Create', exact: true }).click();

  await expectInline(page, dialog(page), 'Destination name "prom" already exists');
});

test('collector labels: a refused label shows beside the form', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  refuse(api, 'FleetService/SetCollectorLabel', 'label "cluster" is reserved');
  await page.goto(`/collectors/${s.collectors[0].id}`);
  await page.getByRole('button', { name: 'Manage labels' }).click();
  await page.getByLabel('Key', { exact: true }).fill('cluster');
  await page.getByLabel('Value', { exact: true }).fill('x');
  await page.getByRole('button', { name: 'Add label', exact: true }).click();

  await expectInline(
    page,
    page.getByRole('region', { name: 'Collector labels' }),
    'Label "cluster" is reserved',
  );
});

test('collector access: a refused group shows beside the add form', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const o = org({ id: 'org-0001' });
  const c = collector({ id: 'col-0001', cluster: 'prod-eu-1', role: 'metrics' });
  api.seed({ orgs: [o], collectors: [c] });
  refuse(api, 'FleetService/CreateAssignment', 'group id is not a uuid');
  await page.goto(`/collectors/${c.id}`);
  await page.getByRole('button', { name: 'Access' }).click();
  await page.getByTestId('group-id-input').fill('not-a-uuid');
  await page.getByTestId('add-assignment-btn').click();

  await expectInline(page, page.locator('main'), 'Group id is not a uuid');
});

test('collector bindings: a refused binding shows in the dialog', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], agentIdentities: [] });
  refuse(api, 'AdminService/CreateAgentIdentity', 'issuer must be an https url');
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');
  await page.getByTestId('binding-new').click();
  await page.getByTestId('binding-issuer').fill('http://idp/');
  await page.getByTestId('binding-app-id').fill('c');
  await page.getByTestId('binding-org').fill('prod-org');
  await page.getByTestId('binding-submit').click();

  await expectInline(page, dialog(page), 'Issuer must be an https url');
});

test('collector bindings: a refused role shows under the roles field (M6)', async ({
  page,
  api,
}) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], agentIdentities: [] });
  refuse(
    api,
    'AdminService/CreateAgentIdentity',
    'role "bogusrole" is not a collector role: use logs, metrics, receiver or singleton',
  );
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');
  await page.getByTestId('binding-new').click();
  // The hint names the roles that exist, so the person need not guess.
  await expect(dialog(page)).toContainText('metrics, logs, receiver, singleton');
  await page.getByTestId('binding-issuer').fill('https://idp/');
  await page.getByTestId('binding-app-id').fill('c');
  await page.getByTestId('binding-org').fill('prod-org');
  await page.getByTestId('binding-roles').fill('bogusrole');
  await page.getByTestId('binding-submit').click();

  const rolesError = dialog(page).getByTestId('binding-roles-error');
  await expect(rolesError).toHaveText(
    'Role "bogusrole" is not a collector role: use logs, metrics, receiver or singleton',
  );
  await expect(rolesError).toHaveAttribute('role', 'alert');
  await expect(page.getByTestId('binding-roles')).toHaveAttribute('aria-invalid', 'true');
  await expect(dialog(page).getByTestId('form-error')).toHaveCount(0);
});

test('single sign-on: a refused save and test show beside the buttons', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  refuse(api, 'AdminService/UpdateOidcSettings', 'issuer must be an https url');
  refuse(api, 'AdminService/TestOidcSettings', 'discovery document not found');
  await page.goto('/admin/auth');
  await page.getByTestId('sso-issuer').fill('http://idp.example');
  await page.getByTestId('sso-save').click();
  await expectInline(page, page.getByTestId('sso-form'), 'Issuer must be an https url');
  await page.getByTestId('sso-test').click();
  await expectInline(page, page.getByTestId('sso-form'), 'Discovery document not found');
});

test('clusters: a refused claim shows in the dialog', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [org({ id: 'org-0001', name: 'prod-org', display_name: 'Production Org' })],
    clusters: [{ id: 'cl-0001', name: 'prod-eu-1', org_id: null }],
  });
  refuse(api, 'AdminService/ClaimCluster', 'cluster is already claimed', 'already_exists');
  await page.goto('/admin/clusters');
  await page.getByRole('button', { name: 'Claim' }).click();
  await dialog(page).getByLabel('Organisation').selectOption('org-0001');
  await dialog(page).getByRole('button', { name: 'Claim' }).click();

  await expectInline(page, dialog(page), 'Cluster is already claimed');
});

test('organisations: a refused create and delete show in their dialogs', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001', name: 'test-org', display_name: 'Test Org' });
  api.seed({ orgs: [o] });
  refuse(api, 'AdminService/CreateOrg', 'name must be a dns label');
  refuse(api, 'AdminService/DeleteOrg', 'organisation still has collectors', 'already_exists');
  await page.goto('/admin/orgs');

  await page.getByRole('button', { name: /new organisation/i }).click();
  await page.getByLabel('Name', { exact: true }).fill('new-org');
  await page.getByLabel('Display name').fill('New Org');
  await page.getByLabel(/admin group id/i).fill('11111111-1111-1111-1111-111111111111');
  await dialog(page).getByRole('button', { name: 'Create', exact: true }).click();
  await expectInline(page, dialog(page), 'Name must be a dns label');
  await page.getByRole('button', { name: 'Cancel' }).click();

  await page.getByRole('button', { name: 'Delete test-org' }).click();
  await dialog(page).getByRole('button', { name: 'Delete', exact: true }).click();
  await expectInline(page, dialog(page), 'Cannot delete: organisation still has collectors');
});

test('agent tokens: a refused create shows in the dialog', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  refuse(api, 'AdminService/CreateAgentToken', 'name is too long');
  await page.goto('/admin/tokens');
  await page.getByRole('button', { name: /new token/i }).click();
  await dialog(page).getByLabel('Name').fill('x');
  await dialog(page).getByRole('button', { name: 'Create', exact: true }).click();

  await expectInline(page, dialog(page), 'Name is too long');
});

test('service accounts: a refused create shows in the dialog', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], serviceAccounts: [] });
  refuse(api, 'ServiceAccountService/CreateServiceAccount', 'name is already in use');
  await api.loginAs(orgAdmin);
  await page.goto('/service-accounts');
  await page.getByTestId('sa-new').click();
  await page.getByTestId('sa-name').fill('ci');
  await dialog(page).getByRole('button', { name: 'Create', exact: true }).click();

  await expectInline(page, dialog(page), 'Name is already in use');
});

test('teams: a refused create and member add show in their dialogs', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  refuse(api, 'TeamService/CreateTeam', 'team name is taken');
  refuse(api, 'TeamService/AddTeamMember', 'user is not in this organisation');
  await page.goto('/teams');

  await page.getByTestId('team-new').click();
  await page.getByTestId('team-name').fill('platform');
  await dialog(page).getByRole('button', { name: 'Create', exact: true }).click();
  await expectInline(page, dialog(page), 'Team name is taken');
  await page.getByRole('button', { name: 'Cancel' }).click();

  await page.getByTestId('team-members-empty-team').click();
  await page.getByTestId('team-member-add-select').selectOption('user-2');
  await page.getByTestId('team-member-add').click();
  await expectInline(page, dialog(page), 'User is not in this organisation');
});

test('users: a refused edit and password reset show in their dialogs', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  refuse(api, 'UserService/UpdateUser', 'email is not valid');
  refuse(api, 'UserService/ResetUserPassword', 'password is too common');
  await page.goto('/admin/users');

  await page.getByTestId('user-edit-alice').click();
  await dialog(page).getByRole('button', { name: 'Save', exact: true }).click();
  await expectInline(page, dialog(page), 'Email is not valid');
  await page.getByRole('button', { name: 'Cancel' }).click();

  await page.getByTestId('user-reset-alice').click();
  await page.getByTestId('reset-password').fill('password1234');
  await dialog(page).getByRole('button', { name: 'Reset password' }).click();
  await expectInline(page, dialog(page), 'Password is too common');
});

test.describe('git', () => {
  test('a refused credential and repository link show in their dialogs', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      collectors: s.collectors,
      gitCredentials: [{ id: 'cred-0001', name: 'gitea-pat', kind: 'pat', username: 'oauth2' }],
    });
    refuse(api, 'GitOpsService/CreateCredential', 'token must not be empty');
    refuse(api, 'GitOpsService/CreateRepoLink', 'repo url must use https or ssh');
    refuse(api, 'GitOpsService/TestCredential', 'credential could not be decrypted');
    await page.goto('/git');

    await page.getByRole('button', { name: /new credential/i }).click();
    await page.getByLabel('Name', { exact: true }).fill('another');
    await page.getByRole('textbox', { name: 'Username' }).fill('oauth2');
    await page.getByRole('textbox', { name: 'Token' }).fill('x');
    await dialog(page).getByRole('button', { name: 'Create', exact: true }).click();
    await expectInline(page, dialog(page), 'Token must not be empty');
    await page.getByRole('button', { name: 'Cancel' }).click();

    await page.getByRole('button', { name: /new repository link/i }).click();
    await page.getByRole('textbox', { name: /clone url/i }).fill('ftp://x/y.git');
    await page.getByRole('combobox', { name: /target collector/i }).selectOption('col-0001');
    await page.getByRole('combobox', { name: 'Credential' }).selectOption('cred-0001');
    await dialog(page).getByRole('button', { name: 'Create', exact: true }).click();
    await expectInline(page, dialog(page), 'Repo url must use https or ssh');
    await page.getByRole('button', { name: 'Cancel' }).click();

    await page.getByRole('button', { name: /test gitea-pat/i }).click();
    await page.getByLabel(/repository url/i).fill('https://gitea.internal/team/configs.git');
    await dialog(page)
      .getByRole('button', { name: /run test/i })
      .click();
    await expectInline(page, dialog(page), 'Credential could not be decrypted');
  });

  test('a refused clone URL shows under the clone URL field (M6)', async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      collectors: s.collectors,
      gitCredentials: [{ id: 'cred-0001', name: 'gitea-pat', kind: 'pat', username: 'oauth2' }],
    });
    refuse(
      api,
      'GitOpsService/CreateRepoLink',
      'clone URL "not a url" is not a git remote URL: use https://host/owner/repo.git',
    );
    await page.goto('/git');

    await page.getByRole('button', { name: /new repository link/i }).click();
    await page.getByRole('textbox', { name: /clone url/i }).fill('not a url');
    await page.getByRole('combobox', { name: /target collector/i }).selectOption('col-0001');
    await page.getByRole('combobox', { name: 'Credential' }).selectOption('cred-0001');
    await dialog(page).getByRole('button', { name: 'Create', exact: true }).click();

    const urlError = dialog(page).getByTestId('repo-url-error');
    await expect(urlError).toHaveText(
      'Clone URL "not a url" is not a git remote URL: use https://host/owner/repo.git',
    );
    await expect(urlError).toHaveAttribute('role', 'alert');
    await expect(page.getByRole('textbox', { name: /clone url/i })).toHaveAttribute(
      'aria-invalid',
      'true',
    );
    await expect(dialog(page).getByTestId('form-error')).toHaveCount(0);
  });
});

test.describe('pipeline editor', () => {
  test('a refused save shows above the editor, not as a toast', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
    refuse(api, 'PipelineService/UpdatePipeline', 'pipeline name "x" is already in use');
    await page.goto(`/pipelines/${s.pipelines[0].id}`);
    await page.getByRole('button', { name: 'Save', exact: true }).click();

    await expectInline(page, page.locator('main'), 'Pipeline name "x" is already in use');
  });

  test('a malformed matcher is refused inline and never becomes a chip', async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({ orgs: [s.org], pipelines: [s.pipelines[0]] });
    await page.goto(`/pipelines/${s.pipelines[0].id}`);
    const chips = page.getByTestId('pipeline-matcher-chip');
    // Wait for the seeded matchers to load before counting from them.
    const before = s.pipelines[0].matchers.length;
    expect(before).toBeGreaterThan(0);
    await expect(chips).toHaveCount(before);
    const input = page.getByTestId('pipeline-matcher-input');

    await input.pressSequentially('cluster');
    await input.press('Enter');
    await expect(page.getByTestId('pipeline-matcher-error')).toContainText('key="value"');
    await expect(input).toHaveAttribute('aria-invalid', 'true');
    await expect(chips).toHaveCount(before);

    await input.fill('env=~"prod("');
    await input.press('Enter');
    await expect(page.getByTestId('pipeline-matcher-error')).toContainText('regular expression');
    await expect(chips).toHaveCount(before);

    // Editing clears the message; a well-formed matcher is added.
    await input.fill('env!~"dev.*"');
    await expect(page.getByTestId('pipeline-matcher-error')).toHaveCount(0);
    await input.press('Enter');
    await expect(chips).toHaveCount(before + 1);
    await expect(input).toHaveValue('');
  });
});

test('restore: a refused restore shows in the dialog', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [
      {
        id: 'pip-r',
        org_id: s.org.id,
        name: 'restorable',
        contents: 'b',
        matchers: ['cluster="prod-eu-1"'],
        enabled: true,
        source: 'ui',
        created_by: 'a@example.com',
        updated_by: 'a@example.com',
        created_at: '2026-08-17T09:00:00Z',
        updated_at: '2026-08-17T09:00:00Z',
        revisions: [
          {
            revision: 1,
            changed_by: 'a@example.com',
            changed_at: '2026-08-17T10:00:00Z',
            change_note: 'created',
            contents: 'a',
            matchers: ['cluster="prod-eu-1"'],
            enabled: true,
          },
          // The newest revision is "current" and offers no Restore (#252), so
          // the spec restores the older one.
          {
            revision: 2,
            changed_by: 'a@example.com',
            changed_at: '2026-08-17T11:00:00Z',
            change_note: 'edited',
            contents: 'b',
            matchers: ['cluster="prod-eu-1"'],
            enabled: true,
          },
        ],
      },
    ],
  });
  refuse(api, 'PipelineService/RestoreRevision', 'revision 1 no longer validates');
  await page.goto('/pipelines/pip-r');
  await page.getByRole('button', { name: /revision history/i }).click();
  await page.locator('[data-testid="view-revision-btn"][data-revision="1"]').click();
  await page.getByTestId('restore-btn').click();
  await page.getByTestId('confirm-restore-btn').click();

  await expectInline(page, page.getByTestId('restore-dialog'), 'Revision 1 no longer validates');
});

test('detach from wizard: a refusal shows in the dialog', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  const wizardPipeline = s.pipelines.find((p) => p.source === 'wizard');
  refuse(api, 'PipelineService/DetachFromWizard', 'pipeline is not a wizard pipeline');
  await page.goto(`/pipelines/${wizardPipeline?.id}`);
  await page.getByTestId('detach-wizard-btn').click();
  await page.getByTestId('confirm-detach-btn').click();

  await expectInline(
    page,
    page.getByTestId('detach-wizard-dialog'),
    'Pipeline is not a wizard pipeline',
  );
});

test('wizard: a refused commit shows above the buttons', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [destination({ id: 'dst-prom', name: 'prom-prod', type: 'prometheus' })],
  });
  refuse(api, 'WizardService/CommitWizard', 'pipeline name "x" is already in use');
  await page.goto('/wizards');
  await page.getByRole('link', { name: /app observability|start|begin/i }).click();
  await expect(page.getByText(/step 1 of/i)).toBeVisible();

  for (let i = 0; i < 6; i++) {
    const next = page.getByRole('button', { name: 'Continue' });
    if (!(await next.isVisible())) break;
    const textInputs = page.locator('input[type="text"]');
    for (let j = 0; j < (await textInputs.count()); j++) {
      const input = textInputs.nth(j);
      if ((await input.inputValue()) === '') await input.fill('e2e-test-value');
    }
    const selects = page.locator('select');
    for (let j = 0; j < (await selects.count()); j++) {
      const select = selects.nth(j);
      if ((await select.inputValue()) === '') {
        const first = select.locator('option:not([value=""])').first();
        if (await first.count()) await select.selectOption(await first.getAttribute('value'));
      }
    }
    await next.click();
  }
  await page.getByRole('button', { name: 'Create pipeline' }).click();

  await expectInline(page, page.locator('main'), 'Pipeline name "x" is already in use');
});
