import { basicScenario, destination } from '../fixtures/factories';
import { appAdmin, orgAdmin, orgEditor, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

/*
 * Two sibling specs were removed rather than fixed. 'destinations page renders
 * heading' only asserted the heading, which every test below already does and
 * the fullstack walkthrough covers for all routes. 'destination list requires
 * its GET handler' was labelled a kill-switch probe but asserted the heading
 * too — it could not fail if the handler disappeared. The first test below is
 * the real kill-switch: it reads rows that only exist if the list is fetched.
 */

test('seeded destinations render name, type, and URL host', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: s.destinations });
  await page.goto('/destinations');

  await expect(page.getByText('prom-prod')).toBeVisible();
  await expect(page.getByRole('cell', { name: 'prometheus', exact: true })).toBeVisible();
  await expect(page.getByText(/prometheus\.example\.com/).first()).toBeVisible();
});

test('destination create dialog reports invalid URL', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new|create|add destination/i }).click();
  // A malformed URL, not an empty one: empty fields are caught by the inputs'
  // own `required` before submit ever runs, so they exercise the browser rather
  // than this dialog's validation.
  await page.getByLabel(/name/i).fill('bad-dest');
  await page.getByLabel(/url/i).fill('not-a-url');
  await page.getByRole('button', { name: /save|create/i }).click();

  // Assert the message itself. This used to match /valid URL|URL/i, whose second
  // branch matches the dialog's own "URL" field LABEL — so it passed whether or
  // not any validation rendered, and would not have caught the message going away.
  await expect(page.getByText(/Enter a valid URL/i)).toBeVisible();
});

test('created destination appears in the list', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new|create|add destination/i }).click();
  await page.getByLabel(/name/i).fill('new-destination');
  await page.getByLabel(/url/i).fill('https://new.example.com');
  await page.getByRole('button', { name: /save|create/i }).click();

  await expect(page.getByText('new-destination')).toBeVisible();
});

// The schema admits prometheus, loki and otlp. The third option in the type
// picker is labelled for Tempo but must submit "otlp"; before this spec it
// submitted "tempo", which the server refuses.
test('the traces destination option submits the otlp type the schema admits', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page.getByRole('button', { name: /new|create|add destination/i }).click();
  const dialog = page.getByRole('dialog', { name: 'New destination' });
  await dialog.getByLabel('Name', { exact: true }).fill('traces');
  await dialog.locator('select').first().selectOption({ label: 'Tempo (OTLP)' });
  await dialog.getByLabel('URL', { exact: true }).fill('http://tempo:4318');
  await dialog.getByRole('button', { name: /create/i }).click();

  await expect(page.getByText('traces')).toBeVisible();
  const creates = api.calls('DestinationService/CreateDestination');
  expect(creates).toHaveLength(1);
  expect((creates[0].body as Record<string, unknown>).type).toBe('otlp');
});

// /destinations carries no requiredRole (routeManifest.ts) — any
// authenticated org member reaches it — but write access is gated by
// useCanAdminister (appAdmin or org role "admin"), so an org admin sees
// the same write affordance an app admin does, while an editor and a
// reader see the list but not the button (W7-08: real, positive coverage
// for the roles this route actually ships, not just appAdmin).
test('an org admin, not just an app admin, can create a destination', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await expect(page.getByRole('button', { name: /new|create|add destination/i })).toBeVisible();
  await page.getByRole('button', { name: /new|create|add destination/i }).click();
  await page.getByLabel(/name/i).fill('org-admin-destination');
  await page.getByLabel(/url/i).fill('https://org-admin.example.com');
  await page.getByRole('button', { name: /save|create/i }).click();

  await expect(page.getByText('org-admin-destination')).toBeVisible();
});

test('orgEditor sees the destinations list but not the New destination button', async ({
  page,
  api,
}) => {
  await api.loginAs(orgEditor);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: s.destinations });
  await page.goto('/destinations');

  // Positive control first: the page actually rendered seeded data, so the
  // absence check below is not vacuous.
  await expect(page.getByText('prom-prod')).toBeVisible();
  await expect(page.getByRole('button', { name: /new|create|add destination/i })).toHaveCount(0);
  // Edit is gated exactly like create (#207).
  await expect(page.getByRole('button', { name: /^Edit / })).toHaveCount(0);
});

test('reader sees the destinations list but not the New destination button', async ({
  page,
  api,
}) => {
  await api.loginAs(reader);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: s.destinations });
  await page.goto('/destinations');

  await expect(page.getByText('prom-prod')).toBeVisible();
  await expect(page.getByRole('button', { name: /new|create|add destination/i })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^Edit / })).toHaveCount(0);
});

