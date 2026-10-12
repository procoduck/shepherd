// F3 (2026-10-09 walkthrough): an org viewer on the owning team may write
// the pipeline (auth.AuthorizeOwnership), and the page even said so, but the
// UI gated every pipeline control on the org role alone and showed it
// read-only. The pipeline page and the list now read Pipeline.can_edit — the
// server's own ownership answer — per pipeline. The mock computes it with the
// same rule (org editor+, or a member of the owning team via `myTeamIds`).
import { currentSchemaVersion } from '@/visual/schemaVersion';
import { basicScenario, pipeline } from '../fixtures/factories';
import { type MeResponse, reader } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { expect, test } from '../fixtures/test';

const teamMember: MeResponse = {
  ...reader,
  userOid: 'u-team-member',
  email: 'member@example.com',
  displayName: 'Team Member',
};

function seedOwned(api: { seed: (partial: Record<string, unknown>) => void }, myTeamIds: string[]) {
  const s = basicScenario();
  const [owned, other, git] = s.pipelines;
  api.seed({
    orgs: [s.org],
    myTeamIds,
    pipelines: [{ ...owned, owner_team_id: 'team-1' }, other, git],
  });
  return { owned, other };
}

test('a viewer on the owning team edits, saves, toggles and may delete that pipeline', async ({
  page,
  api,
}) => {
  await api.loginAs(teamMember);
  const { owned } = seedOwned(api, ['team-1']);
  await page.goto(`/pipelines/${owned.id}`);

  const save = page.getByRole('button', { name: 'Save', exact: true });
  await expect(save).toBeVisible();
  await expect(page.getByRole('switch', { name: `Enabled: ${owned.name}` })).toBeVisible();
  await expect(page.getByTestId('pipeline-delete-btn')).toBeVisible();

  await page.locator('.cm-content').click();
  await page.keyboard.press('End');
  await page.keyboard.type(' // by the team');
  await save.click();
  await expect(
    page.locator('[data-sonner-toast]').filter({ hasText: 'Pipeline saved' }),
  ).toBeVisible();
  expect(api.calls('PipelineService/UpdatePipeline')).toHaveLength(1);

  await page.getByRole('switch', { name: `Enabled: ${owned.name}` }).click();
  await expect(page.getByRole('switch', { name: `Enabled: ${owned.name}` })).toHaveAttribute(
    'aria-checked',
    'false',
  );
});

