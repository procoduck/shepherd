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

// 2026-10-09 kind re-check: the builder stays gated on the org role on
// purpose (its Render call is org-editor), but for a member of the owning
// team it said "Viewers can't change pipelines" — false, since they may edit
// the pipeline's text. It now says what they can do and links there; the
// pipeline page says the builder will open read-only and what a text edit of
// a builder-made pipeline means.
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
  test('the read-only builder points to the text editor, not "viewers cannot edit"', async ({
    page,
    api,
  }) => {
    await api.loginAs(teamMember);
    const visual = seedVisual(api, ['team-1']);
    await page.goto(`/pipelines/${visual.id}/visual`);
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.locator('.react-flow__node')).toHaveCount(1);

    // Still read-only: the builder's actions are org-editor on the server.
    await expect(page.getByTestId('toolbar-read-only')).toBeVisible();
    await expect(page.getByTestId('toolbar-save')).toBeDisabled();

    const note = page.getByTestId('palette-read-only');
    await expect(note).toContainText('The visual builder needs the org editor role');
    await expect(note).not.toContainText("Viewers can't change pipelines");
    await expect(page.getByTestId('toolbar-read-only')).toHaveAttribute(
      'title',
      /needs the org editor role.*owning team/,
    );

    await note.getByRole('link', { name: "edit this pipeline's text on its page" }).click();
    await expect(page).toHaveURL(new RegExp(`/pipelines/${visual.id}$`));
    await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeVisible();
  });

  test('the pipeline page says the builder opens read-only and what a text edit means', async ({
    page,
    api,
  }) => {
    await api.loginAs(teamMember);
    const visual = seedVisual(api, ['team-1']);
    await page.goto(`/pipelines/${visual.id}`);
    await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeVisible();

    const open = page.getByTestId('editor-open-visual');
    await expect(open).toHaveAttribute('title', /opens read-only/i);
    await expect(open).toContainText('read-only');
    await expect(page.getByTestId('editor-visual-text-only-note')).toContainText(
      'you can edit and save this text here',
    );
  });

  test('a viewer on no team keeps the plain viewer message and gets no text-edit note', async ({
    page,
    api,
  }) => {
    await api.loginAs(reader);
    const visual = seedVisual(api, []);
    await page.goto(`/pipelines/${visual.id}/visual`);
    await expect(page.getByTestId('visual-builder')).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId('palette-read-only')).toContainText(
      "Viewers can't change pipelines",
    );

    await page.goto(`/pipelines/${visual.id}`);
    const open = page.getByTestId('editor-open-visual');
    await expect(open).toBeVisible();
    await expect(open).not.toHaveAttribute('title', /.*/);
    await expect(open).not.toContainText('read-only');
    await expect(page.getByTestId('editor-visual-text-only-note')).toHaveCount(0);
  });
});
