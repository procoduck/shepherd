import { basicScenario, org, pipeline } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// Regression coverage for B3 (docs/project-status.md): useOrgId() used to be
// hardcoded to me.orgs[0] with no way to reach a second org's data. These
// specs cover the switcher's visibility rule and that switching actually
// repopulates an org-scoped page.

const orgA = org({ id: 'org-0001', name: 'platform-org', display_name: 'Platform Org' });
const orgB = org({ id: 'org-0002', name: 'data-eng', display_name: 'Data Eng' });

const twoOrgAdmin = {
  userOid: 'u-two-org-admin',
  email: 'twoorg@example.com',
  displayName: 'Two-Org Admin',
  isAppAdmin: true,
  authMethod: 'oidc',
  orgs: [
    { id: orgA.id, name: orgA.name, displayName: orgA.display_name, role: 'admin' },
    { id: orgB.id, name: orgB.name, displayName: orgB.display_name, role: 'admin' },
  ],
};

test('switching org repopulates the pipelines list', async ({ page, api }) => {
  await api.loginAs(twoOrgAdmin);
  const pipelines = [
    pipeline({ id: 'pip-a1', org_id: orgA.id, name: 'alpha-pipeline' }),
    pipeline({ id: 'pip-b1', org_id: orgB.id, name: 'bravo-pipeline' }),
  ];
  api.seed({ orgs: [orgA, orgB], pipelines });

  // The default mock handler for ListPipelines ignores org scoping (see
  // tests/mocks/handlers.ts); override it to filter by the request's orgId
  // like the real backend does, so this test actually exercises org-scoped
  // repopulation rather than a mock that was already org-agnostic.
  api.override('POST', '/shepherd.mgmt.v1.PipelineService/ListPipelines', async (route) => {
    const req = route.request().postDataJSON() as { orgId?: string };
    const items = (api.state.pipelines as Array<Record<string, unknown>>)
      .filter((p) => p.org_id === req.orgId)
      .map((p) => ({
        id: p.id,
        orgId: p.org_id,
        name: p.name,
        contents: p.contents,
        matchers: p.matchers ?? [],
        enabled: !!p.enabled,
        source: p.source,
      }));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items, total: items.length }),
    });
  });

  await page.goto('/pipelines');
  await expect(page.getByText('alpha-pipeline')).toBeVisible();
  await expect(page.getByText('bravo-pipeline')).not.toBeVisible();

  const switcher = page.getByTestId('org-switcher');
  await expect(switcher).toBeVisible();
  await expect(switcher).toHaveAccessibleName(/organization/i);

  await switcher.selectOption(orgB.id);

  await expect(page.getByText('bravo-pipeline')).toBeVisible();
  await expect(page.getByText('alpha-pipeline')).not.toBeVisible();
});

test('org switcher is hidden for a user with only one org', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByText('ui-enabled')).toBeVisible();
  await expect(page.getByTestId('org-switcher')).toHaveCount(0);
});
