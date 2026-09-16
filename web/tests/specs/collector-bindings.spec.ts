/**
 * Admin → Single sign-on → Collector identity bindings.
 *
 * The agent_identities bindings map a collector's OIDC identity (issuer + app
 * id) to an organisation. This drives the create/list/delete flow the section
 * exposes, over the real generated AdminService client and the mock handlers.
 */
import { basicScenario } from '../fixtures/factories';
import { appAdmin, orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('creates, lists and deletes a collector binding', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], agentIdentities: [] });
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');

  const section = page.getByTestId('collector-bindings');
  await expect(section).toBeVisible();
  await expect(section.getByText('No collector identity bindings yet.')).toBeVisible();

  await page.getByTestId('binding-new').click();
  await page.getByTestId('binding-issuer').fill('https://idp.example/');
  await page.getByTestId('binding-app-id').fill('client-eu');
  await page.getByTestId('binding-org').fill('prod-org');
  await page.getByTestId('binding-clusters').fill('prod-eu-1, staging-eu-1');
  await page.getByTestId('binding-submit').click();

  // The new binding appears in the table, with its org and cluster allowlist.
  await expect(section.getByText('client-eu')).toBeVisible();
  await expect(section.getByText('prod-org')).toBeVisible();
  await expect(section.getByText('prod-eu-1')).toBeVisible();

  await page.getByTestId('binding-delete-client-eu').click();
  await page.getByRole('button', { name: /^Remove$/ }).click();
  await expect(section.getByText('No collector identity bindings yet.')).toBeVisible();
});

test('surfaces an unknown-organisation error', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], agentIdentities: [] });
  await api.loginAs(appAdmin);
  await page.goto('/admin/auth');

  await page.getByTestId('binding-new').click();
  await page.getByTestId('binding-issuer').fill('https://idp/');
  await page.getByTestId('binding-app-id').fill('c');
  await page.getByTestId('binding-org').fill('no-such-org');
  await page.getByTestId('binding-submit').click();

  await expect(page.getByText(/unknown organisation/i)).toBeVisible();
});

test('a non-app-admin never reaches the section', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  await page.goto('/admin/auth');
  // The route guard denies /admin/* to a non-app-admin before the page mounts.
  await expect(page.getByTestId('collector-bindings')).toHaveCount(0);
});
