/**
 * Tenant routes (/tenant-routes).
 *
 * A tenant route pairs the org's tenant identity with a rotatable, unguessable
 * path segment. This drives the create / rotate / revoke lifecycle over the
 * real generated TenantRouteService client and the mock handlers. Writes are
 * org-admin; a viewer sees the table but no controls.
 */
import { basicScenario } from '../fixtures/factories';
import { appAdmin, reader } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('creates, rotates and revokes a tenant route', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({ orgs: [s.org], tenantRoutes: [] });
  await api.loginAs(appAdmin);
  await page.goto('/tenant-routes');

  const panel = page.getByTestId('tenant-routes');
  await expect(panel.getByRole('heading', { name: 'Tenant routes' })).toBeVisible();
  await expect(panel.getByText('No tenant routes yet.')).toBeVisible();

  // Create an OTLP route in managed mode. Gateway name is required in both
  // modes (the server needs it to name the managed Gateway too).
  await page.getByTestId('route-new').click();
  await page.getByTestId('route-kind').selectOption('otlp');
  await page.getByTestId('route-gateway-name').fill('shepherd-receiver-gw');
  await page.getByRole('button', { name: 'Create route' }).click();

  // It appears, active, with a server-minted segment.
  const seg = await panel
    .locator('td', { hasText: /^\/otlp\// })
    .first()
    .textContent();
  expect(seg).toContain('/otlp/');
  await expect(panel.getByText('active')).toBeVisible();
  expect(api.calls('/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute').length).toBe(1);

  // Rotate: the old segment goes deprecated, a new active one is minted.
  const segment = seg?.replace('/otlp/', '') ?? '';
  await page.getByTestId(`route-rotate-${segment}`).click();
  await page.getByTestId('route-overlap-hours').fill('12');
  await page.getByRole('button', { name: 'Rotate', exact: true }).click();
  await expect(panel.getByText('deprecated')).toBeVisible();
  expect(api.calls('/shepherd.mgmt.v1.TenantRouteService/RotateTenantRoute').length).toBe(1);

  // Revoke the now-deprecated original.
  await page.getByTestId(`route-revoke-${segment}`).click();
  await page.getByRole('button', { name: /^Revoke$/ }).click();
  await expect(panel.getByText('revoked')).toBeVisible();
  expect(api.calls('/shepherd.mgmt.v1.TenantRouteService/RevokeTenantRoute').length).toBe(1);
});

test('a viewer sees the routes but no write controls', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    tenantRoutes: [
      {
        id: 'tr-1',
        org_id: s.org.id,
        tenant_id: 'tenant-x',
        kind: 'otlp',
        segment: 'otlp-abc123',
        status: 'active',
        gateway_mode: 'managed',
        gateway_name: '',
        gateway_namespace: '',
        created_at: '2026-09-17T09:00:00Z',
        updated_at: '2026-09-17T09:00:00Z',
      },
    ],
  });
  await api.loginAs(reader);
  await page.goto('/tenant-routes');

  const panel = page.getByTestId('tenant-routes');
  await expect(panel.getByText('/otlp/otlp-abc123')).toBeVisible();
  await expect(page.getByTestId('route-new')).toHaveCount(0);
  await expect(page.getByTestId('route-rotate-otlp-abc123')).toHaveCount(0);
});

test('surfaces the missing-tenant-identity precondition', async ({ page, api }) => {
  const s = basicScenario();
  // The org has no tenant identity yet, so the server refuses Create with
  // failed_precondition; the page maps that to a specific, actionable message.
  api.seed({ orgs: [s.org], tenantRoutes: [], tenantRoutesNoIdentity: true });
  await api.loginAs(appAdmin);
  await page.goto('/tenant-routes');

  await page.getByTestId('route-new').click();
  await page.getByTestId('route-gateway-name').fill('shepherd-receiver-gw');
  await page.getByRole('button', { name: 'Create route' }).click();

  await expect(page.getByText(/no tenant identity yet/i)).toBeVisible();
});
