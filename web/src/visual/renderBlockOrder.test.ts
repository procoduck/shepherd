/**
 * Block order in the TS renderer — the mirror of
 * internal/visual/render_blockorder_test.go.
 *
 * Alloy block order is semantic. loki.process runs stages in document order, so
 * `stage.json` before `stage.drop` extracts a field and then drops on it, while
 * the reverse drops against an empty extracted map and silently matches nothing.
 * The config is valid either way and still runs — which is exactly why nothing
 * caught it.
 *
 * `props` is an object keyed by block name, so before `block_order` existed the
 * authored sequence was never stored at all. The shipped schema declares
 * `stage.drop` at index 3 and `stage.json` at 6, so parse-then-drop rendered
 * inverted.
 *
 * These specs exist in both renderers because the two are drift-checked against
 * the shared corpus: if only one honoured order, the goldens would disagree.
 */
import { describe, expect, it } from 'vitest';
import { shippedSchema } from '../../tests/fixtures/schema-fixture';
import { renderTS } from './renderTS';
import type { GraphDocument } from './types';

const doc = (blockOrder?: string[]): GraphDocument =>
  ({
    kind: 'alloy-graph/v1',
    nodes: [
      {
        id: 'n1',
        component: 'loki.process',
        label: 'proc',
        position: { x: 0, y: 0 },
        disabled: false,
        notes: '',
        props: {
          'stage.json': { expressions: { level: 'level' } },
          'stage.drop': { source: 'level', value: 'debug' },
        },
        ...(blockOrder ? { block_order: blockOrder } : {}),
      },
    ],
    edges: [],
    bindings: [],
    schema_version: shippedSchema._meta?.alloy_version ?? '',
    viewport: { x: 0, y: 0, zoom: 1 },
    meta: { created_with: 'test' },
  }) as unknown as GraphDocument;

const order = (content: string) => ({
  json: content.indexOf('stage.json'),
  drop: content.indexOf('stage.drop'),
});

describe('renderTS: loki.process stage order', () => {
  it("emits the author's order when block_order records it", () => {
    const { json, drop } = order(
      renderTS(doc(['stage.json', 'stage.drop']), shippedSchema).content,
    );
    expect(json).toBeGreaterThanOrEqual(0);
    expect(drop).toBeGreaterThanOrEqual(0);
    expect(json).toBeLessThan(drop);
  });

  it('emits the reverse when the author reversed it', () => {
    // Without this, a renderer that happened to hardcode json-before-drop would
    // pass the spec above while still ignoring the author entirely.
    const { json, drop } = order(
      renderTS(doc(['stage.drop', 'stage.json']), shippedSchema).content,
    );
    expect(drop).toBeLessThan(json);
  });

  it('falls back to schema order when block_order is absent', () => {
    // Guarantees graphs saved before the field existed render unchanged rather
    // than shifting under their authors.
    const { json, drop } = order(renderTS(doc(), shippedSchema).content);
    expect(drop).toBeLessThan(json);
  });

  it('ignores undeclared names and still renders every block present', () => {
    const content = renderTS(
      doc(['stage.nonexistent', 'stage.json', 'stage.drop']),
      shippedSchema,
    ).content;
    expect(content).toContain('stage.json');
    expect(content).toContain('stage.drop');
    expect(content).not.toContain('stage.nonexistent');
    const { json, drop } = order(content);
    expect(json).toBeLessThan(drop);
  });

  it('renders blocks omitted from block_order after the ones it names', () => {
    const content = renderTS(doc(['stage.drop']), shippedSchema).content;
    expect(content).toContain('stage.json');
    const { json, drop } = order(content);
    expect(drop).toBeLessThan(json);
  });
});
