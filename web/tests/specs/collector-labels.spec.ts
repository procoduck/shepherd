import { basicScenario } from '../fixtures/factories';
import { orgAdmin, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('labels persist across navigation and support editing, grouping and deletion', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto(`/collectors/${s.collectors[0].id}`);
  await page.getByRole('button', { name: 'Manage labels' }).click();
  await page.getByLabel('Label key', { exact: true }).fill('environment');
  await page.getByLabel('Label value', { exact: true }).fill('production');
  await page.getByRole('button', { name: 'Add label', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Edit label environment' })).toBeVisible();
  await page.reload();
  await page.getByRole('button', { name: 'Attributes & Labels' }).click();
  await page.getByRole('button', { name: 'Edit label environment' }).click();
  await page.getByLabel('Label value', { exact: true }).fill('staging');
  await page.getByRole('button', { name: 'Save label', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Collector labels' })).toContainText('staging');
  await page.goto('/collectors');
  await page.getByLabel('Group collectors by label').selectOption('environment');
  await expect(page.getByRole('heading', { name: 'environment=staging (1)' })).toBeVisible();
  await page.getByLabel('Search collectors').fill('environment=staging');
  await expect(page.getByRole('link', { name: s.collectors[0].cluster, exact: true })).toHaveCount(
    1,
  );
  await page.goto(`/collectors/${s.collectors[0].id}`);
  await page.getByRole('button', { name: 'Attributes & Labels' }).click();
  await page.getByRole('button', { name: 'Delete label environment' }).click();
  await expect(page.getByText('No labels', { exact: true })).toBeVisible();
});

test('all reported attributes remain visible per instance to a reader', async ({ page, api }) => {
  await api.loginAs(reader);
  const s = basicScenario();
  s.collectors[0].labels = { team: 'payments' };
  s.collectors[0].instances = [
    { name: 'node-a', local_attributes: { region: 'east', 'custom.label': 'first-instance-only' } },
    { name: 'node-b', local_attributes: { region: 'west', 'collector.os': 'linux' } },
  ];
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto(`/collectors/${s.collectors[0].id}`);
  await expect(page.getByRole('button', { name: 'Manage labels' })).toHaveCount(0);
  await page.getByRole('button', { name: 'View labels' }).click();
  const attrs = page.getByRole('region', { name: 'Alloy attributes' });
  await expect(attrs).toContainText('first-instance-only');
  await expect(attrs).toContainText('east');
  await expect(attrs).toContainText('west');
  await expect(attrs).toContainText('collector.os');
  await expect(page.getByRole('button', { name: 'Add label', exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Delete label team' })).toHaveCount(0);
});

test('a failed label save preserves the draft', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.goto(`/collectors/${s.collectors[0].id}`);
  await page.getByRole('button', { name: 'Attributes & Labels' }).click();
  await page.getByLabel('Label key', { exact: true }).fill('team');
  await page.getByLabel('Label value', { exact: true }).fill('payments');
  api.failNext('POST', '/shepherd.mgmt.v1.FleetService/SetCollectorLabel', 500, 'Save failed');
  await page.getByRole('button', { name: 'Add label', exact: true }).click();
  await expect(page.getByLabel('Label value', { exact: true })).toHaveValue('payments');
  await expect(page.getByText('No labels', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Add label', exact: true })).toBeEnabled();
});

test('long attributes and label controls fit a desktop viewport', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  s.collectors[0].labels = { 'team.with.a.long.name': 'payments-and-infrastructure'.repeat(4) };
  s.collectors[0].local_attributes = { 'custom.attribute': 'value'.repeat(40) };
  api.seed({ orgs: [s.org], collectors: s.collectors });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(`/collectors/${s.collectors[0].id}`);
  await page.getByRole('button', { name: 'Attributes & Labels' }).click();
  const labels = page.getByRole('region', { name: 'Collector labels' });
  await expect(labels).toBeVisible();
  const box = await labels.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.width).toBeGreaterThan(250);
  expect(box!.x + box!.width).toBeLessThanOrEqual(1280);
  await expect(page.getByRole('button', { name: 'Add label', exact: true })).toBeInViewport();
  await page.screenshot({ path: 'test-results/collector-labels-desktop.png', fullPage: true });
});
