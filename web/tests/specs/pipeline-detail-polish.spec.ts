import { basicScenario, destination } from '../fixtures/factories';
import { type MeResponse, orgAdmin, orgEditor, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// #252 — pipelines page polish: a heading on the New pipeline page, enable and
// delete on the pipeline page, assigning the owning team, revision times,
// no Restore on the current revision, and an optional wizard destination
// that can be cleared again.

// A team member is an org viewer whose write reach comes from OWNING a
// pipeline through a team. Here they are on no team that owns the pipeline
// (no `myTeamIds`), so Pipeline.can_edit is false and they get a viewer's
// page; the owning-team case is pipeline-team-edit.spec.ts (F3).
const teamMember: MeResponse = {
  ...reader,
  userOid: 'u-team-member',
  email: 'member@example.com',
  displayName: 'Team Member',
};

function seedOne(
  api: { seed: (partial: Record<string, unknown>) => void },
  extra: Record<string, unknown> = {},
) {
  const s = basicScenario();
  const p = { ...s.pipelines[0], ...extra };
  api.seed({ orgs: [s.org], pipelines: [p, ...s.pipelines.slice(1)] });
  return { s, p };
}

// ── Heading ────────────────────────────────────────────────────────────────

test('the New pipeline page has a heading', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  seedOne(api);
  await page.goto('/pipelines/new');
  await expect(page.getByRole('heading', { level: 1, name: 'New pipeline' })).toBeVisible();
});

test('the pipeline page is headed by the pipeline name', async ({ page, api }) => {
  await api.loginAs(reader);
  const { p } = seedOne(api);
  await page.goto(`/pipelines/${p.id}`);
  await expect(page.getByRole('heading', { level: 1, name: p.name })).toBeVisible();
});

// ── Enable / delete on the detail page ────────────────────────────────────

test('an editor disables and re-enables a pipeline from its page', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const { p } = seedOne(api);
  await page.goto(`/pipelines/${p.id}`);

  const toggle = page.getByRole('switch', { name: `Enabled: ${p.name}` });
  await expect(toggle).toHaveAttribute('aria-checked', 'true');
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-checked', 'false');
  expect(api.calls('PipelineService/DisablePipeline')).toHaveLength(1);

  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-checked', 'true');
  expect(api.calls('PipelineService/EnablePipeline')).toHaveLength(1);
});

test('an editor deletes a pipeline after confirming, and lands on the list', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const { p } = seedOne(api);
  await page.goto(`/pipelines/${p.id}`);

  await page.getByTestId('pipeline-delete-btn').click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText(p.name);
  // Cancel first: nothing is deleted.
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(api.calls('PipelineService/DeletePipeline')).toHaveLength(0);

  await page.getByTestId('pipeline-delete-btn').click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page).toHaveURL(/\/pipelines$/);
  await expect(page.getByTestId(`pipeline-row-${p.name}`)).toHaveCount(0);
  const calls = api.calls('PipelineService/DeletePipeline');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ id: p.id });
});

test('a git-managed pipeline offers enable but no delete (git is the source of truth)', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  const git = s.pipelines.find((x) => x.source === 'git');
  await page.goto(`/pipelines/${git?.id}`);
  await expect(page.getByRole('switch', { name: `Enabled: ${git?.name}` })).toBeVisible();
  await expect(page.getByTestId('pipeline-delete-btn')).toHaveCount(0);
});

for (const [label, persona] of [
  ['a viewer', reader],
  ['a team member whose team does not own it', teamMember],
] as const) {
  test(`${label} sees the enabled state but no enable or delete action`, async ({ page, api }) => {
    await api.loginAs(persona);
    const { p } = seedOne(api, { owner_team_id: 'team-1' });
    await page.goto(`/pipelines/${p.id}`);
    await expect(page.getByTestId('pipeline-enabled-status')).toHaveText('Enabled');
    await expect(page.getByRole('switch')).toHaveCount(0);
    await expect(page.getByTestId('pipeline-delete-btn')).toHaveCount(0);
  });
}

// ── Owning team ────────────────────────────────────────────────────────────

test('an org admin assigns and clears the owning team', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const { p } = seedOne(api);
  await page.goto(`/pipelines/${p.id}`);

  const picker = page.getByLabel('Owning team');
  await expect(picker).toHaveValue('');
  const options = await picker.locator('option').allTextContents();
  expect(options).toEqual(expect.arrayContaining(['platform', 'local-squad', 'empty-team']));

  await picker.selectOption({ label: 'platform' });
  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'platform' })).toBeVisible();
  let calls = api.calls('PipelineService/SetPipelineOwner');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ id: p.id, ownerTeamId: 'team-1' });
  await expect(picker).toHaveValue('team-1');

  await picker.selectOption('');
  await expect(
    page.locator('[data-sonner-toast]').filter({ hasText: 'Owner cleared' }),
  ).toBeVisible();
  calls = api.calls('PipelineService/SetPipelineOwner');
  expect(calls).toHaveLength(2);
  // An empty owner clears it (proto3 omits the empty string on the wire).
  expect(calls[1].body.ownerTeamId ?? '').toBe('');
  await expect(picker).toHaveValue('');
});

