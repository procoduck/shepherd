import { fromJson, toBinary } from '@bufbuild/protobuf';
import { StructSchema } from '@bufbuild/protobuf/wkt';
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
 * The pipelines come from the refusal's Connect error detail (a
 * google.protobuf.Struct, internal/mgmtapi pipelines_detail.go), never from
 * the message text, which also quotes the destination and Alloy labels.
 *
 * Red runs: before the UI change the link specs found no link in the form
 * error, the delete spec found the refusal in a toast with the dialog closed,
 * and the restore specs found no restore-wizard-version button and no wizard
 * badge. After the review of #291: with links parsed from the message, the
 * diagnostic-label spec linked "default"; the matcher-warning and
 * cancel-leaves-no-diff specs failed before their fixes.
 */

const WIZARD_TEXT = 'prometheus.remote_write "metrics" { endpoint { url = "https://old" } }';
const EDITED_TEXT = `${WIZARD_TEXT}\n// tuned by hand`;

/** A Connect JSON error carrying the server's pipelines detail. */
function refusal(message: string, pipelines: { id: string; name: string }[]) {
  const st = fromJson(StructSchema, { pipelines });
  return JSON.stringify({
    code: 'failed_precondition',
    message,
    details: [
      {
        type: 'google.protobuf.Struct',
        value: Buffer.from(toBinary(StructSchema, st)).toString('base64'),
      },
    ],
  });
}

function seedWithPipelines(api: { seed: (partial: Record<string, unknown>) => void }) {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [destination({ id: 'dst-r', name: 'prom-prod', auth_mode: 'none' })],
    pipelines: [
      pipeline({ id: 'pip-self', name: 'self-mon', source: 'wizard' }),
      pipeline({ id: 'pip-app', name: 'app-obs', source: 'wizard' }),
      // Names the message also quotes, for other reasons: neither may
      // become a link.
      pipeline({ id: 'pip-clash', name: 'prom-prod', source: 'ui' }),
      pipeline({ id: 'pip-default', name: 'default', source: 'ui' }),
    ],
  });
}

test('an update refused for a hand-edited wizard pipeline links the pipeline to its page', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  seedWithPipelines(api);
  const message =
    'destination "prom-prod" was not updated: 1 wizard pipeline(s) using it cannot be ' +
    'regenerated: "self-mon": its contents were edited by hand after its wizard generated ' +
    "them, and regenerating it would discard that edit — on the pipeline's page, restore its " +
    'last wizard-generated revision, detach it from the wizard, or delete it';
  api.override('POST', '/shepherd.mgmt.v1.DestinationService/UpdateDestination', (route) =>
    route.fulfill({
      status: 400,
      contentType: 'application/json',
      body: refusal(message, [{ id: 'pip-self', name: 'self-mon' }]),
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
  await expect(error.getByRole('link')).toHaveCount(1);

  await link.click();
  await expect(page).toHaveURL(/\/pipelines\/pip-self$/);
});

// Review of #291: a Stage diagnostic quotes Alloy labels, and one may equal
// a pipeline's name. Only the pipelines the server lists are linked.
test('a quoted Alloy label in a refusal is not linked, even when a pipeline has that name', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  seedWithPipelines(api);
  const message =
    'destination "prom-prod" was not updated: 1 wizard pipeline(s) using it cannot be ' +
    'regenerated: "self-mon": stage 2, line 4: prometheus.remote_write "default": endpoint ' +
    "url is invalid — change the destination so it can be regenerated, or, on the pipeline's " +
    'page, detach it from the wizard or delete it';
  api.override('POST', '/shepherd.mgmt.v1.DestinationService/UpdateDestination', (route) =>
    route.fulfill({
      status: 400,
      contentType: 'application/json',
      body: refusal(message, [{ id: 'pip-self', name: 'self-mon' }]),
    }),
  );
  await page.goto('/destinations');

  await page.getByRole('button', { name: 'Edit prom-prod' }).click();
  const dialog = page.getByRole('dialog', { name: 'Edit prom-prod' });
  await dialog.getByRole('button', { name: 'Save' }).click();

  const error = dialog.getByTestId('form-error');
  await expect(error).toContainText('prometheus.remote_write "default"');
  await expect(error.getByRole('link', { name: 'self-mon', exact: true })).toBeVisible();
  await expect(error.getByRole('link', { name: 'default' })).toHaveCount(0);
  await expect(error.getByRole('link', { name: 'prom-prod' })).toHaveCount(0);
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
      body: refusal(
        'destination "prom-prod" is used by 2 wizard pipeline(s): "self-mon", "app-obs" — ' +
          "detach them from the wizard or delete them first, each on its pipeline's page",
        [
          { id: 'pip-self', name: 'self-mon' },
          { id: 'pip-app', name: 'app-obs' },
        ],
      ),
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

const CURRENT_MATCHERS = [`cluster="prod-eu-1"`, `team="payments"`];

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
        matchers: CURRENT_MATCHERS,
        enabled: true,
        revisions: [
          {
            revision: 3,
            changed_by: 'ada@example.com',
            changed_at: '2026-10-02T10:00:00Z',
            change_note: 'updated',
            contents: EDITED_TEXT,
            matchers: CURRENT_MATCHERS,
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
  // Restore also puts back the revision's matchers (review of #291).
  const matchers = dialog.getByTestId('restore-matchers-change');
  await expect(matchers).toContainText('team="payments"');
  await expect(matchers).toContainText('restored: cluster="prod-eu-1"');
  await dialog.getByTestId('confirm-restore-btn').click();

  await expect(page.locator('[data-sonner-toast]').filter({ hasText: 'Restored' })).toBeVisible();
  const calls = api.calls('PipelineService/RestoreRevision');
  expect(calls).toHaveLength(1);
  expect(calls[0].body).toMatchObject({ id: 'pip-hand', revision: 2 });
  // Its text is the wizard's again: nothing left to restore.
  await expect(page.getByTestId('restore-wizard-version')).toHaveCount(0);
});

// Review of #291 (nit 7): the offer opens the confirmation without the diff
// view; cancelling must return to the editor, not strand the diff of #2.
test('cancelling "Restore last wizard version" returns to the editor', async ({ page, api }) => {
  await api.loginAs(orgEditor);
  seedHandEdited(api);
  await page.goto('/pipelines/pip-hand');

  await page
    .getByTestId('restore-wizard-version')
    .getByRole('button', { name: 'Restore last wizard version' })
    .click();
  await page.getByTestId('restore-dialog').getByRole('button', { name: 'Cancel' }).click();

  await expect(page.getByTestId('restore-dialog')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Back to editor' })).toHaveCount(0);
  await expect(page.getByTestId('validate-btn')).toBeVisible();
  expect(api.calls('PipelineService/RestoreRevision')).toHaveLength(0);
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
