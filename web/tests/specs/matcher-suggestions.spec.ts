// matcher-suggestions.spec.ts — #139: the matcher inputs offer the key="value"
// matchers FleetService.ListAttributes reports for the org (the server lists
// only keys matching evaluates), as a native datalist the input is wired to.
import { basicScenario, pipeline } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('the pipeline editor suggests matchers from the org attributes', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  const p = pipeline({
    id: 'pip-suggest',
    org_id: s.org.id,
    name: 'suggest-me',
    contents: '// ok',
  });
  api.seed({
    orgs: [s.org],
    pipelines: [p],
    attributes: { cluster: ['prod-eu-1'], role: ['metrics'], team: ['payments'] },
  });
  await page.goto('/pipelines/pip-suggest');

  const input = page.getByPlaceholder(/Enter to add/);
  await expect(input).toHaveAttribute('list', 'pipeline-matcher-suggestions');
  const options = page.locator('#pipeline-matcher-suggestions option');
  await expect(options).toHaveCount(3);
  await expect(options.nth(0)).toHaveAttribute('value', 'cluster="prod-eu-1"');
  await expect(options.nth(2)).toHaveAttribute('value', 'team="payments"');

  // Picking a suggestion is typing it: Enter adds it as a matcher chip.
  await input.fill('team="payments"');
  await input.press('Enter');
  await expect(page.getByText('team="payments"', { exact: true })).toBeVisible();
});
