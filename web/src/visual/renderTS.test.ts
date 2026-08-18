import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import bindingsSecretGraph from './__fixtures__/corpus/bindings-secret.graph.json';
import disabledNodeGraph from './__fixtures__/corpus/disabled-node.graph.json';
import fanInFanOutGraph from './__fixtures__/corpus/fanin-fanout.graph.json';
import kitchenSinkGraph from './__fixtures__/corpus/kitchen-sink.graph.json';
import labelEdgecasesGraph from './__fixtures__/corpus/label-edgecases.graph.json';
import logsChainGraph from './__fixtures__/corpus/logs-chain.graph.json';
// Import corpus fixtures
import minimalScrapeGraph from './__fixtures__/corpus/minimal-scrape.graph.json';
import nestedBlocksGraph from './__fixtures__/corpus/nested-blocks.graph.json';
import otelThreeSignalsGraph from './__fixtures__/corpus/otel-three-signals.graph.json';
import { renderTS } from './renderTS';
import type { GraphDocument, SchemaPayload } from './types';

const fixtureDir = join(__dirname, '__fixtures__/corpus');
const readGolden = (name: string) =>
  readFileSync(join(fixtureDir, `${name}.golden.alloy`), 'utf-8');

const minimalScrapeGolden = readGolden('minimal-scrape');
const fanInFanOutGolden = readGolden('fanin-fanout');
const nestedBlocksGolden = readGolden('nested-blocks');
const bindingsSecretGolden = readGolden('bindings-secret');
const logsChainGolden = readGolden('logs-chain');
const disabledNodeGolden = readGolden('disabled-node');
const labelEdgecasesGolden = readGolden('label-edgecases');
const otelThreeSignalsGolden = readGolden('otel-three-signals');
const kitchenSinkGolden = readGolden('kitchen-sink');

// Schema matching web/tests/fixtures/schema-fixture.ts and internal/visual/render_test.go corpusSchema()
const corpusSchema: SchemaPayload = {
  _meta: { alloy_version: 'alloy-v1.18.1', components_total: 12 },
  components: {
    'discovery.kubernetes': {
      stability: 'ga',
      doc: 'Discovers Kubernetes pods/services.',
      category: 'sources',
      attributes: [{ name: 'role', type: 'string', required: true }],
      blocks: [],
      inputs: [],
      outputs: [{ export: 'targets', type: 'targets' }],
      default_snippet: '',
    },
    'discovery.relabel': {
      stability: 'ga',
      doc: 'Relabels targets.',
      category: 'transform',
      attributes: [],
      blocks: [],
      inputs: [{ prop: 'targets', type: 'targets', cardinality: 'list' }],
      outputs: [{ export: 'output', type: 'targets' }],
      default_snippet: '',
    },
    'prometheus.scrape': {
      stability: 'ga',
      doc: 'Scrapes Prometheus metrics.',
      category: 'transform',
      attributes: [
        { name: 'job_name', type: 'string', required: false },
        { name: 'scrape_interval', type: 'duration', required: false },
        { name: 'password', type: 'secret', required: false },
        { name: 'action', type: 'string', required: false },
      ],
      blocks: [],
      inputs: [{ prop: 'targets', type: 'targets', cardinality: 'list' }],
      outputs: [{ export: 'metrics', type: 'prom.metrics' }],
      default_snippet: '',
    },
    'prometheus.remote_write': {
      stability: 'ga',
      doc: 'Sends metrics to remote_write.',
      category: 'destinations',
      terminal_ok: true,
      attributes: [{ name: 'password', type: 'secret', required: false }],
      blocks: [],
      inputs: [{ prop: 'receiver', type: 'prom.metrics', cardinality: 'list' }],
      outputs: [],
      default_snippet: '',
    },
    'prometheus.relabel': {
      stability: 'ga',
      doc: 'Relabels metrics.',
      category: 'transform',
      attributes: [],
      blocks: [],
      inputs: [{ prop: 'forward_to', type: 'prom.metrics', cardinality: 'list' }],
      outputs: [{ export: 'receiver', type: 'prom.metrics' }],
      default_snippet: '',
    },
    'loki.source.file': {
      stability: 'ga',
      doc: 'Tails log files.',
      category: 'sources',
      attributes: [{ name: 'stage_type', type: 'string', required: false }],
      blocks: [],
      inputs: [],
      outputs: [{ export: 'logs', type: 'loki.logs' }],
      default_snippet: '',
    },
    'loki.process': {
      stability: 'ga',
      doc: 'Processes logs.',
      category: 'transform',
      attributes: [{ name: 'stage_type', type: 'string', required: false }],
      blocks: [],
      inputs: [{ prop: 'forward_to', type: 'loki.logs', cardinality: 'list' }],
      outputs: [{ export: 'receiver', type: 'loki.logs' }],
      default_snippet: '',
    },
    'loki.write': {
      stability: 'ga',
      doc: 'Sends logs to Loki.',
      category: 'destinations',
      terminal_ok: true,
      attributes: [],
      blocks: [],
      inputs: [{ prop: 'receiver', type: 'loki.logs', cardinality: 'list' }],
      outputs: [],
      default_snippet: '',
    },
    'remote.kubernetes.secret': {
      stability: 'ga',
      doc: 'Reads a Kubernetes secret.',
      category: 'config',
      attributes: [],
      blocks: [],
      inputs: [],
      outputs: [],
      default_snippet: '',
    },
    'otelcol.receiver.otlp': {
      stability: 'ga',
      doc: 'Receives OTLP data.',
      category: 'sources',
      attributes: [],
      blocks: [],
      inputs: [],
      outputs: [
        { export: 'output.metrics', type: 'otel.metrics' },
        { export: 'output.logs', type: 'otel.logs' },
        { export: 'output.traces', type: 'otel.traces' },
      ],
      default_snippet: '',
    },
    'otelcol.processor.batch': {
      stability: 'ga',
      doc: 'Batches OTLP data.',
      category: 'transform',
      attributes: [],
      blocks: [],
      inputs: [
        { prop: 'input.metrics', type: 'otel.metrics', cardinality: 'list' },
        { prop: 'input.logs', type: 'otel.logs', cardinality: 'list' },
        { prop: 'input.traces', type: 'otel.traces', cardinality: 'list' },
      ],
      outputs: [
        { export: 'input.metrics', type: 'otel.metrics' },
        { export: 'input.logs', type: 'otel.logs' },
        { export: 'input.traces', type: 'otel.traces' },
      ],
      default_snippet: '',
    },
    'otelcol.exporter.otlp': {
      stability: 'ga',
      doc: 'Exports OTLP data.',
      category: 'destinations',
      terminal_ok: true,
      attributes: [],
      blocks: [],
      inputs: [
        { prop: 'input.metrics', type: 'otel.metrics', cardinality: 'list' },
        { prop: 'input.logs', type: 'otel.logs', cardinality: 'list' },
        { prop: 'input.traces', type: 'otel.traces', cardinality: 'list' },
      ],
      outputs: [],
      default_snippet: '',
    },
  },
  wire_types: {
    targets: { color: 'violet', label: 'targets' },
    'prom.metrics': { color: 'orange', label: 'prom.metrics' },
    'loki.logs': { color: 'green', label: 'loki.logs' },
    'otel.any': { color: 'sky', label: 'otel' },
  },
};