// #207: the auth column and the picker showed the stored enum (`basic_secret`)
// and the secret modes explained nothing. Labels are asserted alongside the
// absence of the raw value so a regression to `d.authMode` fails here.
test('auth modes render as human labels, never the stored value', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [
      destination({ id: 'dst-a', name: 'plain', auth_mode: 'none' }),
      destination({
        id: 'dst-b',
        name: 'basic-dest',
        auth_mode: 'basic_secret',
        secret_namespace: 'monitoring',
        secret_name: 'mimir-creds',
      }),
      destination({ id: 'dst-c', name: 'oauth-dest', auth_mode: 'oauth2_secret' }),
    ],
  });
  await page.goto('/destinations');

  await expect(page.getByRole('cell', { name: /Basic auth \(Kubernetes Secret\)/ })).toBeVisible();
  await expect(page.getByRole('cell', { name: /OAuth2 \(Kubernetes Secret\)/ })).toBeVisible();
  await expect(page.getByRole('cell', { name: 'None', exact: true })).toBeVisible();
  await expect(page.getByText('monitoring/mimir-creds')).toBeVisible();
  await expect(page.getByText('basic_secret')).toHaveCount(0);
  await expect(page.getByText('oauth2_secret')).toHaveCount(0);

  await page.getByRole('button', { name: /new destination/i }).click();
  const dialog = page.getByRole('dialog', { name: 'New destination' });
  await expect(dialog.getByLabel('Auth mode').locator('option')).toHaveText([
    'None',
    'Basic auth (Kubernetes Secret)',
    'OAuth2 (Kubernetes Secret)',
  ]);
});

test('a secret auth mode explains the Secret and asks for its reference', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org], destinations: [] });
  await page.goto('/destinations');

  await page
    .getByRole('button', { name: /new|create|add destination/i })
    .first()
    .click();
  const dialog = page.getByRole('dialog', { name: 'New destination' });
  await expect(dialog.getByTestId('auth-mode-explanation')).toHaveCount(0);

  await dialog.getByLabel('Auth mode').selectOption({ label: 'Basic auth (Kubernetes Secret)' });
  const explanation = dialog.getByTestId('auth-mode-explanation');
  await expect(explanation).toContainText('Kubernetes Secret');
  await expect(explanation).toContainText('never the credential itself');
  await expect(explanation).toContainText('Not applied yet');

  await dialog.getByLabel('Name', { exact: true }).pressSequentially('mimir');
  await dialog.getByLabel('URL', { exact: true }).pressSequentially('https://mimir.example.com');
  await dialog.getByLabel('Secret namespace').pressSequentially('monitoring');
  await dialog.getByLabel('Secret name', { exact: true }).pressSequentially('mimir-creds');
  await dialog.getByRole('button', { name: 'Create' }).click();

  await expect(page.getByText('monitoring/mimir-creds')).toBeVisible();
  const creates = api.calls('DestinationService/CreateDestination');
  expect(creates).toHaveLength(1);
  expect(creates[0].body).toMatchObject({
    authMode: 'basic_secret',
    secretNamespace: 'monitoring',
    secretName: 'mimir-creds',
  });
});

test('editing a destination sends UpdateDestination and the row updates', async ({ page, api }) => {
  await api.loginAs(orgAdmin);
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    destinations: [
      destination({ id: 'dst-e', name: 'prom-prod', tenant_id: 'acme', auth_mode: 'none' }),
    ],
  });
  await page.goto('/destinations');

  await page.getByRole('button', { name: 'Edit prom-prod' }).click();
  const dialog = page.getByRole('dialog', { name: 'Edit prom-prod' });
  await expect(dialog.getByLabel('Name', { exact: true })).toHaveValue('prom-prod');
  const url = dialog.getByLabel('URL', { exact: true });
  await url.clear();
  await url.pressSequentially('https://mimir.example.org/push');
  await dialog.getByLabel('Auth mode').selectOption({ label: 'OAuth2 (Kubernetes Secret)' });
  await expect(dialog.getByTestId('auth-mode-explanation')).toContainText('OAuth2');
  // The fixture's stored reference is prefilled; replace the name only.
  await expect(dialog.getByLabel('Secret namespace')).toHaveValue('monitoring');
  const secretName = dialog.getByLabel('Secret name', { exact: true });
  await secretName.clear();
  await secretName.pressSequentially('oauth-creds');
  await dialog.getByRole('button', { name: 'Save' }).click();

  await expect(dialog).toHaveCount(0);
  await expect(page.getByText('mimir.example.org')).toBeVisible();
  await expect(page.getByRole('cell', { name: /OAuth2 \(Kubernetes Secret\)/ })).toBeVisible();
  await expect(page.getByText('monitoring/oauth-creds')).toBeVisible();

  const updates = api.calls('DestinationService/UpdateDestination');
  expect(updates).toHaveLength(1);
  expect(updates[0].body).toMatchObject({
    id: 'dst-e',
    url: 'https://mimir.example.org/push',
    authMode: 'oauth2_secret',
    secretNamespace: 'monitoring',
    secretName: 'oauth-creds',
    // A field the form does not show survives the full-replace update.
    tenantId: 'acme',
  });
});
