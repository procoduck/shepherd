import { describe, expect, it } from 'vitest';
import { schemaFixture } from '../../tests/fixtures/schema-fixture';
import { portsCompatible, sanitizeLabel, validateGraph } from './l1';
import type { GraphDocument, GraphEdge, GraphNode } from './types';

const doc = (nodes: GraphNode[] = [], edges: GraphEdge[] = []): GraphDocument => ({
  kind: 'alloy-graph/v1',
  schema_version: 'x',
  nodes,
  edges,
  bindings: [],
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'test' },
});
const node = (
  id: string,
  component: string,
  label = id,
  extra: Partial<GraphNode> = {},
): GraphNode => ({
  id,
  component,
  label,
  position: { x: 0, y: 0 },
  props: {},
  disabled: false,
  notes: '',
  ...extra,
});
describe('L1 rule table', () => {
  it('sanitizes labels', () => expect(sanitizeLabel('9 My-Node')).toBe('_9_my_node'));
  it('label collision — same component and sanitized label', () =>
    expect(
      validateGraph(
        doc([node('a', 'discovery.kubernetes', 'A-B'), node('b', 'discovery.kubernetes', 'a_b')]),
        schemaFixture,
      ).some((d) => d.code === 'label_collision'),
    ).toBe(true));
  it('experimental gated', () =>
    expect(
      validateGraph(doc([node('a', 'experimental.component')]), schemaFixture).some(
        (d) => d.code === 'experimental_gated',
      ),
    ).toBe(true));
  it('secret by value', () =>
    expect(
      validateGraph(
        doc([
          node('a', 'prometheus.remote_write', 'a', { props: { password: 'literal' } }),
          node('d', 'prometheus.remote_write'),
        ]),
        schemaFixture,
      ).some((d) => d.code === 'secret_by_value'),
    ).toBe(true));
  it('cycle — 3-node chain', () =>
    expect(
      validateGraph(
        doc(
          [
            node('a', 'discovery.kubernetes'),
            node('b', 'discovery.relabel'),
            node('c', 'discovery.relabel'),
          ],
          [
            { id: '1', from: { node: 'a', port: 'targets' }, to: { node: 'b', port: 'targets' } },
            { id: '2', from: { node: 'b', port: 'output' }, to: { node: 'c', port: 'targets' } },
            { id: '3', from: { node: 'c', port: 'output' }, to: { node: 'a', port: 'x' } },
          ],
        ),
        schemaFixture,
      ).some((d) => d.code === 'cycle'),
    ).toBe(true));
  it('cycle — 2-node self-reference', () =>
    expect(
      validateGraph(
        doc(
          [node('a', 'discovery.relabel'), node('b', 'discovery.relabel')],
          [
            { id: '1', from: { node: 'a', port: 'output' }, to: { node: 'b', port: 'targets' } },
            { id: '2', from: { node: 'b', port: 'output' }, to: { node: 'a', port: 'targets' } },
          ],
        ),
        schemaFixture,
      ).some((d) => d.code === 'cycle'),
    ).toBe(true));
  it('dangling required input', () =>
    expect(
      validateGraph(doc([node('a', 'discovery.relabel')]), schemaFixture).some(
        (d) => d.code === 'dangling_input',
      ),
    ).toBe(true));
  it('required_attr_missing', () =>
    expect(
      validateGraph(doc([node('a', 'discovery.kubernetes')]), schemaFixture).some(
        (d) => d.code === 'required_attr_missing',
      ),
    ).toBe(true));
  it('type_mismatch', () =>
    expect(
      validateGraph(
        doc(
          [
            node('a', 'discovery.kubernetes', 'a', { props: { role: 'pod' } }),
            node('b', 'prometheus.remote_write'),
          ],
          [{ id: 'e', from: { node: 'a', port: 'targets' }, to: { node: 'b', port: 'receiver' } }],
        ),
        schemaFixture,
      ).some((d) => d.code === 'type_mismatch'),
    ).toBe(true));
  it('scalar_input_multi_wire', () =>
    expect(
      validateGraph(
        doc(
          [
            node('a', 'discovery.kubernetes', 'a', { props: { role: 'pod' } }),
            node('b', 'discovery.kubernetes', 'b', { props: { role: 'pod' } }),
            node('c', 'test.scalar'),
          ],
          [
            { id: 'e1', from: { node: 'a', port: 'targets' }, to: { node: 'c', port: 'targets' } },
            { id: 'e2', from: { node: 'b', port: 'targets' }, to: { node: 'c', port: 'targets' } },
          ],
        ),
        {
          ...schemaFixture,
          components: {
            ...schemaFixture.components,
            'test.scalar': {
              stability: 'ga',
              doc: '',
              attributes: [],
              blocks: [],
              inputs: [{ prop: 'targets', type: 'targets' }],
              outputs: [],
              default_snippet: '',
            },
          },
        },
      ).some((d) => d.code === 'scalar_input_multi_wire'),
    ).toBe(true));
  it('disabled intermediate nodes do not satisfy downstream inputs', () =>
    expect(
      validateGraph(
        doc(
          [
            node('a', 'discovery.kubernetes', 'a', { props: { role: 'pod' } }),
            node('b', 'discovery.relabel', 'b', { disabled: true }),
            node('c', 'prometheus.scrape'),
          ],
          [
            { id: 'e1', from: { node: 'a', port: 'targets' }, to: { node: 'b', port: 'targets' } },
            { id: 'e2', from: { node: 'b', port: 'output' }, to: { node: 'c', port: 'targets' } },
          ],
        ),
        schemaFixture,
      ).some((d) => d.code === 'dangling_input' && d.node_id === 'c'),
    ).toBe(true));
  it('output with zero wires', () =>
    expect(
      validateGraph(doc([node('a', 'discovery.kubernetes')]), schemaFixture).some(
        (d) => d.code === 'output_nowhere',
      ),
    ).toBe(true));
  it('no destination', () =>
    expect(
      validateGraph(doc([node('a', 'discovery.kubernetes')]), schemaFixture).some(
        (d) => d.code === 'no_destination',
      ),
    ).toBe(true));
  it('disabled destinations do not count', () =>
    expect(
      validateGraph(
        doc([node('a', 'prometheus.remote_write', 'a', { disabled: true })]),
        schemaFixture,
      ).some((d) => d.code === 'no_destination'),
    ).toBe(true));
  it('otel.any matches otel subtypes only', () => {
    expect(portsCompatible('otel.any', 'otel.logs')).toBe(true);
    expect(portsCompatible('otel.any', 'targets')).toBe(false);
  });
  it('A1 regression — an unnamed port resolves to its positional handle id, not "undefined"', () => {
    // Neither port below has a `prop`/`export` name, so PipelineNode renders the
    // handle as `p0` (portHandleId's fallback) — validateGraph must resolve the
    // same wire by the same rule, or a type-compatible edge to it is silently
    // treated as a dangling input / type mismatch (R1-H1).
    const schemaWithUnnamedPorts = {
      ...schemaFixture,
      components: {
        ...schemaFixture.components,
        'test.unnamed_source': {
          stability: 'ga' as const,
          doc: '',
          attributes: [],
          blocks: [],
          inputs: [],
          outputs: [{ type: 'targets' as const }],
          default_snippet: '',
        },
        'test.unnamed_sink': {
          stability: 'ga' as const,
          doc: '',
          terminal_ok: true,
          attributes: [],
          blocks: [],
          inputs: [{ type: 'targets' as const }],
          outputs: [],
          default_snippet: '',
        },
      },
    };
    const diags = validateGraph(
      doc(
        [node('a', 'test.unnamed_source'), node('b', 'test.unnamed_sink')],
        [{ id: 'e', from: { node: 'a', port: 'p0' }, to: { node: 'b', port: 'p0' } }],
      ),
      schemaWithUnnamedPorts,
    );
    expect(diags.some((d) => d.code === 'type_mismatch')).toBe(false);
    expect(diags.some((d) => d.code === 'dangling_input')).toBe(false);
    expect(diags.some((d) => d.code === 'output_nowhere')).toBe(false);
  });
});
