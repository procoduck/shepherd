// F3 (2026-10-09 walkthrough): an org viewer on the owning team may write
// the pipeline (auth.AuthorizeOwnership), and the page even said so, but the
// UI gated every pipeline control on the org role alone and showed it
// read-only. The pipeline page and the list now read Pipeline.can_edit — the
// server's own ownership answer — per pipeline. The mock computes it with the
// same rule (org editor+, or a member of the owning team via `myTeamIds`).
import { basicScenario } from '../fixtures/factories';
import { type MeResponse, reader } from '../fixtures/personas';
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
