// visual-graph-fallback-load.spec.ts — GraphView responses turned back into
// builder drafts (mocked suite): the builder's fallback load and Recreate.
// A visual pipeline whose wizard_state is missing or unreadable is rebuilt
// from VisualService.GraphView, the same response the read-only graph view
// shows. That response's bindings are display-only (each is an
// expression the props already hold); imported, they rendered the attribute a
// second time. The cases are the server's real responses for builder-rendered
// text (tests/fixtures/graphview-roundtrip.json, kept in sync by
// internal/visual/parse_roundtrip_test.go).
import { readFileSync } from 'node:fs';
import { expect, type Page } from '@playwright/test';
import { basicScenario, pipeline } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { shippedSchema } from '../fixtures/schema-fixture';
import { type ApiFixture, test } from '../fixtures/test';

const cases = JSON.parse(
  readFileSync(new URL('../fixtures/graphview-roundtrip.json', import.meta.url), 'utf8'),
) as { name: string; text: string; graph: { bindings: unknown[] }; opaque: boolean }[];
const byName = (name: string) => {
  const c = cases.find((x) => x.name === name);
  if (!c) throw new Error(`no round-trip case ${name}`);
  return c;
};

async function openFallback(page: Page, api: ApiFixture, name: string) {
  const c = byName(name);
  await api.loginAs(appAdmin);
  const s = basicScenario();
  const visual = pipeline({ id: 'pip-fallback', name: 'fallback', source: 'visual' });
  api.seed({
    orgs: [s.org],
    schema: shippedSchema,
    pipelines: [visual],
    graphViewResult: { graph: c.graph, opaque: c.opaque, warning: '' },
  });
  await page.goto(`/pipelines/${visual.id}/visual`);
  await expect(page.getByTestId('visual-builder')).toBeVisible();
  await page.getByTestId('drawer-toggle').click();
  await page.getByTestId('drawer-tab-code').click();
  const code = page.getByTestId('code-tab-content');
  // The header carries the node count: the imported graph, not an empty one.
  await expect(code).toContainText(c.text.split('\n')[0]);
  return { c, code };
}

test('a fallback load renders each reference once, as it was written', async ({ page, api }) => {
  const { c, code } = await openFallback(page, api, 'bindings-secret');
  expect(c.graph.bindings.length).toBeGreaterThan(0);
  const text = (await code.textContent()) ?? '';
  expect(text.match(/bearer_token = /g)).toHaveLength(1);
  expect(text).not.toContain('endpoint.basic_auth.password =');
  expect(text).toContain('password = remote.kubernetes.secret.creds.data["password"]');
});

for (const name of ['buried-port-ref', 'mixed-list']) {
  test(`a reference inside a larger expression loads and renders unchanged: ${name}`, async ({
    page,
    api,
  }) => {
    const { c, code } = await openFallback(page, api, name);
    const text = (await code.textContent()) ?? '';
    for (const line of c.text.split('\n').filter((l) => l.includes(' = '))) {
      expect(text).toContain(line.trim());
    }
    await page.getByTestId('drawer-tab-problems').click();
    await expect(
      page.getByTestId('problem-row').filter({ hasText: 'prop_wire_conflict' }),
    ).toHaveCount(0);
  });
}

// "Recreate as visual pipeline" copies the same GraphView response into a new
// draft, so the same shapes must survive it: available (not opaque), no
// wire/value conflict, the expression rendered as written, each once.
for (const name of ['buried-port-ref', 'mixed-list', 'bindings-secret']) {
  test(`Recreate carries a reference inside an expression over unchanged: ${name}`, async ({
    page,
    api,
  }) => {
    const c = byName(name);
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: shippedSchema,
      pipelines: [s.pipelines[2]],
      graphViewResult: { graph: c.graph, opaque: c.opaque, warning: '' },
    });
    await page.goto(`/pipelines/${s.pipelines[2].id}/graph`);
    const recreate = page.getByRole('button', { name: 'Recreate as visual pipeline' });
    await expect(recreate).not.toHaveAttribute('aria-disabled', 'true');
    await recreate.click();
    await page.getByTestId('recreate-confirm-btn').click();
    await expect(page).toHaveURL(/\/pipelines\/visual\/new$/);
    await page.getByTestId('drawer-toggle').click();
    await page.getByTestId('drawer-tab-code').click();
    const code = page.getByTestId('code-tab-content');
    await expect(code).toContainText(c.text.split('\n')[0]);
    const text = (await code.textContent()) ?? '';
    for (const line of c.text.split('\n').filter((l) => l.includes(' = '))) {
      expect(text).toContain(line.trim());
    }
    const refs = (t: string) => t.match(/ = remote\.kubernetes\.secret/g)?.length ?? 0;
    expect(refs(text)).toBe(refs(c.text));
    await page.getByTestId('drawer-tab-problems').click();
    await expect(
      page.getByTestId('problem-row').filter({ hasText: 'prop_wire_conflict' }),
    ).toHaveCount(0);
  });
}
