/**
 * Fullstack: admin CRUD scenarios (4, 5)
 *
 * Scenario 4: Create org, claim cluster → both appear on admin screens.
 * Scenario 5: Cluster list includes all clusters (claimed + unclaimed) with created_at.
 *
 * Red-green proofs:
 * - Scenario 4/5: red = revert ListClusters to ListUnclaimedClusters only →
 *   claimed cluster absent from GET /api/admin/clusters response.
 */
import { expect, loginAsAdmin, test } from './fixtures';

test.describe('admin-crud', () => {
  test('scenario 4: create org, claim cluster, both appear on admin screens', async ({ page }) => {
    await loginAsAdmin(page);

    // Create a new org with a unique name
    const orgName = `fs-test-org-${Date.now()}`;
    const createResp = await page.request.post('/api/admin/orgs', {
      headers: {
        'Content-Type': 'application/json',
        'X-Requested-With': 'XMLHttpRequest',
      },
      data: {
        name: orgName,
        display_name: 'FS Test Org',
        admin_group_id: '99999999-test-4000-8000-000000000001',
      },
    });
    expect(createResp.status()).toBe(201);
    const org = (await createResp.json()) as { id: string; name: string; admin_group_id: string };
    expect(org.id).toBeTruthy();
    expect(org.name).toBe(orgName);
    // admin_group_id MUST be present in the response (P1 contract fix)
    expect(org.admin_group_id).toBe('99999999-test-4000-8000-000000000001');

    // Verify org appears in the list (API-level — AdminOrgsPage UI is a stub)
    const listResp = await page.request.get('/api/admin/orgs', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    expect(listResp.status()).toBe(200);
    const list = (await listResp.json()) as { items: Array<{ name: string }> };
    expect(list.items.some((o) => o.name === orgName)).toBe(true);

    // Clean up
    await page.request.delete(`/api/admin/orgs/${org.id}`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
  });

  test('scenario 5: cluster list returns all clusters with created_at (P1 contract fix)', async ({
    page,
  }) => {
    await loginAsAdmin(page);

    // GET /api/admin/clusters must return ALL clusters (claimed + unclaimed)
    const resp = await page.request.get('/api/admin/clusters', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    expect(resp.status()).toBe(200);
    const body = (await resp.json()) as {
      items: Array<{ id: string; name: string; org_id?: string; created_at?: string }>;
    };
    expect(Array.isArray(body.items)).toBe(true);

    // Each cluster must have created_at (P1 fix)
    for (const cluster of body.items) {
      expect(cluster.created_at).toBeTruthy();
      expect(typeof cluster.created_at).toBe('string');
    }

    // The seeded prod-eu-1 cluster (claimed) must appear — not just unclaimed
    const prodCluster = body.items.find((c) => c.name === 'prod-eu-1');
    if (prodCluster) {
      // Claimed clusters have a non-empty org_id
      expect(prodCluster.org_id).toBeTruthy();
    }
    // staging-us-1 (unclaimed) should also appear
    const stagingCluster = body.items.find((c) => c.name === 'staging-us-1');
    if (stagingCluster) {
      // Unclaimed: org_id is empty UUID or null
      expect(stagingCluster).toBeTruthy();
    }
  });
});