// Seeded shuffle for deterministic permutation tests (matches Go test seed 42)
function seededShuffle<T>(arr: T[], seed: number): T[] {
  const result = [...arr];
  // Linear congruential generator matching enough of rand.New(rand.NewSource(42))
  let s = seed;
  const next = () => {
    s = Math.imul(s, 1664525) + 1013904223;
    return (s >>> 0) / 0x100000000;
  };
  for (let i = result.length - 1; i > 0; i--) {
    const j = Math.floor(next() * (i + 1));
    [result[i], result[j]] = [result[j], result[i]];
  }
  return result;
}

const corpus: Array<{ name: string; graph: unknown; golden: string }> = [
  { name: 'minimal-scrape', graph: minimalScrapeGraph, golden: minimalScrapeGolden },
  { name: 'fanin-fanout', graph: fanInFanOutGraph, golden: fanInFanOutGolden },
  { name: 'nested-blocks', graph: nestedBlocksGraph, golden: nestedBlocksGolden },
  { name: 'bindings-secret', graph: bindingsSecretGraph, golden: bindingsSecretGolden },
  { name: 'logs-chain', graph: logsChainGraph, golden: logsChainGolden },
  { name: 'disabled-node', graph: disabledNodeGraph, golden: disabledNodeGolden },
  { name: 'label-edgecases', graph: labelEdgecasesGraph, golden: labelEdgecasesGolden },
  { name: 'otel-three-signals', graph: otelThreeSignalsGraph, golden: otelThreeSignalsGolden },
  { name: 'kitchen-sink', graph: kitchenSinkGraph, golden: kitchenSinkGolden },
];

// 7.5.2 — TS codegen vs corpus
// RED-RUN PROOF: Changing attribute emission order in renderTS.ts (e.g. emitting in
// props-map insertion order instead of schema order) causes corpus entries with
// multiple attributes to produce output that doesn't match the golden → test fails.
describe('7.5.2 TS codegen vs corpus', () => {
  for (const { name, graph, golden } of corpus) {
    it(`${name} renders byte-exact`, () => {
      const doc = graph as GraphDocument;
      const result = renderTS(doc, corpusSchema);
      expect(result.diagnostics).toEqual([]);
      expect(result.content).toBe(golden);
    });
  }

  // Permutation invariance for key entries
  for (const name of ['minimal-scrape', 'fanin-fanout', 'kitchen-sink']) {
    it(`${name} is permutation-invariant (5 shuffles)`, () => {
      const entry = corpus.find((c) => c.name === name)!;
      const doc = entry.graph as GraphDocument;
      const golden = entry.golden;
      for (let i = 0; i < 5; i++) {
        const shuffled: GraphDocument = {
          ...doc,
          nodes: seededShuffle(doc.nodes, 42 + i * 7),
          edges: seededShuffle(doc.edges, 42 + i * 13),
        };
        const result = renderTS(shuffled, corpusSchema);
        expect(result.content).toBe(golden);
      }
    });
  }
});
