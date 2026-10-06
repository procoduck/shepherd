/**
 * #253: grouping the collectors list renders one table per label value, and
 * each table used to size its columns to its own content, so the Status/Last
 * Seen/... columns of consecutive groups did not line up. The columns now have
 * fixed widths, so every group's header cells sit at the same x offsets.
 */
import { basicScenario } from '../fixtures/factories';
import { orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('grouped collector tables line their columns up', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  // Very different content widths per group: a long cluster name and long
  // labels in one group, short ones in the other.
  s.collectors[0].cluster = 'a-really-quite-long-cluster-name-for-the-prod-region-eu-west';
  s.collectors[0].labels = { env: 'prod', owner: 'platform-observability-team-long-label' };
  s.collectors[1].labels = { env: 'prod' };
  s.collectors[2].cluster = 'c';
  s.collectors[2].labels = { env: 'dev' };
  s.collectors[3].labels = { env: 'dev' };
  api.seed({ orgs: [s.org], collectors: s.collectors });

  await page.goto('/collectors');
  await page.getByLabel('Group by').selectOption('env');

  const tables = page.getByRole('table');
  await expect(tables).toHaveCount(2);
  const lefts = async (i: number) =>
    tables
      .nth(i)
      .locator('thead th')
      .evaluateAll((ths) => ths.map((th) => Math.round(th.getBoundingClientRect().left)));
  const first = await lefts(0);
  const second = await lefts(1);
  expect(first.length).toBe(6);
  expect(second).toEqual(first);
});
