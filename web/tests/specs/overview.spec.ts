import { collector, org, pipeline } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('overview shows stat cards', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001' });
  api.seed({ orgs: [o], collectors: [collector({ id: 'col-0001' })] });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: /Overview/i })).toBeVisible();
});

// F3: Collectors / Active pipelines / Clusters tiles are wired to real
// data — one ListCollectors + one ListPipelines call, no per-item fan-out.
test('overview tiles reflect real collector/pipeline/cluster counts', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001' });
  api.seed({
    orgs: [o],
    collectors: [
      collector({ id: 'col-0001', cluster_id: 'clu-0001', cluster: 'prod-eu-1' }),
      collector({ id: 'col-0002', cluster_id: 'clu-0001', cluster: 'prod-eu-1', role: 'logs' }),
      collector({ id: 'col-0003', cluster_id: 'clu-0002', cluster: 'prod-us-1' }),
    ],
    pipelines: [
      pipeline({ id: 'pip-0001', enabled: true }),
      pipeline({ id: 'pip-0002', enabled: true }),
      pipeline({ id: 'pip-0003', enabled: false }),
    ],
  });
  await page.goto('/');

  // Tile order is Orgs / Collectors / Active pipelines / Clusters:
  // 1 org, 3 collectors, 2 of 3 pipelines enabled, 2 distinct clusters.
  await expect(async () => {
    const values = await page.locator('p.text-2xl').allTextContents();
    expect(values).toEqual(['1', '3', '2', '2']);
  }).toPass();

  expect(api.calls('/shepherd.mgmt.v1.FleetService/ListCollectors').length).toBeLessThanOrEqual(2);
  expect(api.calls('/shepherd.mgmt.v1.PipelineService/ListPipelines').length).toBeLessThanOrEqual(
    2,
  );
  // No per-cluster or per-pipeline fan-out for the derived tiles.
  expect(api.calls('/shepherd.mgmt.v1.AdminService/ListClusters')).toHaveLength(0);
});

// Regression coverage for F3's app-admin org-scoping bug
// (docs/project-status.md): FleetService.ListCollectors used to special-case
// IsAppAdmin sessions with an unscoped ListAllCollectors query, so the
// Collectors/Clusters tiles showed every org's collectors regardless of
// req.orgId or which org the switcher had selected. The default mock
// handler for ListCollectors ignores org scoping (see tests/mocks/
// handlers.ts); override it to filter by the request's orgId like the real
// (now-fixed) backend does, so this exercises org-scoped repopulation
// rather than a mock that was already org-agnostic.
test('Collectors/Clusters tiles repopulate for an app-admin switching org', async ({
  page,
  api,
}) => {
  const orgA = org({ id: 'org-0001', name: 'platform-org', display_name: 'Platform Org' });
  const orgB = org({ id: 'org-0002', name: 'data-eng', display_name: 'Data Eng' });
  await api.loginAs({
    userOid: 'u-two-org-admin',
    email: 'twoorg@example.com',
    displayName: 'Two-Org Admin',
    isAppAdmin: true,
    authMethod: 'oidc',
    orgs: [
      { id: orgA.id, name: orgA.name, displayName: orgA.display_name, role: 'admin' },
      { id: orgB.id, name: orgB.name, displayName: orgB.display_name, role: 'admin' },
    ],
  });
  const collectors = [
    collector({ id: 'col-a1', cluster_id: 'clu-a1', cluster: 'platform-eu-1', org_id: orgA.id }),
    collector({ id: 'col-b1', cluster_id: 'clu-b1', cluster: 'data-eng-eu-1', org_id: orgB.id }),
    collector({ id: 'col-b2', cluster_id: 'clu-b2', cluster: 'data-eng-us-1', org_id: orgB.id }),
  ];
  api.seed({ orgs: [orgA, orgB], collectors, pipelines: [] });

  api.override('POST', '/shepherd.mgmt.v1.FleetService/ListCollectors', async (route) => {
    const req = route.request().postDataJSON() as { orgId?: string };
    const items = (api.state.collectors as Array<Record<string, unknown>>)
      .filter((c) => c.org_id === req.orgId)
      .map((c) => ({ id: c.id, clusterId: c.cluster_id, cluster: c.cluster, role: c.role }));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ items, total: items.length }),
    });
  });

  await page.goto('/');

  // org A: 1 collector, 1 cluster.
  await expect(async () => {
    const values = await page.locator('p.text-2xl').allTextContents();
    expect(values).toEqual(['2', '1', '0', '1']);
  }).toPass();

  await page.getByTestId('org-switcher').selectOption(orgB.id);

  // org B: 2 collectors, 2 clusters — must change, not stay pinned to org A
  // or silently show every org's collectors.
  await expect(async () => {
    const values = await page.locator('p.text-2xl').allTextContents();
    expect(values).toEqual(['2', '2', '0', '2']);
  }).toPass();
});

test('overview tiles show a per-tile loading state, not a dash', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const o = org({ id: 'org-0001' });
  api.seed({ orgs: [o], collectors: [collector({ id: 'col-0001' })] });

  // Delay, then let the default handlers serve the seeded data.
  api.delay('POST', '/shepherd.mgmt.v1.FleetService/ListCollectors', 300);
  api.delay('POST', '/shepherd.mgmt.v1.PipelineService/ListPipelines', 300);

  await page.goto('/');
  // While the collectors/pipelines calls are in flight, those tiles show an
  // ellipsis loading indicator rather than the hardcoded '—' placeholder.
  const values = await page.locator('p.text-2xl').allTextContents();
  expect(values).not.toContain('—');

  await expect(async () => {
    const settled = await page.locator('p.text-2xl').allTextContents();
    expect(settled).toEqual(['1', '1', '0', '0']);
  }).toPass();
});
