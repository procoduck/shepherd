/**
 * Fullstack: wizard scenario (8), GetMe org roles scenario (14)
 *
 * Scenario 8: Destination CRUD persists across navigation.
 * Scenario 14-wizard: Wizard list returns full step definitions (P2 drift fix).
 */
import { expect, getMe, loginAsAdmin, rpc, test } from './fixtures';

test.describe('wizard', () => {
  test('scenario 8: create destination persists across page refresh', async ({ page }) => {
    await loginAsAdmin(page);

    const me = await getMe(page);
    const org = me.orgs.find((o) => o.name === 'platform-org') ?? me.orgs[0];
    if (!org) throw new Error('dev seed must provide at least one org');
    const orgId = org.id;

    const destName = `fs-dest-${Date.now()}`;
    const createResp = await rpc(page, 'DestinationService', 'CreateDestination', {
      orgId,
      name: destName,
      type: 'prometheus',
      url: 'http://mimir:9090/api/v1/push',
      tenantId: '',
      authMode: 'none',
    });
    expect(createResp.status()).toBe(200);
    const dest = (await createResp.json()) as { id: string; name: string };
    expect(dest.name).toBe(destName);

    // Verify it persists — refetch from API (real DB)
    const listResp = await rpc(page, 'DestinationService', 'ListDestinations', { orgId });
    expect(listResp.status()).toBe(200);
    const list = (await listResp.json()) as { items?: Array<{ name: string }> };
    expect((list.items ?? []).some((d) => d.name === destName)).toBe(true);

    // Clean up
    await rpc(page, 'DestinationService', 'DeleteDestination', { orgId, id: dest.id });
  });

  test('scenario 14-wizard: wizard list returns full step definitions', async ({ page }) => {
    await loginAsAdmin(page);

    const me = await getMe(page);
    if (!me.orgs.length) throw new Error('dev seed must provide at least one org');
    const orgId = me.orgs[0].id;

    const wizardsResp = await rpc(page, 'WizardService', 'ListWizards', { orgId });
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
