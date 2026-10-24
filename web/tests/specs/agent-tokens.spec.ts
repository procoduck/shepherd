/**
 * Org-bound agent tokens (migration 0038) on the Agent Tokens page.
 *
 *   - An app admin sees every token; unbound ones (pre-0038) sort first,
 *     carry an "Unbound" badge and a Bind action, and a notice says how many
 *     need binding. Binding keeps the secret: the page sends BindAgentToken
 *     with the chosen org and allowlists, and the row shows the org after.
 *   - An org admin reaches the page through the Access nav group, lists only
 *     their org's tokens (the request is scoped by org_id), creates tokens
 *     bound to that org with the org fixed in the dialog, and gets no Bind.
 *   - Below org admin the link is hidden and the route is refused before
 *     ListAgentTokens fires.
 */
import { agentToken, org } from '../fixtures/factories';
import { appAdmin, orgAdmin, orgEditor, viewer } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

const PROD = org({ id: 'org-0001', name: 'prod-org', display_name: 'Production Org' });
const OTHER = org({ id: 'org-0002', name: 'other-org', display_name: 'Other Org' });

test('app admin: unbound tokens sort first, are flagged, and bind in place', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [PROD, OTHER],
    agentTokens: [
      agentToken({
        id: 'tok-bound',
        name: 'bound-agent',
        org_id: 'org-0001',
        clusters: ['prod-eu-1'],
      }),
      agentToken({ id: 'tok-legacy', name: 'legacy-agent', org_id: '' }),
    ],
  });
  await page.goto('/admin/tokens');

  await expect(page.getByTestId('tokens-unbound-notice')).toContainText('1 token is not bound');
  const rows = page.getByRole('row');
  // Header row, then the unbound token first.
  await expect(rows.nth(1)).toContainText('legacy-agent');
  await expect(page.getByTestId('token-unbound-tok-legacy')).toBeVisible();
  await expect(rows.nth(2)).toContainText('prod-org');
  await expect(rows.nth(2)).toContainText('prod-eu-1');
  // 0.22 -> 0.23 upgrade walkthrough: an unbound token's empty allowlists
  // read "any", as if it could act for every cluster and role; it is refused
  // outright. "any" is only for a bound token's empty allowlist.
  await expect(page.getByTestId('token-clusters-tok-legacy')).toHaveText('— (unbound: refused)');
  await expect(page.getByTestId('token-roles-tok-legacy')).toHaveText('— (unbound: refused)');
  await expect(page.getByTestId('token-roles-tok-bound')).toHaveText('any');
  expect(api.calls('AdminService/ListAgentTokens')[0].body).not.toHaveProperty('orgId');

  await page.getByTestId('token-bind-tok-legacy').click();
  const dialog = page.getByRole('dialog');
  await dialog.getByTestId('token-org').selectOption('org-0002');
  await dialog.getByTestId('token-clusters').pressSequentially('edge-1, edge-2', { delay: 10 });
  await dialog.getByTestId('token-roles').pressSequentially('metrics', { delay: 10 });
  await dialog.getByTestId('token-bind-submit').click();

  await expect(page.getByTestId('token-unbound-tok-legacy')).toHaveCount(0);
  await expect(page.getByTestId('tokens-unbound-notice')).toHaveCount(0);
  await expect(page.getByRole('row').filter({ hasText: 'legacy-agent' })).toContainText(
    'other-org',
  );
  const calls = api.calls('AdminService/BindAgentToken');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({
    id: 'tok-legacy',
    orgId: 'org-0002',
    clusters: ['edge-1', 'edge-2'],
    roles: ['metrics'],
  });
});