test("a viewer on the owning team gets nothing on another team's or an unowned pipeline", async ({
  page,
  api,
}) => {
  await api.loginAs(teamMember);
  const { other } = seedOwned(api, ['team-1']);
  await page.goto(`/pipelines/${other.id}`);
  await expect(page.getByRole('heading', { level: 1, name: other.name })).toBeVisible();
  await expect(page.getByTestId('pipeline-enabled-status')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toHaveCount(0);
  await expect(page.getByRole('switch')).toHaveCount(0);
});

test('a viewer on no team stays read-only on a team-owned pipeline', async ({ page, api }) => {
  await api.loginAs(reader);
  const { owned } = seedOwned(api, []);
  await page.goto(`/pipelines/${owned.id}`);
  await expect(page.getByTestId('pipeline-enabled-status')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toHaveCount(0);
  await expect(page.getByRole('switch')).toHaveCount(0);
  await expect(page.getByTestId('pipeline-delete-btn')).toHaveCount(0);
});

test("the list offers the enable toggle on the team's own pipeline only", async ({ page, api }) => {
  await api.loginAs(teamMember);
  const { owned, other } = seedOwned(api, ['team-1']);
  await page.goto('/pipelines');
  await expect(
    page
      .getByTestId(`pipeline-row-${owned.name}`)
      .getByRole('switch', { name: `Enabled: ${owned.name}` }),
  ).toBeVisible();
  await expect(page.getByTestId(`pipeline-row-${other.name}`).getByRole('switch')).toHaveCount(0);
  // Creating stays an org-role decision: a viewer is offered no New pipeline.
  await expect(page.getByTestId('pipeline-new')).toHaveCount(0);
});

// #319 (maintainer-approved RBAC change): VisualService Render, Validate and
// UpgradeCheck are authorized per pipeline — anyone who may edit the pipeline
// (Pipeline.can_edit) — so a member of the owning team below org editor uses
// the builder on their team's pipeline like an editor does. A new graph and
// Simulate stay org-editor; the builder says so instead of offering them.
const visualGraph = {
  kind: 'alloy-graph/v1',
  schema_version: currentSchemaVersion(schemaFixture),
  nodes: [
    {
      id: 'n1',
      component: 'prometheus.exporter.self',
      label: 'self',
      position: { x: 100, y: 100 },
      props: {},
      disabled: false,
      notes: '',
    },
  ],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-visual-builder' },
};

function seedVisual(
  api: { seed: (partial: Record<string, unknown>) => void },
  myTeamIds: string[],
) {
  const s = basicScenario();
  const visual = {
    ...pipeline({ id: 'pip-vis', name: 'team-visual', source: 'visual' }),
    owner_team_id: 'team-1',
    wizard_state: visualGraph,
  };
  api.seed({ orgs: [s.org], schema: schemaFixture, myTeamIds, pipelines: [visual] });
  return visual;
}

test.describe('a builder-made pipeline, for its owning team below org editor', () => {
  test('the builder is editable and saves, naming the pipeline on Render', async ({
    page,
    api,
  }) => {
    await api.loginAs(teamMember);
    const visual = seedVisual(api, ['team-1']);
    await page.goto(`/pipelines/${visual.id}/visual`);
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('.react-flow__node')).toHaveCount(1);

    await expect(page.getByTestId('toolbar-read-only')).toHaveCount(0);
    await expect(page.getByTestId('palette-read-only')).toHaveCount(0);

    // The graph is theirs to change: the lone (unwired, so blocking) node goes.
    await page.click('[data-testid="pipeline-node"]', { force: true });
    await expect(page.locator('.react-flow__node.selected')).toHaveCount(1);
    await page.focus('[data-testid="canvas"]');
    await page.keyboard.press('Delete');
    await expect(page.locator('.react-flow__node')).toHaveCount(0);

    await page.getByTestId('toolbar-name').fill('team-visual-renamed');
    const matcher = page.getByTestId('matcher-input');
    await matcher.fill('team="checkout"');
    await matcher.press('Enter');

    const save = page.getByTestId('toolbar-save');
    await expect(save).toBeEnabled();
    await save.click();
    await expect(page).toHaveURL(new RegExp(`/pipelines/${visual.id}\\?from=visual$`));

    const renders = api.calls('VisualService/Render');
    expect(renders.length).toBeGreaterThan(0);
    expect(renders.at(-1)?.body).toMatchObject({ pipelineId: visual.id });
    const updates = api.calls('PipelineService/UpdatePipeline');
    expect(updates).toHaveLength(1);
    expect(updates[0]?.body).toMatchObject({
      id: visual.id,
      name: 'team-visual-renamed',
      matchers: expect.arrayContaining(['team="checkout"']),
    });
  });

  test('Simulate is disabled, with the reason, for a team member below org editor', async ({
    page,
    api,
  }) => {
    await api.loginAs(teamMember);
    const visual = seedVisual(api, ['team-1']);
    await page.goto(`/pipelines/${visual.id}/visual`);
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });

    const sandbox = page.getByTestId('simulate-menu-trigger');
    await expect(sandbox).toBeDisabled();
    await expect(sandbox).toHaveAttribute('title', /Simulation needs the org editor role/);

    await page.getByTestId('drawer-tab-simulate').click();
    const relabel = page.getByTestId('simulate-relabel-run');
    await expect(relabel).toBeDisabled();
    await expect(relabel).toHaveAttribute('title', /Simulation needs the org editor role/);
    expect(api.calls('SimulateService/')).toHaveLength(0);
  });

  test("another team's builder pipeline stays read-only with the viewer message", async ({
    page,
    api,
  }) => {
    await api.loginAs(teamMember);
    const visual = seedVisual(api, ['team-2']);
    await page.goto(`/pipelines/${visual.id}/visual`);
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('toolbar-read-only')).toBeVisible();
    await expect(page.getByTestId('toolbar-save')).toBeDisabled();
    await expect(page.getByTestId('palette-read-only')).toContainText(
      "Viewers can't change pipelines",
    );
  });

  test('a new visual pipeline stays org-editor', async ({ page, api }) => {
    await api.loginAs(teamMember);
    seedVisual(api, ['team-1']);
    await page.goto('/pipelines/visual/new');
    await expect(page.getByTestId('route-denied')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('visual-builder')).toHaveCount(0);
  });

  test('the pipeline page links the builder with no read-only note', async ({ page, api }) => {
    await api.loginAs(teamMember);
    const visual = seedVisual(api, ['team-1']);
    await page.goto(`/pipelines/${visual.id}`);
    await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeVisible();

    const open = page.getByTestId('editor-open-visual');
    await expect(open).toBeVisible();
    await expect(open).not.toContainText('read-only');
    await expect(open).not.toHaveAttribute('title', /.*/);
    await expect(page.getByTestId('editor-visual-text-only-note')).toHaveCount(0);
  });

  // #324 review: moving from an editable pipeline to one the member may not
  // edit, inside the same builder mount, must not keep the first one's edit
  // right.
  test("moving from the team's pipeline to another team's in the builder turns it read-only", async ({
    page,
    api,
  }) => {
    await api.loginAs(teamMember);
    const s = basicScenario();
    const mine = {
      ...pipeline({ id: 'pip-mine', name: 'mine', source: 'visual' }),
      owner_team_id: 'team-1',
      wizard_state: visualGraph,
    };
    const theirs = {
      ...pipeline({ id: 'pip-theirs', name: 'theirs', source: 'visual' }),
      owner_team_id: 'team-2',
      wizard_state: visualGraph,
    };
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      myTeamIds: ['team-1'],
      pipelines: [mine, theirs],
    });
    await page.goto(`/pipelines/${mine.id}/visual`);
    await expect(page.locator('.react-flow__node')).toHaveCount(1, { timeout: 10_000 });
    await expect(page.getByTestId('toolbar-read-only')).toHaveCount(0);

    // Client-side, so the builder stays mounted and only its pipelineId changes.
    await page.evaluate((id) => {
      window.history.pushState({}, '', `/pipelines/${id}/visual`);
      window.dispatchEvent(new PopStateEvent('popstate'));
    }, theirs.id);
    await expect(page.getByTestId('toolbar-pipeline-id')).toHaveText(theirs.id);
    await expect(page.getByTestId('toolbar-read-only')).toBeVisible();
    await expect(page.getByTestId('toolbar-save')).toBeDisabled();
  });

  test('a viewer on no team keeps the plain viewer message', async ({ page, api }) => {
    await api.loginAs(reader);
    const visual = seedVisual(api, []);
    await page.goto(`/pipelines/${visual.id}/visual`);
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('palette-read-only')).toContainText(
      "Viewers can't change pipelines",
    );
    await expect(page.getByTestId('simulate-menu-trigger')).toHaveAttribute(
      'title',
      /Viewers can't change pipelines/,
    );

    await page.goto(`/pipelines/${visual.id}`);
    const open = page.getByTestId('editor-open-visual');
    await expect(open).toBeVisible();
    await expect(open).not.toContainText('read-only');
  });
});
