/**
 * Both renderers are proved against the SHIPPED schema — the artifact the Go
 * server embeds, deep-merged with the same overlay — and against the same corpus
 * of graphs and goldens as `internal/visual/render_test.go`. There is no
 * hand-written component model anywhere in this file.
 *
 * That is the whole point of the rewrite. Until 2026-08-19 this file declared
 * twelve components whose port model was the mirror image of the shipped one
 * (`prometheus.scrape` "exported" metrics; `prometheus.remote_write` "accepted" a
 * receiver), the Go suite declared the same fiction, and all nine goldens matched
 * on both sides while being config that real Alloy rejects. A fixture cannot
 * disagree with the artifact if it *is* the artifact.
 */
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { shippedSchema } from '../../tests/fixtures/schema-fixture';
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
import type { GraphDocument } from './types';

const fixtureDir = join(__dirname, '__fixtures__/corpus');
const goCorpusDir = join(__dirname, '../../../internal/visual/testdata/corpus');
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
// Because the schema is the shipped artifact, renaming a port inside it — say
// prometheus.remote_write's `receiver` export — also fails here, as the metrics hop
// stops resolving and reports edge_unresolved.
describe('7.5.2 TS codegen vs corpus', () => {
  for (const { name, graph, golden } of corpus) {
    it(`${name} renders byte-exact`, () => {
      const doc = graph as GraphDocument;
      const result = renderTS(doc, shippedSchema);
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
        const result = renderTS(shuffled, shippedSchema);
        expect(result.content).toBe(golden);
      }
    });
  }

  // `make generate-corpus` copies the Go corpus into web/src/visual/__fixtures__.
  // A half-run copy is invisible otherwise: the TS suite would keep passing
  // against a stale golden while the Go suite asserts a newer one.
  it('the web corpus copies are byte-identical to the Go originals', () => {
    const goFiles = readdirSync(goCorpusDir).sort();
    expect(readdirSync(fixtureDir).sort()).toEqual(goFiles);
    for (const file of goFiles) {
      expect(readFileSync(join(fixtureDir, file), 'utf-8'), `${file} is out of sync`).toBe(
        readFileSync(join(goCorpusDir, file), 'utf-8'),
      );
    }
  });
});

// ---------------------------------------------------------------------------
// D1 port model. Every component named below is read from the shipped artifact,
// so the roles and paths under test are the ones the product ships:
//   - discovery.kubernetes exports `targets` — a DATA export, role "produces".
//   - prometheus.remote_write exports `receiver` — a RECEIVER export, role
//     "accepts", so the *producer* holds the reference.
//   - otelcol.* consumers expose `output.metrics` / `output.logs` /
//     `output.traces` arguments at path ["output", <signal>], role "produces".
// ---------------------------------------------------------------------------

interface TestNode {
  id: string;
  component: string;
  label: string;
  props?: Record<string, unknown>;
  disabled?: boolean;
}
interface TestEdge {
  id: string;
  from: [string, string];
  to: [string, string];
  order?: number;
}

const makeDoc = (nodes: TestNode[], edges: TestEdge[] = []): GraphDocument =>
  ({
    kind: 'alloy-graph/v1',
    schema_version: 'v1.18.1',
    nodes: nodes.map((n) => ({
      id: n.id,
      component: n.component,
      label: n.label,
      position: { x: 0, y: 0 },
      props: n.props ?? {},
      disabled: n.disabled ?? false,
      notes: '',
    })),
    edges: edges.map((e) => ({
      id: e.id,
      from: { node: e.from[0], port: e.from[1] },
      to: { node: e.to[0], port: e.to[1] },
      ...(e.order === undefined ? {} : { order: e.order }),
    })),
    bindings: [],
    viewport: { x: 0, y: 0, zoom: 1 },
    meta: { created_with: 'test' },
  }) as GraphDocument;

const codes = (r: ReturnType<typeof renderTS>) => r.diagnostics.map((d) => d.code);