test('app admin: a new token needs an org, and carries its allowlists', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [PROD, OTHER], agentTokens: [] });
  await page.goto('/admin/tokens');

  await page.getByRole('button', { name: /new token/i }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').pressSequentially('edge-agent', { delay: 10 });
  // The org in view is preselected; any org may be picked.
  await expect(dialog.getByTestId('token-org')).toHaveValue('org-0001');
  await dialog.getByTestId('token-org').selectOption('org-0002');
  await dialog.getByTestId('token-clusters').pressSequentially('edge-1', { delay: 10 });
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText(/only time/i)).toBeVisible();
  expect(api.calls('AdminService/CreateAgentToken')[0].body).toMatchObject({
    name: 'edge-agent',
    orgId: 'org-0002',
    clusters: ['edge-1'],
  });
});

test('org admin: reaches the page from the nav and sees only their org’s tokens', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  api.seed({
    orgs: [PROD, OTHER],
    agentTokens: [
      agentToken({ id: 'tok-mine', name: 'my-agent', org_id: 'org-0001' }),
      agentToken({ id: 'tok-theirs', name: 'their-agent', org_id: 'org-0002' }),
      agentToken({ id: 'tok-legacy', name: 'legacy-agent', org_id: '' }),
    ],
  });
  await page.goto('/');
  await page.getByRole('link', { name: 'Agent Tokens' }).click();

  await expect(page.getByRole('heading', { name: 'Agent Tokens' })).toBeVisible();
  await expect(page.getByText('my-agent')).toBeVisible();
  await expect(page.getByText('their-agent')).toHaveCount(0);
  await expect(page.getByText('legacy-agent')).toHaveCount(0);
  await expect(page.getByTestId('tokens-unbound-notice')).toHaveCount(0);
  // Binding moves a token between orgs: never offered below app admin.
  await expect(page.getByTestId('token-bind-tok-mine')).toHaveCount(0);
  expect(api.calls('AdminService/ListAgentTokens')[0].body).toMatchObject({ orgId: 'org-0001' });
});

test('org admin: creates a token bound to their org, which the dialog fixes', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  api.seed({ orgs: [PROD, OTHER], agentTokens: [] });
  await page.goto('/admin/tokens');

  await page.getByRole('button', { name: /new token/i }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').pressSequentially('prod-agent', { delay: 10 });
  const orgField = dialog.getByTestId('token-org');
  await expect(orgField).toBeDisabled();
  await expect(orgField).toHaveValue('Production Org (prod-org)');
  await dialog.getByTestId('token-roles').pressSequentially('metrics, logs', { delay: 10 });
  await dialog.getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText(/only time/i)).toBeVisible();
  expect(api.calls('AdminService/CreateAgentToken')[0].body).toMatchObject({
    name: 'prod-agent',
    orgId: 'org-0001',
    roles: ['metrics', 'logs'],
  });
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByText('prod-agent')).toBeVisible();
});

test('org admin: revoking is scoped to their org', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  api.seed({
    orgs: [PROD],
    agentTokens: [agentToken({ id: 'tok-mine', name: 'my-agent', org_id: 'org-0001' })],
  });
  await page.goto('/admin/tokens');
  await page.getByRole('button', { name: 'Revoke' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke' }).click();

  await expect(page.getByText('revoked', { exact: true })).toBeVisible();
  expect(api.calls('AdminService/RevokeAgentToken')[0].body).toMatchObject({
    id: 'tok-mine',
    orgId: 'org-0001',
  });
});

for (const [name, persona] of [
  ['org editor', orgEditor],
  ['viewer', viewer],
] as const) {
  test(`${name}: no Agent Tokens link, and the route is refused before it lists`, async ({
    page,
    api,
  }) => {
    await api.loginAs(persona);
    api.seed({ orgs: [PROD], agentTokens: [agentToken({ org_id: 'org-0001' })] });
    await page.goto('/');
    await expect(page.getByRole('link', { name: 'Collectors' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Agent Tokens' })).toHaveCount(0);

    await page.goto('/admin/tokens');
    await expect(page.getByTestId('route-denied')).toBeVisible();
    expect(api.calls('AdminService/ListAgentTokens')).toHaveLength(0);
  });
}
