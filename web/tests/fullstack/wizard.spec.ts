/**
 * Fullstack: wizard scenario (8), /api/me org roles scenario (14)
 *
 * Scenario 8: Destination CRUD persists across navigation.
 * Scenario 14-wizard: Wizard list returns full step definitions (P2 drift fix).
 */
import { expect, loginAsAdmin, test } from './fixtures';

test.describe('wizard', () => {
  test('scenario 8: create destination persists across page refresh', async ({ page }) => {
    await loginAsAdmin(page);

    const meResp = await page.request.get('/api/me', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    const me = (await meResp.json()) as { orgs: Array<{ id: string; name: string }> };
    const org = me.orgs.find((o) => o.name === 'platform-org') ?? me.orgs[0];
    if (!org) throw new Error('dev seed must provide at least one org');
    const orgId = org.id;

    const destName = `fs-dest-${Date.now()}`;
    const createResp = await page.request.post(`/api/orgs/${orgId}/destinations`, {
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
      data: {
        name: destName,
        type: 'prometheus',
        url: 'http://mimir:9090/api/v1/push',
        tenant_id: '',
        auth_mode: 'none',
      },
    });
    expect(createResp.status()).toBe(201);
    const dest = (await createResp.json()) as { id: string; name: string };
    expect(dest.name).toBe(destName);

    // Verify it persists — refetch from API (real DB)
    const listResp = await page.request.get(`/api/orgs/${orgId}/destinations`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    expect(listResp.status()).toBe(200);
    const list = (await listResp.json()) as { items: Array<{ name: string }> };
    expect(list.items.some((d) => d.name === destName)).toBe(true);

    // Clean up
    await page.request.delete(`/api/orgs/${orgId}/destinations/${dest.id}`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
  });

  test('scenario 14-wizard: wizard list returns full step definitions', async ({ page }) => {
    await loginAsAdmin(page);

    const meResp = await page.request.get('/api/me', {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    const me = (await meResp.json()) as { orgs: Array<{ id: string }> };
    if (!me.orgs.length) throw new Error('dev seed must provide at least one org');
    const orgId = me.orgs[0].id;

    const wizardsResp = await page.request.get(`/api/orgs/${orgId}/wizards`, {
      headers: { 'X-Requested-With': 'XMLHttpRequest' },
    });
    expect(wizardsResp.status()).toBe(200);
    const wizards = (await wizardsResp.json()) as {
      items: Array<{ kind: string; steps?: unknown[] }>;
    };
    expect(Array.isArray(wizards.items)).toBe(true);
    if (wizards.items.length > 0) {
      const wizard = wizards.items[0];
      // Wizard must have full steps — not an empty array (P2 mock drift fix)
      expect(Array.isArray(wizard.steps)).toBe(true);
      expect(wizard.steps!.length).toBeGreaterThan(0);
    }
  });
});