describe('D1 direction', () => {
  it('emits a data-export reference inside the consumer', () => {
    const r = renderTS(
      makeDoc(
        [
          { id: 'n1', component: 'discovery.kubernetes', label: 'k8s', props: { role: 'pod' } },
          { id: 'n2', component: 'prometheus.scrape', label: 'app' },
        ],
        [{ id: 'e1', from: ['n1', 'targets'], to: ['n2', 'targets'] }],
      ),
      shippedSchema,
    );
    expect(r.diagnostics).toEqual([]);
    expect(r.content).toContain(
      'prometheus.scrape "app" {\n  targets = [discovery.kubernetes.k8s.targets]\n}',
    );
  });

  it('emits a receiver-export reference inside the producer', () => {
    const r = renderTS(
      makeDoc(
        [
          { id: 'n2', component: 'prometheus.scrape', label: 'app' },
          { id: 'n3', component: 'prometheus.remote_write', label: 'sink' },
        ],
        [{ id: 'e2', from: ['n2', 'forward_to'], to: ['n3', 'receiver'] }],
      ),
      shippedSchema,
    );
    expect(r.diagnostics).toEqual([]);
    expect(r.content).toContain(
      'prometheus.scrape "app" {\n  forward_to = [prometheus.remote_write.sink.receiver]\n}',
    );
    expect(r.content).toContain('prometheus.remote_write "sink" {\n}');
  });

  it("emits a nested-block reference at the port's declared path", () => {
    const r = renderTS(
      makeDoc(
        [
          { id: 'n1', component: 'otelcol.receiver.otlp', label: 'ingest' },
          { id: 'n2', component: 'otelcol.exporter.otlp', label: 'remote' },
        ],
        [
          { id: 'e1', from: ['n1', 'output.metrics'], to: ['n2', 'input'] },
          { id: 'e2', from: ['n1', 'output.logs'], to: ['n2', 'input'] },
        ],
      ),
      shippedSchema,
    );
    expect(r.diagnostics).toEqual([]);
    expect(r.content).toContain(
      'otelcol.receiver.otlp "ingest" {\n' +
        '  output {\n' +
        '    metrics = [otelcol.exporter.otlp.remote.input]\n' +
        '    logs = [otelcol.exporter.otlp.remote.input]\n' +
        '  }\n' +
        '}',
    );
  });

  it('reports an edge that connects two arguments instead of dropping it', () => {
    const r = renderTS(
      makeDoc(
        [
          { id: 'n1', component: 'prometheus.scrape', label: 'a' },
          { id: 'n2', component: 'prometheus.scrape', label: 'b' },
        ],
        [{ id: 'e1', from: ['n1', 'forward_to'], to: ['n2', 'targets'] }],
      ),
      shippedSchema,
    );
    expect(codes(r)).toContain('edge_unresolved');
    expect(r.content).not.toContain('forward_to');
  });

  it('renders both directions of a real metrics pipeline', () => {
    const r = renderTS(
      makeDoc(
        [
          { id: 'n1', component: 'discovery.kubernetes', label: 'k8s', props: { role: 'pod' } },
          {
            id: 'n2',
            component: 'prometheus.scrape',
            label: 'app',
            props: { job_name: 'app', scrape_interval: '30s' },
          },
          {
            id: 'n3',
            component: 'prometheus.remote_write',
            label: 'sink',
            props: { endpoint: [{ url: 'https://prom/api/v1/write' }] },
          },
        ],
        [
          { id: 'e1', from: ['n1', 'targets'], to: ['n2', 'targets'] },
          { id: 'e2', from: ['n2', 'forward_to'], to: ['n3', 'receiver'] },
        ],
      ),
      shippedSchema,
    );
    expect(r.diagnostics).toEqual([]);
    // Byte-identical to internal/visual/render_rules_test.go's expectation, and
    // accepted by `alloy validate` in the pinned grafana/alloy:v1.18.1 image.
    expect(r.content).toBe(
      '// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); graph revision 3, schema v1.18.1\n' +
        '\ndiscovery.kubernetes "k8s" {\n  role = "pod"\n}\n' +
        '\nprometheus.scrape "app" {\n' +
        '  targets = [discovery.kubernetes.k8s.targets]\n' +
        '  forward_to = [prometheus.remote_write.sink.receiver]\n' +
        '  job_name = "app"\n' +
        '  scrape_interval = "30s"\n}\n' +
        '\nprometheus.remote_write "sink" {\n' +
        '  endpoint {\n    url = "https://prom/api/v1/write"\n  }\n}\n',
    );
  });

  it('renders a real OTel chain', () => {
    const r = renderTS(
      makeDoc(
        [
          { id: 'n1', component: 'otelcol.receiver.otlp', label: 'ingest' },
          { id: 'n2', component: 'otelcol.processor.batch', label: 'batcher' },
          { id: 'n3', component: 'otelcol.exporter.otlp', label: 'remote' },
        ],
        [
          { id: 'e1', from: ['n1', 'output.metrics'], to: ['n2', 'input'] },
          { id: 'e2', from: ['n2', 'output.metrics'], to: ['n3', 'input'] },
        ],
      ),
      shippedSchema,
    );
    expect(r.diagnostics).toEqual([]);
    expect(r.content).toContain(
      'otelcol.receiver.otlp "ingest" {\n  output {\n    metrics = [otelcol.processor.batch.batcher.input]\n  }\n}',
    );
    expect(r.content).toContain(
      'otelcol.processor.batch "batcher" {\n  output {\n    metrics = [otelcol.exporter.otlp.remote.input]\n  }\n}',
    );
  });
});

