import { basicScenario } from '../fixtures/factories';
import { orgEditor, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// "Detach from wizard" (#262 follow-up): a wizard pipeline whose text was
// edited by hand blocks destination edits; detaching makes it an ordinary
// pipeline in place. The action sits on the pipeline page, asks first (it
// will no longer follow destination changes), and is hidden from viewers.

test('an editor detaches a wizard pipeline after confirming what it means', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  const wizardPipeline = s.pipelines.find((p) => p.source === 'wizard');
  expect(wizardPipeline).toBeDefined();
  await page.goto(`/pipelines/${wizardPipeline?.id}`);

  await page.getByTestId('detach-wizard-btn').click();
  const dialog = page.getByTestId('detach-wizard-dialog');
  await expect(dialog).toContainText('no longer follow destination changes');
  await dialog.getByTestId('confirm-detach-btn').click();

  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'Detached' })).toBeVisible();
  await expect(page.getByText('Source:').locator('span')).toHaveText('ui');
  await expect(page.getByTestId('detach-wizard-btn')).toHaveCount(0);
  const calls = api.calls('PipelineService/DetachFromWizard');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ id: wizardPipeline?.id });
});

test('cancelling the confirmation detaches nothing', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  const wizardPipeline = s.pipelines.find((p) => p.source === 'wizard');
  await page.goto(`/pipelines/${wizardPipeline?.id}`);

  await page.getByTestId('detach-wizard-btn').click();
  await page.getByTestId('detach-wizard-dialog').getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByTestId('detach-wizard-dialog')).toHaveCount(0);
  expect(api.calls('PipelineService/DetachFromWizard')).toHaveLength(0);
});

test('a viewer sees no Detach action, and a non-wizard pipeline offers none', async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org], pipelines: s.pipelines });
  const wizardPipeline = s.pipelines.find((p) => p.source === 'wizard');
  await page.goto(`/pipelines/${wizardPipeline?.id}`);
  await expect(page.getByText('Source:').locator('span')).toHaveText('wizard');
  await expect(page.getByTestId('detach-wizard-btn')).toHaveCount(0);

  await api.loginAs(orgEditor);
  const uiPipeline = s.pipelines.find((p) => p.source === 'ui');
  await page.goto(`/pipelines/${uiPipeline?.id}`);
  await expect(page.getByText('Source:').locator('span')).toHaveText('ui');
  await expect(page.getByTestId('detach-wizard-btn')).toHaveCount(0);
});
