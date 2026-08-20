import { describe, expect, it } from 'vitest';
import type { ComponentDef } from '../types';
import { layoutPorts, portRole } from './PipelineNode';

// prometheus.scrape shape, per the real schema artifact (not the fictional
// mocked-suite fixture — see docs/archive/reviews/canvas-ux-and-forms.md "root
// cause"): two ARGUMENTS (schema `inputs`), no exports. `targets` is a
// DATA-kind wire; `forward_to` is a RECEIVER-kind wire (it takes a list of
// prometheus.remote_write-style receivers).
const scrape: ComponentDef = {
  stability: 'ga',
  doc: 'Scrapes Prometheus metrics',
  attributes: [],
  blocks: [],
  inputs: [
    { prop: 'targets', type: 'targets', cardinality: 'list' },
    { prop: 'forward_to', type: 'prom.metrics', cardinality: 'list' },
  ],
  outputs: [],
  default_snippet: '',
};

describe('PipelineNode port layout', () => {
  describe('portRole (D1)', () => {
    it('a data-kind argument (targets) accepts; a data-kind export produces', () => {
      expect(portRole('targets', 'input')).toBe('accepts');
      expect(portRole('targets', 'output')).toBe('produces');
    });

    it('a receiver-kind argument (forward_to) produces; a receiver-kind export (receiver) accepts', () => {
      expect(portRole('prom.metrics', 'input')).toBe('produces');
      expect(portRole('prom.metrics', 'output')).toBe('accepts');
      expect(portRole('loki.logs', 'input')).toBe('produces');
      expect(portRole('loki.logs', 'output')).toBe('accepts');
    });
  });

  describe('layoutPorts', () => {
    it("places prometheus.scrape's targets (data) on the left and forward_to (receiver) on the right, per D1", () => {
      const { left, right } = layoutPorts(scrape);
      expect(left.map((p) => p.handleId)).toEqual(['targets']);
      expect(right.map((p) => p.handleId)).toEqual(['forward_to']);
    });

    // The CRITICAL defect this task fixes: React Flow's default `top: 50%`
    // rendered every handle on a node at the identical point — measured on a
    // real prometheus.scrape node, both `targets` and `forward_to` landed at
    // (736, 512). A precise drop on the port dot silently hit the wrong
    // handle. Each port in the same column must now get a distinct, ordered
    // vertical coordinate.
    it('gives every port in the same column its own, distinct vertical coordinate', () => {
      const twoDataInputs: ComponentDef = {
        ...scrape,
        inputs: [
          { prop: 'a', type: 'targets' },
          { prop: 'b', type: 'targets' },
        ],
      };
      const { left } = layoutPorts(twoDataInputs);
      expect(left).toHaveLength(2);
      const tops = left.map((p) => p.top);
      expect(new Set(tops).size).toBe(tops.length);
      expect(tops[1]).toBeGreaterThan(tops[0]);
    });

    it('keeps rfType (source/target) tied to the schema classification, not the visual role', () => {
      const { left, right } = layoutPorts(scrape);
      // forward_to renders on the right (role "produces") but is still a
      // React Flow `target` handle — CanvasPane's connection/edge model
      // depends on this staying unchanged; only the rendered side moved.
      expect(right.find((p) => p.handleId === 'forward_to')?.rfType).toBe('target');
      expect(left.find((p) => p.handleId === 'targets')?.rfType).toBe('target');
    });

    it('handles a component with no ports', () => {
      expect(layoutPorts(undefined)).toEqual({ left: [], right: [] });
    });
  });
});
