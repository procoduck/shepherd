// visual-experimental-gate.spec.ts — #114: the visual builder's palette offers
// experimental components only when the current org has opted in. The store flag
// is wired from the org's `allowExperimentalComponents` (from GetMe), replacing
// the previously hardcoded `false`.
import { expect } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { orgEditor } from '../fixtures/personas';
import { schemaFixture, shippedSchema } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

// An experimental component pulled from the shipped schema and added to the
// curated fixture — picked dynamically so an Alloy bump that promotes one
// component (loki.secretfilter went experimental -> public-preview) doesn't
// silently defang this spec.
const experimentalName = Object.keys(shippedSchema.components).find(
  (n) => shippedSchema.components[n].stability === 'experimental',
);
if (!experimentalName) throw new Error('shipped schema declares no experimental component');
const paletteItem = `palette-item-${experimentalName}`;
const schemaWithExperimental = {
  ...schemaFixture,
  components: {
    ...schemaFixture.components,
    [experimentalName]: shippedSchema.components[experimentalName],
  },
};

// orgEditor with the org opted into experimental components.
const experimentalOrgEditor = {
  ...orgEditor,
  orgs: [{ ...orgEditor.orgs[0], allowExperimentalComponents: true }],
};

test.describe('visual builder — experimental component gate (#114)', () => {
  test('the palette hides experimental components for an org that has not opted in', async ({
    page,
    api,
  }) => {
    await api.loginAs(orgEditor); // default org: allowExperimentalComponents = false
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaWithExperimental });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });

    await page.locator('[data-testid="palette-search"]').fill(experimentalName);
    await expect(page.locator(`[data-testid="${paletteItem}"]`)).toHaveCount(0);
  });

  test('the palette offers experimental components once the org opts in', async ({ page, api }) => {
    await api.loginAs(experimentalOrgEditor);
    const s = basicScenario();
    api.seed({ orgs: [s.org], schema: schemaWithExperimental });
    await page.goto('/pipelines/visual/new');
    await page.waitForSelector('[data-testid="palette-search"]', { timeout: 10_000 });

    await page.locator('[data-testid="palette-search"]').fill(experimentalName);
    await expect(page.locator(`[data-testid="${paletteItem}"]`)).toBeVisible();
  });
});
