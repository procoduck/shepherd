import { basicScenario, destination, pipeline } from '../fixtures/factories';
import { orgAdmin, orgEditor, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

/*
 * M4 (2026-10-08 pre-release walkthrough): destination refusals told people to
 * "re-run its wizard" or "point them at another destination" — neither is an
 * action the UI has (running a wizard again creates a NEW pipeline). The
 * server now names the actions that exist; these specs hold the UI to making
 * them one click away: every pipeline a refusal names links to its page, and
 * a hand-edited wizard pipeline's page offers "Restore last wizard version".
 *
 * Red run (before the UI change): the link specs found no link named after
 * the pipeline in the form error, the delete spec found the refusal in a
 * toast with the dialog closed, and the restore specs found no
 * restore-wizard-version button and no wizard badge in the revision history.
 */

const WIZARD_TEXT = 'prometheus.remote_write "metrics" { endpoint { url = "https://old" } }';
const EDITED_TEXT = `${WIZARD_TEXT}\n// tuned by hand`;

function seedWithPipelines(api: { seed: (partial: Record<string, unknown>) => void }) {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [destination({ id: 'dst-r', name: 'prom-prod', auth_mode: 'none' })],
    pipelines: [
      pipeline({ id: 'pip-self', name: 'self-mon', source: 'wizard' }),
      pipeline({ id: 'pip-app', name: 'app-obs', source: 'wizard' }),
      // Same name as the destination: a quoted destination name in the
      // refusal must not become a pipeline link.
      pipeline({ id: 'pip-clash', name: 'prom-prod', source: 'ui' }),
    ],
  });
}

test('an update refused for a hand-edited wizard pipeline links the pipeline to its page', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  seedWithPipelines(api);
  const refusal =
    'destination "prom-prod" was not updated: 1 wizard pipeline(s) using it cannot be ' +
    'regenerated: "self-mon": its contents were edited by hand after its wizard generated ' +
    "them, and regenerating it would discard that edit — on the pipeline's page, restore its " +
    'last wizard-generated revision, detach it from the wizard, or delete it';
  api.override('POST', '/shepherd.mgmt.v1.DestinationService/UpdateDestination', (route) =>
    route.fulfill({
      status: 400,
      contentType: 'application/json',
      body: JSON.stringify({ code: 'failed_precondition', message: refusal }),
    }),
  );
  await page.goto('/destinations');

  await page.getByRole('button', { name: 'Edit prom-prod' }).click();
  const dialog = page.getByRole('dialog', { name: 'Edit prom-prod' });
  await dialog.getByRole('button', { name: 'Save' }).click();

  const error = dialog.getByTestId('form-error');
  await expect(error).toContainText('restore its last wizard-generated revision');
  const link = error.getByRole('link', { name: 'self-mon', exact: true });
  await expect(link).toHaveAttribute('href', '/pipelines/pip-self');
  // The destination's own quoted name is not a pipeline link, even when a
  // pipeline happens to share it.
  await expect(error.getByRole('link', { name: 'prom-prod' })).toHaveCount(0);

  await link.click();
  await expect(page).toHaveURL(/\/pipelines\/pip-self$/);
});

test('a delete refused because wizard pipelines use it stays in the dialog with each pipeline linked', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  seedWithPipelines(api);
  api.override('POST', '/shepherd.mgmt.v1.DestinationService/DeleteDestination', (route) =>
    route.fulfill({
      status: 400,
      contentType: 'application/json',
      body: JSON.stringify({
        code: 'failed_precondition',
        message:
          'destination "prom-prod" is used by 2 wizard pipeline(s): "self-mon", "app-obs" — ' +
          "detach them from the wizard or delete them first, each on its pipeline's page",
      }),
    }),
  );
  await page.goto('/destinations');

  await page.getByRole('button', { name: 'Delete destination' }).click();
  const dialog = page.getByRole('dialog', { name: 'Delete destination' });
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click();

  const error = dialog.getByTestId('form-error');
  await expect(error).toContainText('Cannot delete');
  await expect(error).toContainText('detach them from the wizard or delete them first');
  await expect(error.getByRole('link', { name: 'self-mon', exact: true })).toHaveAttribute(
    'href',
    '/pipelines/pip-self',
  );
  await expect(error.getByRole('link', { name: 'app-obs', exact: true })).toHaveAttribute(
    'href',
    '/pipelines/pip-app',
  );
  await expect(page.getByRole('cell', { name: 'prom-prod', exact: true })).toBeVisible();
});

function seedHandEdited(
  api: { seed: (partial: Record<string, unknown>) => void },
  contents = EDITED_TEXT,
) {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    pipelines: [
      pipeline({
        id: 'pip-hand',
        name: 'self-mon',
        source: 'wizard',
        contents,
        enabled: true,
        revisions: [
          {
            revision: 3,
            changed_by: 'ada@example.com',
            changed_at: '2026-10-02T10:00:00Z',
            change_note: 'updated',
            contents: EDITED_TEXT,
            matchers: [`cluster="prod-eu-1"`],
            enabled: true,
          },
          {
            revision: 2,
            changed_by: 'grace@example.com',
            changed_at: '2026-10-01T10:00:00Z',
            change_note: 're-rendered: destination "prom-prod" updated',
            contents: WIZARD_TEXT,
            matchers: [`cluster="prod-eu-1"`],
            enabled: true,
          },
          {
            revision: 1,
            changed_by: 'grace@example.com',
            changed_at: '2026-09-30T10:00:00Z',
            change_note: 'created',
            contents: `${WIZARD_TEXT} // older`,
            matchers: [`cluster="prod-eu-1"`],
            enabled: false,
          },
        ],
      }),
    ],
  });
}

test('a hand-edited wizard pipeline offers "Restore last wizard version", which restores the newest wizard revision', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  seedHandEdited(api);
  await page.goto('/pipelines/pip-hand');

  // The revisions the wizard wrote are marked as such.
  await page.getByRole('button', { name: /revision history \(3\)/i }).click();
  await expect(page.getByTestId('wizard-revision-badge')).toHaveCount(2);

  const offer = page.getByTestId('restore-wizard-version');
  await expect(offer).toContainText('edited by hand');
  await offer.getByRole('button', { name: 'Restore last wizard version' }).click();

  const dialog = page.getByTestId('restore-dialog');
  await expect(dialog).toContainText('Restore revision #2');
  await expect(dialog.getByTestId('restore-wizard-note')).toContainText('last version');
  await dialog.getByTestId('confirm-restore-btn').click();

  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'Restored' })).toBeVisible();
  const calls = api.calls('PipelineService/RestoreRevision');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ id: 'pip-hand', revision: 2 });
  // Its text is the wizard's again: nothing left to restore.
  await expect(page.getByTestId('restore-wizard-version')).toHaveCount(0);
});

test('no "Restore last wizard version" while the text is what the wizard wrote, or for a viewer', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  seedHandEdited(api, WIZARD_TEXT);
  await page.goto('/pipelines/pip-hand');
  await expect(page.getByText('Source:').locator('span')).toHaveText('wizard');
  await expect(page.getByTestId('detach-wizard-btn')).toBeVisible();
  await expect(page.getByTestId('restore-wizard-version')).toHaveCount(0);

  await api.loginAs(reader);
  seedHandEdited(api);
  await page.goto('/pipelines/pip-hand');
  await expect(page.getByText('Source:').locator('span')).toHaveText('wizard');
  await expect(page.getByTestId('restore-wizard-version')).toHaveCount(0);
});