for (const [label, persona] of [
  ['an editor', orgEditor],
  ['a viewer', reader],
  ['a team member', teamMember],
] as const) {
  test(`${label} sees the owning team but cannot change it`, async ({ page, api }) => {
    await api.loginAs(persona);
    const { p } = seedOne(api, { owner_team_id: 'team-1' });
    await page.goto(`/pipelines/${p.id}`);
    await expect(page.getByTestId('pipeline-owner')).toHaveText('platform');
    await expect(page.getByLabel('Owning team')).toHaveCount(0);
  });
}

// ── Revision list: time and Restore on the current revision ──────────────

function seedRevisions(api: { seed: (partial: Record<string, unknown>) => void }) {
  return seedOne(api, {
    contents: 'rev2-line',
    revisions: [
      {
        revision: 2,
        changed_by: 'ada@example.com',
        changed_at: '2026-08-18T10:15:00Z',
        change_note: 'tighten',
        contents: 'rev2-line',
        matchers: [],
        enabled: true,
      },
      {
        revision: 1,
        changed_by: 'grace@example.com',
        changed_at: '2026-08-17T10:00:00Z',
        change_note: 'created',
        contents: 'rev1-line',
        matchers: [],
        enabled: true,
      },
    ],
  });
}

test('each revision shows when it was made: relative, with the exact time on hover', async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const { p } = seedRevisions(api);
  await page.goto(`/pipelines/${p.id}`);
  await page.getByRole('button', { name: /revision history \(2\)/i }).click();

  const time = page.locator('[data-testid="revision-time"][data-revision="2"]');
  await expect(time).toHaveText(/ago$|just now/);
  await expect(time).toHaveAttribute('datetime', '2026-08-18T10:15:00.000Z');
  // The absolute value carries the time of day, not just the date.
  const title = (await time.getAttribute('title')) ?? '';
  const expected = await page.evaluate(() =>
    new Date('2026-08-18T10:15:00Z').toLocaleString(undefined, {
      dateStyle: 'medium',
      timeStyle: 'short',
    }),
  );
  expect(title).toBe(expected);
});

test('Restore is not offered for the current revision, only for older ones', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const { p } = seedRevisions(api);
  await page.goto(`/pipelines/${p.id}`);
  await page.getByRole('button', { name: /revision history \(2\)/i }).click();
  await expect(page.getByTestId('current-revision-badge')).toHaveCount(1);

  await page.locator('[data-testid="view-revision-btn"][data-revision="2"]').click();
  await expect(page.getByTestId('revision-diff')).toBeVisible();
  await expect(page.getByTestId('restore-btn')).toHaveCount(0);
  await expect(page.getByTestId('current-revision-note')).toBeVisible();

  await page.getByRole('button', { name: /back to editor/i }).click();
  await page.locator('[data-testid="view-revision-btn"][data-revision="1"]').click();
  await expect(page.getByTestId('restore-btn')).toBeEnabled();
  await expect(page.getByTestId('current-revision-note')).toHaveCount(0);
});

// ── Wizard: an optional destination can be cleared again ─────────────────

test('an optional wizard destination can be set back to "skip" after picking one', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [
      destination({ id: 'dst-prom', name: 'prom-prod', type: 'prometheus' }),
      destination({ id: 'dst-loki', name: 'loki-prod', type: 'loki' }),
    ],
  });
  await page.goto('/wizards');
  await page.getByRole('link', { name: /app observability|start|begin/i }).click();
  await page.getByLabel('Metrics endpoint URL').fill('http://myapp:9090/metrics');
  await page.getByLabel('Job label').fill('my-app');
  await page.getByRole('button', { name: /next|continue/i }).click();
  await page.getByRole('button', { name: /next|continue/i }).click();

  const logsDest = page.getByLabel('Logs destination (Loki)');
  await expect(logsDest).toHaveValue('');
  await logsDest.selectOption('loki-prod');
  await expect(logsDest).toHaveValue('loki-prod');
  // The empty "skip" choice stays selectable on an optional field. (Playwright
  // can select a disabled <option> programmatically where a user cannot, so
  // the attribute is what proves a person can get back to "skip".)
  const skip = logsDest.locator('option[value=""]');
  await expect(skip).not.toHaveAttribute('disabled');
  await expect(skip).toHaveText(/skip/i);
  await logsDest.selectOption('');
  await expect(logsDest).toHaveValue('');

  // A REQUIRED destination still has no way back to empty.
  const metricsDest = page.getByLabel('Metrics destination');
  await expect(metricsDest.locator('option[value=""]')).toHaveAttribute('disabled');
});
