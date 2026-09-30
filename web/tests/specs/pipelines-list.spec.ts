import { currentSchemaVersion } from '@/visual/schemaVersion';
import { basicScenario, pipeline } from '../fixtures/factories';
import { appAdmin, orgEditor } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { expect, test } from '../fixtures/test';

test('pipelines table shows all pipelines', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByRole('table')).toBeVisible();
  await expect(page.getByText('ui-enabled')).toBeVisible();
  await expect(page.getByText('wizard-disabled')).toBeVisible();
  await expect(page.getByText('git-pipe')).toBeVisible();
});

test('New pipeline button is visible for orgAdmin', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  await expect(page.getByRole('link', { name: /New pipeline/i })).toBeVisible();
});

test('a visual pipeline rendered under an older schema is badged', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  // "Current" is whatever the fixture's artifact is — the newest one on disk,
  // so it moves with every Alloy bump; hard-coding it made this spec badge
  // both rows after v1.20.1 landed.
  const currentVersion = currentSchemaVersion(schemaFixture);
  const stale = {
    ...pipeline({ id: 'pip-stale', name: 'stale-visual', source: 'visual' }),
    wizard_state: { schema_version: 'alloy-v1.12.0' },
  };
  const current = {
    ...pipeline({ id: 'pip-current', name: 'current-visual', source: 'visual' }),
    wizard_state: { schema_version: currentVersion },
  };
  api.seed({ orgs: [s.org], schema: schemaFixture, pipelines: [stale, current] });
  await page.goto('/pipelines');

  // Exactly the stale one is badged — the current-schema visual pipeline and
  // every non-visual pipeline get nothing.
  await expect(page.getByTestId('pipeline-stale-render')).toHaveCount(1);
  const staleRow = page.getByTestId('pipeline-row-stale-visual');
  const badge = staleRow.getByTestId('pipeline-stale-render');
  await expect(badge).toBeVisible();
  await expect(badge).toContainText('alloy-v1.12.0');
  await expect(badge).toHaveAttribute('href', '/pipelines/pip-stale/visual');
});
