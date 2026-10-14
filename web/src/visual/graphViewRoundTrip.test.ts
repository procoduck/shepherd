/**
 * Builder text through VisualService.GraphView and back (the builder's
 * fallback load when a pipeline's wizard_state is missing, and "Recreate as
 * visual pipeline"). Each case is the server's real GraphView response for a
 * builder-rendered text — corpus goldens plus two shapes that hold references
 * inside a larger expression — generated and kept in sync by
 * internal/visual/parse_roundtrip_test.go, which proves the Go half; this file
 * proves renderTS gives back the same text once builderDraftFromGraphView has
 * dropped the view-only bindings.
 */
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { shippedSchema } from '../../tests/fixtures/schema-fixture';
import { builderDraftFromGraphView } from './graphViewFlow';
import { renderTS } from './renderTS';
import type { GraphDocument } from './types';

const cases = JSON.parse(
  readFileSync(new URL('../../tests/fixtures/graphview-roundtrip.json', import.meta.url), 'utf8'),
) as { name: string; text: string; graph: GraphDocument; opaque: boolean }[];

describe('GraphView → builder draft → renderTS', () => {
  it('covers the corpus and the expression shapes', () => {
    expect(cases.map((c) => c.name)).toEqual(
      expect.arrayContaining(['bindings-secret', 'buried-port-ref', 'mixed-list']),
    );
  });

  for (const c of cases) {
    it(`gives back the text it was parsed from: ${c.name}`, () => {
      expect(c.opaque).toBe(false);
      const r = renderTS(builderDraftFromGraphView(c.graph), shippedSchema);
      expect(r.diagnostics.map((d) => d.code)).not.toContain('prop_wire_conflict');
      expect(r.content).toBe(c.text);
    });
  }

  it('would render a reference twice if the view-only bindings were kept', () => {
    const secret = cases.find((c) => c.name === 'bindings-secret');
    expect(secret?.graph.bindings.length).toBeGreaterThan(0);
    const kept = renderTS(secret?.graph as GraphDocument, shippedSchema).content;
    expect(kept.match(/bearer_token = /g)).toHaveLength(2);
  });

  it('keeps the expression shapes as written, with no wire', () => {
    for (const name of ['buried-port-ref', 'mixed-list']) {
      const c = cases.find((x) => x.name === name) as (typeof cases)[number];
      expect(c.graph.edges).toEqual([]);
      expect(c.graph.bindings.length).toBeGreaterThan(0);
    }
    const buried = cases.find((x) => x.name === 'buried-port-ref');
    expect(buried?.text).toContain('targets = array.concat(discovery.kubernetes.pods.targets, [{');
    expect(buried?.text).toContain('"localhost:9090"');
  });
});
