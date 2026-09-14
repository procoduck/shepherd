// visual-palette-search.spec.ts — F12: the palette used to filter with
// `includes()` and keep schema object order, so a fuzzy match (a component
// whose DOC text merely mentions the query) could out-rank the component
// whose NAME the query names exactly. See paletteSearch.test.ts for the
// pure-function unit coverage this spec proves end to end.
import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture, shippedSchema } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

// schemaFixture's curated component set has no natural fuzzy-vs-exact
// collision for "remote_write" (only prometheus.remote_write's own name
// contains it) — add the real shipped `prometheus.receive_http`, whose doc
// ("Receives Prometheus metrics via HTTP remote_write.") matches the query
// only by substring, never by name. Sliced from the same artifact
// schema-fixture.ts derives from — not hand-authored.
const receiveHttp = shippedSchema.components['prometheus.receive_http'];
if (!receiveHttp) throw new Error('shipped schema declares no prometheus.receive_http');
const schemaWithReceiveHttp = {
  ...schemaFixture,
  components: { ...schemaFixture.components, 'prometheus.receive_http': receiveHttp },
};

test.describe('visual palette search', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(orgEditor);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaWithReceiveHttp });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 8_000 });
  });

  test('an exact name match ranks above a fuzzy doc-only match', async ({ page }) => {
    await page.locator('[data-testid="palette-search"]').fill('remote_write');

    const items = page.locator('[data-testid^="palette-item-"]');
    await expect(items.first()).toHaveAttribute(
      'data-testid',
      'palette-item-prometheus.remote_write',
    );
    // Both still show up — this is a rank, not a filter, regression.
    await expect(
      page.locator('[data-testid="palette-item-prometheus.receive_http"]'),
    ).toBeVisible();
  });
});