describe('nested blocks (D2)', () => {
  it('emits repeatable blocks, nested blocks and indentation', () => {
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'prometheus.remote_write',
          label: 'sink',
          props: {
            endpoint: [
              {
                url: 'https://a/write',
                remote_timeout: '30s',
                basic_auth: {
                  username: 'u',
                  password: { $expr: 'remote.kubernetes.secret.s.data["pw"]' },
                },
              },
              { url: 'https://b/write' },
            ],
          },
        },
      ]),
      shippedSchema,
    );
    expect(r.diagnostics).toEqual([]);
    expect(r.content).toContain(
      'prometheus.remote_write "sink" {\n' +
        '  endpoint {\n' +
        '    url = "https://a/write"\n' +
        '    remote_timeout = "30s"\n' +
        '    basic_auth {\n' +
        '      username = "u"\n' +
        '      password = remote.kubernetes.secret.s.data["pw"]\n' +
        '    }\n' +
        '  }\n' +
        '  endpoint {\n' +
        '    url = "https://b/write"\n' +
        '  }\n' +
        '}',
    );
  });

  it('emits an empty block for an empty object', () => {
    const r = renderTS(
      makeDoc([
        { id: 'n1', component: 'otelcol.receiver.otlp', label: 'ingest', props: { grpc: {} } },
      ]),
      shippedSchema,
    );
    expect(r.content).toContain('otelcol.receiver.otlp "ingest" {\n  grpc {\n  }\n}');
  });

  it('reports a repeated non-repeatable block', () => {
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'otelcol.receiver.otlp',
          label: 'ingest',
          props: { grpc: [{}, {}] },
        },
      ]),
      shippedSchema,
    );
    expect(codes(r)).toContain('block_not_repeatable');
  });

  it('reports a block whose value is not an object', () => {
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'otelcol.receiver.otlp',
          label: 'ingest',
          props: { grpc: '0.0.0.0:4317' },
        },
      ]),
      shippedSchema,
    );
    expect(codes(r)).toContain('prop_type_mismatch');
  });
});

describe('typed serialization', () => {
  const render = (props: Record<string, unknown>) =>
    renderTS(
      makeDoc([{ id: 'n1', component: 'prometheus.scrape', label: 'app', props }]),
      shippedSchema,
    );

  it('renders each declared type in its Alloy form', () => {
    const r = render({
      job_name: 'app',
      scrape_interval: '30s',
      sample_limit: 1000,
      honor_labels: true,
      params: { b: '2', 'a-x': '1' },
      scrape_protocols: ['PrometheusText1.0.0', 2],
    });
    expect(r.diagnostics).toEqual([]);
    expect(r.content).toContain('  job_name = "app"\n');
    expect(r.content).toContain('  scrape_interval = "30s"\n');
    expect(r.content).toContain('  sample_limit = 1000\n');
    expect(r.content).toContain('  honor_labels = true\n');
    expect(r.content).toContain('  params = {"a-x" = "1", b = "2"}\n');
    expect(r.content).toContain('  scrape_protocols = ["PrometheusText1.0.0", 2]\n');
  });

  it('sorts map keys by byte order, matching the Go renderer', () => {
    const r = render({ params: { ab: '2', a_b: '1', B: '3' } });
    expect(r.content).toContain('params = {B = "3", a_b = "1", ab = "2"}');
  });

  it('never inlines a secret', () => {
    const r = render({ bearer_token: 'hunter2' });
    expect(codes(r)).toContain('secret_by_value');
    expect(r.content).not.toContain('hunter2');
  });

  it('accepts a secret supplied as an expression', () => {
    const r = render({ bearer_token: { $expr: 'sys.env("TOKEN")' } });
    expect(r.diagnostics).toEqual([]);
    expect(r.content).toContain('  bearer_token = sys.env("TOKEN")\n');
  });

  it('refuses to quote a string into a list-typed attribute', () => {
    const r = render({ scrape_protocols: 'PrometheusText1.0.0' });
    expect(codes(r)).toContain('prop_type_mismatch');
    expect(r.content).not.toContain('scrape_protocols');
  });

  it('reports a duration without a unit', () => {
    expect(codes(render({ scrape_interval: 30 }))).toContain('invalid_duration');
  });

  it('coerces a numeric string typed as number', () => {
    const r = render({ sample_limit: '1000' });
    expect(r.diagnostics).toEqual([]);
    expect(r.content).toContain('  sample_limit = 1000\n');
  });

  it('omits a null prop rather than stringifying it', () => {
    const r = render({ job_name: null });
    expect(r.diagnostics).toEqual([]);
    expect(r.content).not.toContain('job_name');
  });

  it('emits a capsule value as an expression and warns about unknown props', () => {
    // loki.source.docker.relabel_rules is one of the six capsule-typed
    // attributes in the artifact: its only legal value is another component's
    // export, so a string must be emitted bare rather than quoted.
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'loki.source.docker',
          label: 'c',
          props: { relabel_rules: 'discovery.relabel.filter.rules', nope: 'x' },
        },
      ]),
      shippedSchema,
    );
    expect(r.content).toContain('  relabel_rules = discovery.relabel.filter.rules\n');
    expect(codes(r)).toContain('unknown_prop');
    expect(r.content).not.toContain('nope');
  });

  it('formats large numbers without an exponent, matching the Go renderer', () => {
    expect(render({ sample_limit: 1e21 }).content).toContain(`sample_limit = 1${'0'.repeat(21)}\n`);
  });
});

