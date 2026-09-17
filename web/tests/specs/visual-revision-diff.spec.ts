// visual-revision-diff.spec.ts — #118: the visual builder's graph revision
// diff. The Toolbar's History button opens RevisionCompare, which lists the
// saved pipeline's revisions and renders the server-computed structural diff
// (nodes / wires / bindings) of a picked revision against the current version.
import { expect } from '@playwright/test';
import { basicScenario, pipeline } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { schemaFixture } from '../fixtures/schema-fixture';
import { test } from '../fixtures/test';

const mockGraph = {
  kind: 'alloy-graph/v1',
  schema_version: 'alloy-v1.18.1',
  nodes: [],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'shepherd-parser' },
};

const visualPipe = pipeline({
  id: 'pip-visual-diff',
  name: 'existing-visual',
  source: 'visual',
  matchers: ['cluster="prod-eu-1"'],
  revisions: [
    {
      revision: 2,
      changed_by: 'ada@example.com',
      changed_at: '2026-09-16T10:00:00Z',
      change_note: 'add loki',
    },
    {
      revision: 1,
      changed_by: 'grace@example.com',
      changed_at: '2026-09-15T10:00:00Z',
      change_note: 'created',
    },
  ],
});

const richDiff = {
  diff: {
    node_changes: [
      {
        kind: 'changed',
        id: 'n1',
        component: 'prometheus.scrape',
        label: 'web',
        field_changes: [{ field: 'prop:scrape_interval', old_value: '15s', new_value: '30s' }],
      },
      { kind: 'added', id: 'n2', component: 'loki.write', label: 'central', field_changes: [] },
    ],
    edge_changes: [
      {
        kind: 'added',
        id: 'e1',
        from: { node: 'n1', port: 'forward_to' },
        to: { node: 'n2', port: 'receiver' },
      },
    ],
    binding_changes: [
      {
        kind: 'changed',
        node: 'n1',
        prop: 'forward_to',
        old_ref: { node: 'local', export: 'receiver', expr: '' },
        new_ref: { node: 'central', export: 'receiver', expr: '' },
      },
    ],
  },
  from_opaque: false,
  to_opaque: false,
  warning: '',
};

test.describe('visual builder — graph revision diff (#118)', () => {
  test.beforeEach(async ({ page, api }) => {
    await api.loginAs(appAdmin);
    const s = basicScenario();
    api.seed({
      orgs: [s.org],
      schema: schemaFixture,
      pipelines: [visualPipe],
      graphViewResult: { graph: mockGraph, opaque: false, warning: '' },
      visualRenderResult: { content: '// generated\n', node_map: {}, diagnostics: [] },
    });
    await page.goto(`/pipelines/${visualPipe.id}/visual`);
    await page.waitForSelector('[data-testid="visual-builder"]', { timeout: 10_000 });
  });

  test('History opens the compare modal and renders a structural graph diff', async ({
    page,
    api,
  }) => {
    api.seed({ graphDiffResult: richDiff });

    await page.click('[data-testid="toolbar-history"]');
    const modal = page.getByTestId('revision-compare');
    await expect(modal).toBeVisible();

    // Pick revision #2 — diff it against the current version.
    await page.click('[data-testid="compare-revision-2"]');
    const diff = page.getByTestId('compare-diff');
    await expect(diff).toBeVisible();

    // Changed node with a per-prop before/after.
    await expect(diff).toContainText('prometheus.scrape "web"');
    await expect(diff).toContainText('prop:scrape_interval');
    await expect(diff).toContainText('15s');
    await expect(diff).toContainText('30s');
    // Added node.
    await expect(diff).toContainText('loki.write "central"');
    await expect(diff.getByTestId('change-kind-added').first()).toBeVisible();
    // Added wire, rendered node.port → node.port.
    await expect(diff).toContainText('n1.forward_to → n2.receiver');
    // Changed binding, old → new ref.
    await expect(diff).toContainText('n1.forward_to');
    await expect(diff).toContainText('local.receiver');
    await expect(diff).toContainText('central.receiver');

    const calls = api.calls('VisualService/DiffRevisions');
    expect(calls).toHaveLength(1);
    const body = calls[0].body as Record<string, unknown>;
    expect(body.id).toBe(visualPipe.id);
    expect(body.fromRevision).toBe(2);
    // toRevision 0 means "current"; connect-es omits a zero int from the JSON body.
    expect(body.toRevision ?? 0).toBe(0);
  });

  test('reports when a revision is identical to the current graph', async ({ page }) => {
    // No graphDiffResult seeded → the mock returns an empty diff.
    await page.click('[data-testid="toolbar-history"]');
    await page.click('[data-testid="compare-revision-1"]');
    await expect(page.getByTestId('compare-no-changes')).toBeVisible();
  });
});
