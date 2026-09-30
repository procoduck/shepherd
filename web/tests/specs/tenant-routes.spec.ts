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

test('shows whether each route is in the cluster, with the reason when it is not', async ({
  page,
  api,
}) => {
  const s = basicScenario();
  const route = (segment: string, extra: Record<string, unknown>) => ({
    id: `tr-${segment}`,
    org_id: s.org.id,
    tenant_id: 'tenant-x',
    kind: 'otlp',
    segment,
    status: 'active',
    gateway_mode: 'operator',
    gateway_name: 'edge',
    gateway_namespace: 'gateways',
    created_at: '2026-09-29T09:00:00Z',
    updated_at: '2026-09-29T09:00:00Z',
    ...extra,
  });
  api.seed({
    orgs: [s.org],
    tenantRoutes: [
      route('otlp-new', { apply_status: 'pending' }),
      route('otlp-ok', { apply_status: 'applied', applied_at: '2026-09-29T09:05:00Z' }),
      route('otlp-closed', {
        apply_status: 'refused',
        apply_message: 'Accepted=False (reason NotAllowedByListeners)',
      }),
      route('otlp-broken', { apply_status: 'error', apply_message: 'httproutes is forbidden' }),
    ],
  });
  await api.loginAs(reader);
  await page.goto('/tenant-routes');

  const panel = page.getByTestId('tenant-routes');
  await expect(panel.getByRole('columnheader', { name: 'In cluster' })).toBeVisible();
  await expect(page.getByTestId('route-apply-otlp-new')).toHaveText('pending');
  await expect(page.getByTestId('route-apply-otlp-ok')).toHaveText('applied');
  await expect(page.getByTestId('route-apply-otlp-ok').getByText('applied')).toHaveAttribute(
    'title',
    /Last verified/,
  );
  // A refusal or error shows its reason, not just a colour.
  await expect(page.getByTestId('route-apply-otlp-closed')).toContainText('refused');
  await expect(page.getByTestId('route-apply-otlp-closed')).toContainText('NotAllowedByListeners');
  await expect(page.getByTestId('route-apply-otlp-broken')).toContainText(
    'httproutes is forbidden',
  );
});

test('a reader connects an app to an active route: endpoint and snippets from the server', async ({
  page,
  api,
}) => {
  const s = basicScenario();
  const route = (segment: string, extra: Record<string, unknown>) => ({
    id: `tr-${segment}`,
    org_id: s.org.id,
    tenant_id: 'tenant-x',
    kind: 'otlp',
    segment,
    status: 'active',
    gateway_mode: 'operator',
    gateway_name: 'edge',
    gateway_namespace: 'gateways',
    created_at: '2026-09-29T09:00:00Z',
    updated_at: '2026-09-29T09:00:00Z',
    ...extra,
  });
  api.seed({
    orgs: [s.org],
    gatewayPublicBaseUrl: 'https://telemetry.example.com',
    tenantRoutes: [
      route('otlp-live', { apply_status: 'applied' }),
      route('otlp-old', { status: 'revoked', apply_status: 'removed' }),
      route('faro-live', { kind: 'faro', apply_status: 'not_applicable' }),
    ],
  });
  await api.loginAs(reader);
  await page.goto('/tenant-routes');

  // Only the active OTLP route offers it: a revoked endpoint 404s, and Faro
  // has no receiver.
  await expect(page.getByTestId('route-connect-otlp-old')).toHaveCount(0);
  await expect(page.getByTestId('route-connect-faro-live')).toHaveCount(0);

  await page.getByTestId('route-connect-otlp-live').click();
  const dialog = page.getByTestId('connect-app');
  await expect(dialog.getByTestId('connect-app-not-applied')).toHaveCount(0);
  await dialog.getByTestId('connect-app-service').fill('checkout');
  await dialog.getByTestId('connect-app-render').click();

  // The configured URL is filled in, and the endpoint is the server's.
  await expect(dialog.getByTestId('connect-app-endpoint')).toHaveText(
    'https://telemetry.example.com/otlp/otlp-live',
  );
  await expect(dialog.getByTestId('connect-app-gateway')).toHaveValue(
    'https://telemetry.example.com',
  );
  await expect(dialog.getByTestId('connect-app-artifact')).toContainText(
    'OTEL_SERVICE_NAME=checkout',
  );
  await dialog.getByTestId('connect-app-tab-k8s').click();
  await expect(dialog.getByTestId('connect-app-artifact')).toContainText(
    'name: OTEL_EXPORTER_OTLP_ENDPOINT',
  );
  const calls = api.calls('/shepherd.mgmt.v1.TenantRouteService/RenderConnectApp');
  expect(calls.length).toBe(1);
  expect(calls[0].body).toMatchObject({ serviceName: 'checkout' });
  // Left empty, so the server's configured URL is the one used.
  expect(calls[0].body).not.toHaveProperty('gatewayBaseUrl');

  // A plaintext override is refused with the server's reason.
  await dialog.getByTestId('connect-app-gateway').fill('http://edge.example.org');
  await dialog.getByTestId('connect-app-render').click();
  await expect(dialog.getByTestId('connect-app-error')).toContainText('https');
});

test('connect-an-app warns when the route is not applied, and explains a missing gateway URL', async ({
  page,
  api,
}) => {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    tenantRoutes: [
      {
        id: 'tr-new',
        org_id: s.org.id,
        tenant_id: 'tenant-x',
        kind: 'otlp',
        segment: 'otlp-new',
        status: 'active',
        apply_status: 'pending',
        gateway_mode: 'operator',
        gateway_name: 'edge',
        gateway_namespace: '',
        created_at: '2026-09-29T09:00:00Z',
        updated_at: '2026-09-29T09:00:00Z',
      },
    ],
  });
  await api.loginAs(reader);
  await page.goto('/tenant-routes');
  await page.getByTestId('route-connect-otlp-new').click();
  const dialog = page.getByTestId('connect-app');
  await expect(dialog.getByTestId('connect-app-not-applied')).toContainText('pending');
  await dialog.getByTestId('connect-app-service').fill('checkout');
  await dialog.getByTestId('connect-app-render').click();
  await expect(dialog.getByTestId('connect-app-error')).toContainText('gateway URL');
});

test('a revoked route reads "not routed", not "pending" (#205)', async ({ page, api }) => {
  const s = basicScenario();
  api.seed({
    orgs: [s.org],
    tenantRoutes: [
      {
        id: 'tr-gone',
        org_id: s.org.id,
        tenant_id: 'tenant-x',
        kind: 'otlp',
        segment: 'otlp-gone',
        status: 'revoked',
        apply_status: 'pending',
        valid_until: '2099-01-01T00:00:00Z',
        gateway_mode: 'operator',
        gateway_name: 'edge',
        gateway_namespace: '',
        created_at: '2026-09-29T09:00:00Z',
        updated_at: '2026-09-29T09:00:00Z',
      },
    ],
  });
  await api.loginAs(reader);
  await page.goto('/tenant-routes');
  await expect(page.getByTestId('route-apply-otlp-gone')).toHaveText('not routed');
  await expect(page.getByTestId('tenant-routes')).not.toContainText('2099');
});
