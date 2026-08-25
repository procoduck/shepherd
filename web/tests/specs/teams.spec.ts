/**
 * Teams.
 *
 * Membership reaches a team two ways — an identity provider group, or an
 * explicit list of local users — and the page exists to make which one
 * visible. The states worth pinning are the ones an operator misreads: a
 * group-backed team looks empty (its roster lives in the IdP and cannot be
 * listed), and a team with neither source really is empty.
 */
import { appAdmin, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('shows where each team gets its members from', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/teams');

  await expect(page.getByRole('heading', { name: 'Teams' })).toBeVisible();

  // Group-backed: names the group, and does NOT claim a member count — the
  // roster is in the IdP, so any number shown here would be a lie.
  await expect(page.getByTestId('team-source-group-platform')).toContainText('platform-engineers');
  await expect(page.getByTestId('team-source-members-platform')).toHaveCount(0);

  // Explicitly membered: a real count, no group.
  await expect(page.getByTestId('team-source-members-local-squad')).toContainText('1 member');
  await expect(page.getByTestId('team-source-group-local-squad')).toHaveCount(0);

  // Neither: says so, rather than rendering as an empty group-backed team.
  await expect(page.getByTestId('team-row-empty-team')).toContainText('no members yet');
});

test('lists only explicit members, and says why the group ones are absent', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  await page.goto('/teams');

  await page.getByTestId('team-members-platform').click();
  // The note is the whole point: an empty list beside a configured group would
  // read as "this group has nobody in it".
  await expect(page.getByTestId('team-members-group-note')).toContainText('platform-engineers');
  await expect(page.getByTestId('team-members-empty')).toBeVisible();
});

test('adds and removes an explicit member, and the count follows', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/teams');

  await page.getByTestId('team-members-empty-team').click();
  await page.getByTestId('team-member-add-select').selectOption('user-2');
  await page.getByTestId('team-member-add').click();
  await expect(page.getByTestId('team-member-alice')).toBeVisible();

  await page.getByTestId('team-member-remove-alice').click();
  await expect(page.getByTestId('team-members-empty')).toBeVisible();
});

test('creates a team with no group, for a deployment with no identity provider', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  await page.goto('/teams');

  await page.getByTestId('team-new').click();
  await page.getByTestId('team-name').fill('no-idp');
  // Deliberately leaving the group empty — the case that did not exist before
  // teams could hold explicit members.
  await page.getByRole('button', { name: /^create$/i }).click();

  await expect(page.getByTestId('team-row-no-idp')).toContainText('no members yet');
});

test('refuses a duplicate team name and a group already bound elsewhere', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  await page.goto('/teams');

  await page.getByTestId('team-new').click();
  await page.getByTestId('team-name').fill('platform');
  await page.getByRole('button', { name: /^create$/i }).click();
  await expect(page.getByText(/team name already exists/i)).toBeVisible();

  // The group collision must name the group, not the name: sending an admin
  // off to rename a team when the real conflict is elsewhere wastes the trip.
  await page.getByTestId('team-name').fill('something-else');
  await page.getByTestId('team-group').fill('platform-engineers');
  await page.getByRole('button', { name: /^create$/i }).click();
  await expect(page.getByText(/already bound to that group/i)).toBeVisible();
});

test('a viewer sees the teams but is offered no way to change them', async ({ page, api }) => {
  await api.loginAs(reader);
  await page.goto('/teams');

  await expect(page.getByTestId('team-row-platform')).toBeVisible();
  await expect(page.getByTestId('team-new')).toHaveCount(0);
  await expect(page.getByTestId('team-members-platform')).toHaveCount(0);
  await expect(page.getByTestId('team-delete-platform')).toHaveCount(0);
});
