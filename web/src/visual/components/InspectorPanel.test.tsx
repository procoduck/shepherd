// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { UpgradeCheckResult } from '@/api/client';
import { useVisualStore } from '../store';
import type { GraphDocument, SchemaPayload } from '../types';
import { InspectorPanel } from './InspectorPanel';

// UpgradeReview (rendered by InspectorPanel when no node is selected and an
// upgrade banner is showing) reads the selected org via useOrgId (F3),
// which itself calls useOrg() -> useQueryClient() -- stub useOrgId directly
// so the test doesn't need a QueryClientProvider or a real network
// round-trip. (useMe is mocked too since other InspectorPanel paths may
// still read it; harmless to keep alongside the useOrgId mock.)
vi.mock('@/hooks/useMe', () => ({
  useMe: () => ({ data: { orgs: [{ id: 'org-1' }] } }),
}));
vi.mock('@/hooks/useOrg', () => ({
  useOrgId: () => 'org-1',
}));

// upgradeCheck is UpgradeReview's only network call; stub it so Accept has a
// deterministic result to prune against. stampSchemaVersion/pruneRemovedAttrs
// stay real (imported from the real modules) -- this test exists precisely to
// prove the DOCUMENT that ends up in the store after Accept is the one those
// real functions produced, not one InspectorPanel re-derives itself.
const upgradeCheckMock = vi.fn<() => Promise<UpgradeCheckResult>>();
vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>();
  return { ...actual, upgradeCheck: () => upgradeCheckMock() };
});

const baseDoc: GraphDocument = {
  kind: 'alloy-graph/v1',
  schema_version: 'alloy-v1.12.0',
  nodes: [
    {
      id: 'n1',
      component: 'test.component',
      label: 'n1',
      position: { x: 0, y: 0 },
      props: { keep: 'yes', gone: 'should-be-pruned' },
      disabled: false,
      notes: '',
    },
  ],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'test' },
};

const schema: SchemaPayload = {
  _meta: { alloy_version: '1.18.1', components_total: 1 },
  components: {
    'test.component': {
      stability: 'ga',
      doc: '',
      attributes: [],
      blocks: [],
      inputs: [],
      outputs: [],
      default_snippet: '',
    },
  },
  wire_types: {},
};

describe('InspectorPanel upgrade accept handler', () => {
  beforeEach(() => {
    useVisualStore.setState({
      doc: structuredClone(baseDoc),
      selected: [],
      diagnostics: [],
      schema,
      allowExperimental: false,
      connectingFrom: null,
    });
    useVisualStore.temporal.getState().clear();
    upgradeCheckMock.mockReset();
    upgradeCheckMock.mockResolvedValue({
      old_version: 'alloy-v1.12.0',
      new_version: 'alloy-v1.18.1',
      needs_upgrade: true,
      items: [
        {
          node_id: 'n1',
          node_label: 'n1',
          component: 'test.component',
          class: 'attr_removed',
          detail: 'gone',
        },
      ],
    });
  });

  it('accepting the upgrade stores the pruned+stamped document, not the pre-prune one', async () => {
    render(<InspectorPanel />);

    fireEvent.click(screen.getByTestId('upgrade-review-open'));
    await waitFor(() => {
      const btn = screen.getByTestId('upgrade-accept') as HTMLButtonElement;
      expect(btn.disabled).toBe(false);
    });

    fireEvent.click(screen.getByTestId('upgrade-accept'));

    // The review closes...
    await waitFor(() => expect(screen.queryByTestId('upgrade-review')).toBeNull());

    // ...and the store holds the version UpgradeReview computed, with the
    // removed attribute actually gone from the node's props. A handler that
    // re-imports `useVisualStore.getState().doc` verbatim (merely patching
    // schema_version) happens to reach the same state ONLY because that read
    // follows UpgradeReview's own import; asserting both the stamp and the
    // prune together is what would catch a handler that reintroduces the
    // pre-prune document (e.g. one racing ahead of UpgradeReview's import, or
    // reading a stale snapshot captured before it).
    const finalDoc = useVisualStore.getState().doc;
    expect(finalDoc.schema_version).toBe('alloy-v1.18.1');
    expect(finalDoc.nodes[0]?.props).toEqual({ keep: 'yes' });
    expect(finalDoc.nodes[0]?.props).not.toHaveProperty('gone');
  });
});
