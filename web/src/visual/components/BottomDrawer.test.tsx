// @vitest-environment jsdom
import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { shippedSchema } from '../../../tests/fixtures/schema-fixture';
import { useVisualStore } from '../store';
import type { GraphDocument, GraphNode } from '../types';
import { BottomDrawer } from './BottomDrawer';

// The drawer reads the org (Verify, the simulators) and the caller's write
// access; stub both so the test needs no QueryClientProvider.
vi.mock('@/hooks/useOrg', () => ({
  useOrgId: () => 'org-1',
  useCanWrite: () => true,
}));

const node = (id: string, component: string, props: GraphNode['props'] = {}): GraphNode => ({
  id,
  component,
  label: id,
  position: { x: 0, y: 0 },
  props,
  disabled: false,
  notes: '',
});

const docOf = (nodes: GraphNode[]): GraphDocument => ({
  kind: 'alloy-graph/v1',
  schema_version: 'v1.18.1',
  nodes,
  edges: [],
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'test' },
});

const openCode = (doc: GraphDocument) => {
  useVisualStore.setState({ doc, schema: shippedSchema, diagnostics: [], selected: [] });
  render(<BottomDrawer />);
  fireEvent.click(screen.getByTestId('drawer-tab-code'));
  return screen.getByTestId('code-tab-content');
};

// #400: past the preview's bounds the Code tab says so rather than showing a
// truncated or empty config.
describe('BottomDrawer generated config bounds', () => {
  beforeEach(() => {
    useVisualStore.temporal.getState().clear();
  });

  it('renders the config for an ordinary graph', () => {
    const tab = openCode(docOf([node('app', 'prometheus.scrape')]));
    expect(tab.textContent).toContain('prometheus.scrape "app" {');
    expect(screen.queryByTestId('code-too-large')).toBeNull();
  });

  it('shows "too large to render" when the output passes 4 MiB', () => {
    const tab = openCode(docOf([node('big', 'nonexistent.component', { s: 'a'.repeat(5 << 20) })]));
    expect(screen.getByTestId('code-too-large').textContent).toBe(
      'Too large to render: the generated config would pass 4 MiB.',
    );
    expect(tab.querySelector('pre')).toBeNull();
  });

  it('shows "too large to render" when the diagnostics pass their cap', () => {
    const tab = openCode(
      docOf(Array.from({ length: 1000 }, (_, i) => node(`n${i}`, 'nonexistent.component'))),
    );
    expect(screen.getByTestId('code-too-large').textContent).toBe(
      'Too large to render: the graph has more problems than the preview reports (800).',
    );
    expect(tab.textContent).not.toContain('nonexistent.component "n0"');
  });
});
