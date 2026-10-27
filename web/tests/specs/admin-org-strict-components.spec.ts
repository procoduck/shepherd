// admin-org-strict-components.spec.ts — strict tenant mode accepts only
// components with a reviewed classification; an app admin may allow one more
// for an org (a component classified refused, or one no shipped schema
// classifies). Admin → Organisations → Edit lists them, each marked
// admin-allowed and unreviewed, and adds or removes one through its own
// app-admin RPC (AddOrgStrictComponent / RemoveOrgStrictComponent),
// independent of Save. A refusal — a component strict mode already accepts,
// a second entry, a removal while a pipeline uses it — shows the server's
// message.
import { expect } from '@playwright/test';
import { appAdmin } from '../fixtures/personas';
import { test } from '../fixtures/test';

const org = {
  id: 'org-0001',
  name: 'prod-org',
  display_name: 'Production Org',
  admin_group_id: 'admins',
  viewer_group_id: '',
  created_at: '2026-09-01T09:00:00Z',
  updated_at: '2026-09-01T09:00:00Z',
  tenants: [{ tenant: 'acme', kind: 'exact', is_default: true }],
};

test('lists the admin-allowed components as unreviewed, with their shipped classification', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [
      {
        ...org,
        strict_components: [
          { id: 'sc-1', component: 'otelcol.auth.google', created_by: 'root' },
          { id: 'sc-2', component: 'otelcol.receiver.future', created_by: 'root' },
        ],
      },
    ],
  });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-strict-components');

  const google = editor.getByTestId('org-strict-component-row-otelcol.auth.google');
  await expect(google.getByTestId('org-strict-component-status')).toHaveText(
    'admin-allowed, unreviewed (classified refused)',
  );
  await expect(google.getByTestId('org-strict-component-status')).toHaveAttribute(
    'title',
    /token_header/,
  );
  await expect(
    editor
      .getByTestId('org-strict-component-row-otelcol.receiver.future')
      .getByTestId('org-strict-component-status'),
  ).toHaveText('admin-allowed, unreviewed (unclassified)');
});

test('an app admin allows a component; one strict mode accepts and a duplicate are refused', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({ orgs: [{ ...org, strict_components: [] }] });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-strict-components');
  await expect(editor.getByTestId('org-strict-components-empty')).toBeVisible();

  // Caught before a round trip: not a component name at all.
  await editor.getByTestId('org-strict-component-new').pressSequentially('Otelcol Auth');
  await editor.getByTestId('org-strict-component-add').click();
  await expect(editor.getByTestId('org-strict-component-error')).toContainText(
    'Use a component name',
  );
  expect(api.calls('AdminService/AddOrgStrictComponent')).toHaveLength(0);

  await editor.getByTestId('org-strict-component-new').fill('');
  await editor.getByTestId('org-strict-component-new').pressSequentially('otelcol.auth.google');
  await editor.getByTestId('org-strict-component-new').press('Enter'); // adds, does not submit
  await expect(
    editor
      .getByTestId('org-strict-component-row-otelcol.auth.google')
      .getByTestId('org-strict-component-status'),
  ).toHaveText('admin-allowed, unreviewed (classified refused)');
  await expect(page.getByRole('dialog')).toBeVisible();
  expect(api.calls('AdminService/AddOrgStrictComponent')[0].body).toMatchObject({
    orgId: 'org-0001',
    component: 'otelcol.auth.google',
  });

  // Already accepted in strict mode: the server refuses it.
  await editor.getByTestId('org-strict-component-new').pressSequentially('loki.write');
  await editor.getByTestId('org-strict-component-add').click();
  await expect(editor.getByTestId('org-strict-component-error')).toContainText(
    'already accepts it',
  );

  // A second entry for the same component.
  await editor.getByTestId('org-strict-component-new').fill('');
  await editor.getByTestId('org-strict-component-new').pressSequentially('otelcol.auth.google');
  await editor.getByTestId('org-strict-component-add').click();
  await expect(editor.getByTestId('org-strict-component-error')).toContainText('already allows');
  expect(api.calls('AdminService/UpdateOrg')).toHaveLength(0);
});

test('a removal the server refuses names the pipelines; an unused one goes', async ({
  page,
  api,
}) => {
  await api.loginAs(appAdmin);
  api.seed({
    orgs: [
      {
        ...org,
        strict_components: [
          { id: 'sc-1', component: 'otelcol.auth.google', in_use_by: ['google-otlp'] },
          { id: 'sc-2', component: 'otelcol.receiver.future' },
        ],
      },
    ],
  });
  await page.goto('/admin/orgs');
  await page.getByRole('button', { name: 'Edit prod-org' }).click();
  const editor = page.getByTestId('org-strict-components');

  await editor.getByTestId('org-strict-component-remove-otelcol.auth.google').click();
  await expect(editor.getByTestId('org-strict-component-error')).toContainText(
    'pipeline "google-otlp"',
  );
  await expect(editor.getByTestId('org-strict-component-row-otelcol.auth.google')).toBeVisible();

  await editor.getByTestId('org-strict-component-remove-otelcol.receiver.future').click();
  await expect(editor.getByTestId('org-strict-component-row-otelcol.receiver.future')).toHaveCount(
    0,
  );
  await expect(editor.getByTestId('org-strict-component-error')).toHaveCount(0);
  expect(api.calls('AdminService/RemoveOrgStrictComponent').map((c) => c.body)).toEqual([
    expect.objectContaining({ orgId: 'org-0001', id: 'sc-1' }),
    expect.objectContaining({ orgId: 'org-0001', id: 'sc-2' }),
  ]);
});
