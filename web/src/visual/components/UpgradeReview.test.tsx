// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { UpgradeCheckResult } from '../../api/client';
import { useVisualStore } from '../store';
import { UpgradeReview } from './UpgradeReview';

// W7-06: UpgradeReview calls upgradeCheck (a Connect RPC, via api/client.ts)
// and useOrgId (F3: the selected org, not useMe directly) directly. Mocking
// both here — rather than wrapping the tree in a QueryClientProvider and a
// transport mock — keeps this a plain render test, same reasoning the plan
// draft gives.
const upgradeCheckMock = vi.fn<(orgId: string, doc: unknown) => Promise<UpgradeCheckResult>>();

vi.mock('../../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/client')>();
  return {
    ...actual,
    upgradeCheck: (orgId: string, doc: unknown) => upgradeCheckMock(orgId, doc),
  };
});

// A stable return value matters here, not just its shape: UpgradeReview's
// effect depends on `orgId` directly (see its own comment on why —
// re-checking only when schema_version/orgId/open change, not on every doc
// mutation); a mock that returned a fresh value on every call would still be
// `'org-1' === 'org-1'` (a primitive), so this stays safe without needing
// the reference-stability care the old useMe mock's comment called out.
vi.mock('../../hooks/useOrg', () => ({
  useOrgId: () => 'org-1',
}));

afterEach(() => {
  cleanup();
  upgradeCheckMock.mockReset();
});

// Minimal alloy-graph/v1 doc, one node carrying a prop an `attr_removed`
// item will name and a second prop that must survive the prune.
const baseDoc = {
  kind: 'alloy-graph/v1' as const,
  schema_version: 'alloy-v1.17.0',
  nodes: [
    {
      id: 'n1',
      component: 'test.component',
      label: 'my-scrape',
      position: { x: 0, y: 0 },
      props: { removed_attr: 'x', kept_attr: 'y' },
      disabled: false,
      notes: '',
    },
  ],
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'test' },
};

function seedStore() {
  useVisualStore.setState({
    doc: structuredClone(baseDoc),
    selected: [],
    diagnostics: [],
    schema: null,
    allowExperimental: false,
    connectingFrom: null,
  });
  useVisualStore.temporal.getState().clear();
}

const upgradeResult = (items: UpgradeCheckResult['items']): UpgradeCheckResult => ({
  old_version: 'alloy-v1.17.0',
  new_version: 'alloy-v1.18.0',
  needs_upgrade: true,
  items,
});

describe('UpgradeReview', () => {
  it('renders each upgrade item, tagged by node label and diff class', async () => {
    seedStore();
    upgradeCheckMock.mockResolvedValue(
      upgradeResult([
        {
          node_id: 'n1',
          node_label: 'my-scrape',
          component: 'test.component',
          class: 'enum_value_removed',
          detail: 'foo',
        },
      ]),
    );
    render(<UpgradeReview open onClose={vi.fn()} onAccept={vi.fn()} />);

    await waitFor(() => expect(screen.getByTestId('upgrade-item-enum_value_removed')).toBeTruthy());
    expect(screen.getByTestId('upgrade-item-node').textContent).toBe('my-scrape');
    expect(screen.getByTestId('upgrade-item-detail').textContent).toContain(
      'Enum value removed: foo',
    );
  });

  it('shows the attr_removed drop-on-save notice', async () => {
    seedStore();
    upgradeCheckMock.mockResolvedValue(
      upgradeResult([
        {
          node_id: 'n1',
          node_label: 'my-scrape',
          component: 'test.component',
          class: 'attr_removed',
          detail: 'removed_attr',
        },
      ]),
    );
    render(<UpgradeReview open onClose={vi.fn()} onAccept={vi.fn()} />);

    await waitFor(() => expect(screen.getByTestId('upgrade-discard-attr')).toBeTruthy());
    expect(screen.getByTestId('upgrade-discard-attr').textContent).toContain('removed_attr');
    expect(screen.getByTestId('upgrade-discard-attr').textContent).toContain(
      'will be dropped on save',
    );
  });

  it('Accept prunes the attr_removed prop and stamps the new schema version', async () => {
    seedStore();
    upgradeCheckMock.mockResolvedValue(
      upgradeResult([
        {
          node_id: 'n1',
          node_label: 'my-scrape',
          component: 'test.component',
          class: 'attr_removed',
          detail: 'removed_attr',
        },
      ]),
    );
    const onAccept = vi.fn();
    render(<UpgradeReview open onClose={vi.fn()} onAccept={onAccept} />);

    await waitFor(() => expect(screen.getByTestId('upgrade-accept')).toBeTruthy());
    fireEvent.click(screen.getByTestId('upgrade-accept'));

    expect(onAccept).toHaveBeenCalledTimes(1);
    expect(onAccept).toHaveBeenCalledWith('alloy-v1.18.0');
    const doc = useVisualStore.getState().doc;
    expect(doc.schema_version).toBe('alloy-v1.18.0');
    expect(doc.nodes[0].props).toEqual({ kept_attr: 'y' });

    // Accept stamped a new schema_version onto the doc this component is
    // still subscribed to (the test never flips `open` to false, unlike a
    // real caller) — that re-triggers the same effect that ran on mount, per
    // its own dependency comment. Flush it so nothing resolves after this
    // test's act() scope closes.
    await waitFor(() => expect(upgradeCheckMock).toHaveBeenCalledTimes(2));
  });

  it('disables Accept while a component_removed item is present, and clicking it does nothing', async () => {
    seedStore();
    upgradeCheckMock.mockResolvedValue(
      upgradeResult([
        {
          node_id: 'n1',
          node_label: 'my-scrape',
          component: 'test.component',
          class: 'component_removed',
          detail: '',
        },
      ]),
    );
    const onAccept = vi.fn();
    render(<UpgradeReview open onClose={vi.fn()} onAccept={onAccept} />);

    await waitFor(() => expect(screen.getByTestId('upgrade-blocked')).toBeTruthy());
    const accept = screen.getByTestId('upgrade-accept') as HTMLButtonElement;
    expect(accept.disabled).toBe(true);

    fireEvent.click(accept);
    expect(onAccept).not.toHaveBeenCalled();
    // The doc must be untouched too — a disabled Accept must not have
    // pruned or stamped anything on the side.
    expect(useVisualStore.getState().doc.schema_version).toBe('alloy-v1.17.0');
  });
});
