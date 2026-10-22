/**
 * The org editor role, end to end through the UI.
 *
 * This spec exists because the role shipped server-side and the UI never
 * learned about it: useCanWrite checked for `admin` alone, so an editor — the
 * role whose entire purpose is authoring pipelines — got a read-only pipelines
 * page and a locked editor. The `orgEditor` persona had been defined, with a
 * comment describing exactly this, and no spec ever imported it.
 *
 * The distinction the role draws is what the assertions below pin: an editor
 * authors what the org RUNS, and cannot change what the org IS.
 */
import { basicScenario } from '../fixtures/factories';
import { localAdmin, orgAdmin, orgEditor, viewer } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('an editor can author pipelines', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');

  await expect(page.getByTestId('pipeline-new')).toBeVisible();
  await expect(page.getByTestId('pipeline-visual-builder')).toBeVisible();
});

test('an editor opens the pipeline editor writable, not read-only', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines/pip-0001');

  // Save is the affordance the read-only path removes entirely, so its presence
  // is the difference between "can author" and "can look".
  await expect(page.getByRole('button', { name: /save/i })).toBeVisible();
});

test('an editor cannot change where telemetry ships', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  await page.goto('/destinations');

  // Destinations are "what the org IS" — org admin on the server, so offering
  // the form here would only produce a rejection after it was filled in.
  await expect(page.getByRole('button', { name: /new destination/i })).toHaveCount(0);
});

test('a viewer still gets a read-only pipelines page', async ({ page, api }) => {
  // The control: widening write access to editors must not widen it to viewers.
  await api.loginAs(viewer);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');

  await expect(page.getByTestId('pipeline-new')).toHaveCount(0);
  await expect(page.getByTestId('pipeline-visual-builder')).toHaveCount(0);
});

test('a viewer cannot change where telemetry ships either', async ({ page, api }) => {
  // The same control as "an editor cannot change where telemetry ships",
  // one rung down: destinations are gated by useCanAdminister (org admin or
  // app admin), which a viewer clears no more than an editor does.
  await api.loginAs(viewer);
  await page.goto('/destinations');
  await expect(page.getByRole('button', { name: /new destination/i })).toHaveCount(0);
});

test('an org admin can also author pipelines, not just an app admin', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  await page.goto('/pipelines');

  await expect(page.getByTestId('pipeline-new')).toBeVisible();
  await expect(page.getByTestId('pipeline-visual-builder')).toBeVisible();
});

test('a local admin can also author pipelines', async ({ page, api }) => {
  // Unlike every other persona here, local admin's fixture carries no org
  // membership at all (isAppAdmin bypasses useCanWrite regardless), so the
  // pipelines list itself never resolves an orgId — this only works because
  // PipelinesPage renders its header and write affordances unconditionally
  // and shows "No pipelines yet." rather than an org-required gate.
  await api.loginAs(localAdmin);
  api.seed({ orgs: [] });
  await page.goto('/pipelines');

  await expect(page.getByTestId('pipeline-new')).toBeVisible();
  await expect(page.getByTestId('pipeline-visual-builder')).toBeVisible();
});
