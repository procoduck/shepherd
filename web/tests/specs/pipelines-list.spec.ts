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

// 2026-10-09 re-check: the rows showed a pointer cursor and did nothing when
// clicked (the collectors list had the same dead affordance).
test('clicking a row opens the pipeline; its toggle still only toggles', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');
  const row = page.getByTestId('pipeline-row-ui-enabled');

  await row.getByRole('switch', { name: 'Enabled: ui-enabled' }).click();
  await expect(row.getByRole('switch', { name: 'Enabled: ui-enabled' })).toHaveAttribute(
    'aria-checked',
    'false',
  );
  await expect(page).toHaveURL(/\/pipelines$/);

  await row.getByText('cluster="prod-eu-1"').click();
  await expect(page).toHaveURL(/\/pipelines\/pip-0001$/);
});

test('the enable toggle is a switch whose knob sits at the start when off (#208)', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const on = pipeline({ id: 'pip-on', name: 'on-pipe', enabled: true });
  const off = pipeline({ id: 'pip-off', name: 'off-pipe', enabled: false });
  api.seed({ orgs: [s.org], pipelines: [on, off] });
  await page.goto('/pipelines');

  const onSwitch = page.getByRole('switch', { name: 'Enabled: on-pipe' });
  const offSwitch = page.getByRole('switch', { name: 'Enabled: off-pipe' });
  await expect(onSwitch).toHaveAttribute('aria-checked', 'true');
  await expect(offSwitch).toHaveAttribute('aria-checked', 'false');

  // The knob stays inside its track, at the left when off and the right when on.
  for (const [sw, atRight] of [
    [offSwitch, false],
    [onSwitch, true],
  ] as const) {
    const track = await sw.boundingBox();
    const knob = await sw.locator('span').boundingBox();
    if (!track || !knob) throw new Error('no layout');
    expect(knob.x).toBeGreaterThanOrEqual(track.x);
    expect(knob.x + knob.width).toBeLessThanOrEqual(track.x + track.width + 0.5);
    const knobCenter = knob.x + knob.width / 2;
    const trackCenter = track.x + track.width / 2;
    expect(knobCenter > trackCenter).toBe(atRight);
  }
});