describe('diagnostics', () => {
  it('accumulates diagnostics instead of replacing them', () => {
    const r = renderTS(
      makeDoc([
        { id: 'n1', component: 'nonexistent.component', label: 'x' },
        {
          id: 'n2',
          component: 'prometheus.scrape',
          label: 'app',
          props: { bearer_token: 'hunter2' },
        },
      ]),
      shippedSchema,
    );
    expect(codes(r)).toContain('unknown_component');
    expect(codes(r)).toContain('secret_by_value');
  });

  it('reports every colliding label pair', () => {
    const r = renderTS(
      makeDoc([
        { id: 'a', component: 'prometheus.remote_write', label: 'my-sink' },
        { id: 'b', component: 'prometheus.remote_write', label: 'my_sink' },
        { id: 'c', component: 'prometheus.scrape', label: 'one two' },
        { id: 'e', component: 'prometheus.scrape', label: 'one-two' },
      ]),
      shippedSchema,
    );
    expect(r.content).toBe('');
    expect(codes(r)).toEqual(['label_collision', 'label_collision']);
  });

  it('keeps unknown-component props instead of dropping them', () => {
    const r = renderTS(
      makeDoc([
        { id: 'n1', component: 'nonexistent.component', label: 'x', props: { b: '2', a: 1 } },
      ]),
      shippedSchema,
    );
    expect(codes(r)).toContain('unknown_component');
    expect(r.content).toContain('nonexistent.component "x" {\n  a = 1\n  b = "2"\n}');
  });

  it('reports a prop that is also satisfied by a wire', () => {
    const r = renderTS(
      makeDoc(
        [
          { id: 'n1', component: 'discovery.kubernetes', label: 'k8s' },
          { id: 'n2', component: 'prometheus.scrape', label: 'app', props: { targets: [] } },
        ],
        [{ id: 'e1', from: ['n1', 'targets'], to: ['n2', 'targets'] }],
      ),
      shippedSchema,
    );
    expect(codes(r)).toContain('prop_wire_conflict');
    expect(r.content).toContain('targets = [discovery.kubernetes.k8s.targets]');
  });

  it('reports an empty binding on both fields and skips the line', () => {
    const doc = makeDoc([{ id: 'n1', component: 'prometheus.scrape', label: 'app' }]);
    doc.bindings = [
      { node: 'n1', prop: 'job_name', ref: { node: '', export: '', expr: '  ' } },
      { node: 'n1', prop: '', ref: { node: '', export: '', expr: 'x' } },
    ];
    const r = renderTS(doc, shippedSchema);
    expect(codes(r)).toEqual(['empty_binding_expr', 'empty_binding_prop']);
    expect(r.content).toContain('prometheus.scrape "app" {\n}');
  });

  it('reports a cycle and still orders it deterministically', () => {
    // prometheus.relabel is a real two-ended transform: its `forward_to`
    // argument produces and its `receiver` export accepts, so two of them wired
    // to each other is a genuine cycle rather than a synthetic one.
    const nodes: TestNode[] = [
      { id: 'n1', component: 'prometheus.relabel', label: 'zzz' },
      { id: 'n2', component: 'prometheus.relabel', label: 'aaa' },
    ];
    const edges: TestEdge[] = [
      { id: 'e1', from: ['n1', 'forward_to'], to: ['n2', 'receiver'] },
      { id: 'e2', from: ['n2', 'forward_to'], to: ['n1', 'receiver'] },
    ];
    const first = renderTS(makeDoc(nodes, edges), shippedSchema);
    expect(codes(first)).toContain('cycle');
    const reversed = renderTS(makeDoc([...nodes].reverse(), edges), shippedSchema);
    expect(reversed.content).toBe(first.content);
    expect(first.content.indexOf('"aaa"')).toBeLessThan(first.content.indexOf('"zzz"'));
  });

  it('sanitizes labels by code point, matching the Go renderer', () => {
    const r = renderTS(
      makeDoc([{ id: 'n1', component: 'prometheus.scrape', label: 'a😀b' }]),
      shippedSchema,
    );
    expect(r.content).toContain('prometheus.scrape "a_b"');
  });
});
