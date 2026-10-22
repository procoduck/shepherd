import { basicScenario } from '../fixtures/factories';
import { appAdmin, orgAdmin, orgEditor, viewer } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

/*
 * S6: one query-error pattern. Before this, ten pages had no error branch at
 * all — a failed list query rendered as an empty state ("No collectors",
 * "No wizards are registered"), telling a viewer nothing went wrong when in
 * fact nothing loaded. This is table-driven across every route this
 * workstream touches: seed an org, fail the list call the page depends on,
 * and require a role='alert' element with a message — not a silent empty
 * state.
 */

const CASES: Array<{ route: string; procedure: string }> = [
  { route: '/collectors', procedure: 'FleetService/ListCollectors' },
  { route: '/wizards', procedure: 'WizardService/ListWizards' },
  { route: '/admin/orgs', procedure: 'AdminService/ListOrgs' },
  { route: '/admin/clusters', procedure: 'AdminService/ListClusters' },
  { route: '/admin/tokens', procedure: 'AdminService/ListAgentTokens' },
  { route: '/teams', procedure: 'TeamService/ListTeams' },
];

for (const { route, procedure } of CASES) {
  test(`${route} shows an alert, not an empty state, when its list query fails`, async ({
    page,
    api,
  }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({ orgs: [s.org] });
    api.failNext('POST', `/shepherd.mgmt.v1.${procedure}`, 503, 'unavailable');
    await page.goto(route);

    const alert = page.getByRole('alert');
    await expect(alert).toBeVisible({ timeout: 5000 });
    await expect(alert).toContainText(/fail|error|retry|permission/i);
  });
}

test('collector detail shows an alert when GetCollector fails', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  api.failNext('POST', '/shepherd.mgmt.v1.FleetService/GetCollector', 503, 'unavailable');
  await page.goto('/collectors/col-0001');

  const alert = page.getByRole('alert');
  await expect(alert).toBeVisible({ timeout: 5000 });
  await expect(alert).toContainText(/fail|error|retry|permission/i);
});

test('pipelines list still shows an alert when ListPipelines fails (retired bespoke error box)', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  api.failNext('POST', '/shepherd.mgmt.v1.PipelineService/ListPipelines', 503, 'unavailable');
  await page.goto('/pipelines');

  const alert = page.getByRole('alert');
  await expect(alert).toBeVisible({ timeout: 5000 });
  await expect(alert).toContainText(/fail|error|retry|permission/i);
});

test('teams page keeps its teams-error testid after moving onto QueryError', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  api.failNext('POST', '/shepherd.mgmt.v1.TeamService/ListTeams', 503, 'unavailable');
  await page.goto('/teams');

  await expect(page.getByTestId('teams-error')).toBeVisible({ timeout: 5000 });
});

// W7-08: real, positive coverage of the query-error pattern for the roles
// each route actually admits (routeManifest.ts), not just appAdmin.

test('teams page shows the error state for an org admin too (org-viewer floor)', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  api.failNext('POST', '/shepherd.mgmt.v1.TeamService/ListTeams', 503, 'unavailable');
  await page.goto('/teams');

  await expect(page.getByTestId('teams-error')).toBeVisible({ timeout: 5000 });
});

test('collectors list shows an alert for a viewer too (no elevated requirement)', async ({
  page,
  api,
}) => {
  await api.loginAs(viewer);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  api.failNext('POST', '/shepherd.mgmt.v1.FleetService/ListCollectors', 503, 'unavailable');
  await page.goto('/collectors');

  const alert = page.getByRole('alert');
  await expect(alert).toBeVisible({ timeout: 5000 });
  await expect(alert).toContainText(/fail|error|retry|permission/i);
});

test('wizards list shows an alert for an org editor too (org editor floor)', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  api.failNext('POST', '/shepherd.mgmt.v1.WizardService/ListWizards', 503, 'unavailable');
  await page.goto('/wizards');

  const alert = page.getByRole('alert');
  await expect(alert).toBeVisible({ timeout: 5000 });
  await expect(alert).toContainText(/fail|error|retry|permission/i);
});
