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
 *
 * The corpus itself is read directly from `internal/visual/testdata/corpus` —
 * the same directory `internal/visual/render_test.go` reads — rather than from
 * a web-local copy. A copy needs a sync step (`make generate-corpus`) and a
 * test proving the copy stayed in sync; reading the one source directly needs
 * neither. Same precedent as `web/tests/fixtures/schema-fixture.ts` reading
 * `internal/schema/artifacts` directly instead of vendoring it.
 */
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { shippedSchema } from '../../tests/fixtures/schema-fixture';
import {
  MAX_COMPONENT_BYTES,
  MAX_DIAGNOSTIC_BYTES,
  MAX_DIAGNOSTICS,
  MAX_LABEL_BYTES,
  MAX_RENDER_BYTES,
  renderTS,
} from './renderTS';
import type { GraphDocument } from './types';

const goCorpusDir = join(__dirname, '../../../internal/visual/testdata/corpus');
const readGraph = (name: string): GraphDocument =>
  JSON.parse(readFileSync(join(goCorpusDir, `${name}.graph.json`), 'utf-8')) as GraphDocument;
const readGolden = (name: string) =>
  readFileSync(join(goCorpusDir, `${name}.golden.alloy`), 'utf-8');
// The diagnostics parity contract between this renderer and
// internal/visual/render.go: every corpus entry records the {code, severity}
// pairs both renderers must emit for the same graph, sorted so emission order
// isn't part of the contract. internal/visual/render_test.go's "7.2.1 corpus
// renders byte-exact" DescribeTable reads the exact same file per entry.
interface DiagExpectation {
  code: string;
  severity: string;
}
const readDiagnostics = (name: string): DiagExpectation[] =>
  JSON.parse(
    readFileSync(join(goCorpusDir, `${name}.diagnostics.json`), 'utf-8'),
  ) as DiagExpectation[];
const sortDiagnostics = (diags: Array<{ code: string; severity?: string }>): DiagExpectation[] =>
  diags
    .map(({ code, severity }) => ({ code, severity: severity ?? '' }))
    .sort((a, b) => a.code.localeCompare(b.code) || a.severity.localeCompare(b.severity));

const minimalScrapeGraph = readGraph('minimal-scrape');
const fanInFanOutGraph = readGraph('fanin-fanout');
const nestedBlocksGraph = readGraph('nested-blocks');
const bindingsSecretGraph = readGraph('bindings-secret');
const logsChainGraph = readGraph('logs-chain');
const disabledNodeGraph = readGraph('disabled-node');
const labelEdgecasesGraph = readGraph('label-edgecases');
const otelThreeSignalsGraph = readGraph('otel-three-signals');
const kitchenSinkGraph = readGraph('kitchen-sink');
const diagnosticsMixedGraph = readGraph('diagnostics-mixed');
const bindingsNestedGraph = readGraph('bindings-nested');

const minimalScrapeGolden = readGolden('minimal-scrape');
const fanInFanOutGolden = readGolden('fanin-fanout');
const nestedBlocksGolden = readGolden('nested-blocks');
const bindingsSecretGolden = readGolden('bindings-secret');
const logsChainGolden = readGolden('logs-chain');
const disabledNodeGolden = readGolden('disabled-node');
const labelEdgecasesGolden = readGolden('label-edgecases');
const otelThreeSignalsGolden = readGolden('otel-three-signals');
const kitchenSinkGolden = readGolden('kitchen-sink');
const diagnosticsMixedGolden = readGolden('diagnostics-mixed');
const bindingsNestedGolden = readGolden('bindings-nested');

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

const corpus: Array<{
  name: string;
  graph: unknown;
  golden: string;
  diagnostics: DiagExpectation[];
}> = [
  {
    name: 'minimal-scrape',
    graph: minimalScrapeGraph,
    golden: minimalScrapeGolden,
    diagnostics: readDiagnostics('minimal-scrape'),
  },
  {
    name: 'fanin-fanout',
    graph: fanInFanOutGraph,
    golden: fanInFanOutGolden,
    diagnostics: readDiagnostics('fanin-fanout'),
  },
  {
    name: 'nested-blocks',
    graph: nestedBlocksGraph,
    golden: nestedBlocksGolden,
    diagnostics: readDiagnostics('nested-blocks'),
  },
  {
    name: 'bindings-secret',
    graph: bindingsSecretGraph,
    golden: bindingsSecretGolden,
    diagnostics: readDiagnostics('bindings-secret'),
  },
  {
    name: 'logs-chain',
    graph: logsChainGraph,
    golden: logsChainGolden,
    diagnostics: readDiagnostics('logs-chain'),
  },
  {
    name: 'disabled-node',
    graph: disabledNodeGraph,
    golden: disabledNodeGolden,
    diagnostics: readDiagnostics('disabled-node'),
  },
  {
    name: 'label-edgecases',
    graph: labelEdgecasesGraph,
    golden: labelEdgecasesGolden,
    diagnostics: readDiagnostics('label-edgecases'),
  },
  {
    name: 'otel-three-signals',
    graph: otelThreeSignalsGraph,
    golden: otelThreeSignalsGolden,
    diagnostics: readDiagnostics('otel-three-signals'),
  },
  {
    name: 'kitchen-sink',
    graph: kitchenSinkGraph,
    golden: kitchenSinkGolden,
    diagnostics: readDiagnostics('kitchen-sink'),
  },
  {
    name: 'diagnostics-mixed',
    graph: diagnosticsMixedGraph,
    golden: diagnosticsMixedGolden,
    diagnostics: readDiagnostics('diagnostics-mixed'),
  },
  {
    // #426: doc.bindings[] entries at nested paths — a repeated block's
    // index, deeper nesting, a dotted block name (`stage.tenant`).
    name: 'bindings-nested',
    graph: bindingsNestedGraph,
    golden: bindingsNestedGolden,
    diagnostics: readDiagnostics('bindings-nested'),
  },
];

// 7.5.2 — TS codegen vs corpus
// RED-RUN PROOF: Changing attribute emission order in renderTS.ts (e.g. emitting in
// props-map insertion order instead of schema order) causes corpus entries with
// multiple attributes to produce output that doesn't match the golden → test fails.
// Because the schema is the shipped artifact, renaming a port inside it — say
// prometheus.remote_write's `receiver` export — also fails here, as the metrics hop
// stops resolving and reports edge_unresolved.
//
// Diagnostics are checked against `<name>.diagnostics.json` rather than a
// hard-coded `[]`: nine of the ten entries record no diagnostics, and
// "diagnostics-mixed" records the three codes (secret_by_value,
// empty_binding_expr, empty_binding_prop) this renderer and
// internal/visual/render.go both claim to emit for the same graph —
// render_test.go's "7.2.1 corpus renders byte-exact" DescribeTable asserts the
// same file, so a renderer that drops or renames one of these codes fails on
// whichever side changed.
describe('7.5.2 TS codegen vs corpus', () => {
  for (const { name, graph, golden, diagnostics } of corpus) {
    it(`${name} renders byte-exact`, () => {
      const doc = graph as GraphDocument;
      const result = renderTS(doc, shippedSchema);
      expect(sortDiagnostics(result.diagnostics)).toEqual(sortDiagnostics(diagnostics));
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
    // Bare, not `[...]`: a discovery export is already a []discovery.Target,
    // and Alloy v1.18.1 refuses a list of them at evaluation time (refValue in
    // renderTS.ts; docs/proofs/sandbox-sim-e2e.md §1).
    expect(r.content).toContain(
      'prometheus.scrape "app" {\n  targets = discovery.kubernetes.k8s.targets\n}',
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

  it('labels a one-node graph "1 node", as render.go does', () => {
    const r = renderTS(
      makeDoc([
        { id: 'n1', component: 'discovery.kubernetes', label: 'k8s', props: { role: 'pod' } },
      ]),
      shippedSchema,
    );
    expect(r.content).toMatch(
      /^\/\/ generated by shepherd visual builder — do not edit by hand \(edits will be overwritten\); 1 node, schema /,
    );
    expect(r.content).not.toContain('graph revision');
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
    // verified with `alloy RUN` (not just `alloy validate`) in the pinned
    // grafana/alloy:v1.18.1 image. `validate` accepts the bracketed
    // `targets = [discovery....targets]` spelling this assertion used to carry
    // and `run` refuses it, which is how that defect survived until a real
    // sandbox run hit it (docs/proofs/sandbox-sim-e2e.md §1).
    expect(r.content).toBe(
      `// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); 3 nodes, schema ${shippedSchema._meta.alloy_version}\n` +
        '\ndiscovery.kubernetes "k8s" {\n  role = "pod"\n}\n' +
        '\nprometheus.scrape "app" {\n' +
        '  targets = discovery.kubernetes.k8s.targets\n' +
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
    expect(r.content).toContain('targets = discovery.kubernetes.k8s.targets');
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

// `$raw` is the read-only graph view's copy of an opaque block's source
// (ParseAlloy, internal/visual/parse.go). Neither renderer may emit it —
// render.go skips it the same way (parse_graphview_test.go).
describe('$raw (graph-view source text)', () => {
  it('is never emitted for a component the schema does not know', () => {
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'opaque.declare',
          label: 'feature',
          props: { $raw: 'declare "feature" {}', comment: 'x' },
        },
      ]),
      shippedSchema,
    );
    expect(r.content).not.toContain('$raw');
    expect(r.content).toContain('comment = "x"');
  });

  it('is neither emitted nor reported for a known component', () => {
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'discovery.kubernetes',
          label: 'k',
          props: { role: 'pod', $raw: 'discovery.kubernetes "k" {}' },
        },
      ]),
      shippedSchema,
    );
    expect(r.content).not.toContain('$raw');
    expect(r.diagnostics.map((d) => d.message).join('\n')).not.toContain('$raw');
  });
});

// #400: the browser copy of #393's output bounds. The browser's size limits are
// looser than the server's (4 MiB of text and 800 diagnostics, against 1 MiB
// and 200): a preview of a graph the server would refuse is still useful up
// to the point it would hang the tab. The name limits are the server's 256
// bytes, because the server refuses a longer name and previewing one would
// mislead.
describe('output bounds (#400)', () => {
  const unknownNodes = (count: number, id = (i: number) => `n${i}`): TestNode[] =>
    Array.from({ length: count }, (_, i) => ({
      id: id(i),
      component: 'nonexistent.component',
      label: `x${i}`,
    }));

  it('refuses a graph whose text would pass 4 MiB, with no partial content', () => {
    // 15,000 × -1.79e308 is 105 KB of JSON and 4.6 MB of Alloy: the value is
    // refused while it is serialized, never built whole first.
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'nonexistent.component',
          label: 'big',
          props: { values: Array.from({ length: 15_000 }, () => -1.79e308) },
        },
      ]),
      shippedSchema,
    );
    expect(MAX_RENDER_BYTES).toBe(4 << 20);
    expect(r.content).toBe('');
    expect(r.nodeMap).toEqual({});
    const over = r.diagnostics.find((d) => d.code === 'output_too_large');
    expect(over?.severity).toBe('error');
    expect(over?.message).toBe(
      'the rendered pipeline would exceed 4194304 bytes (4 MiB), the most the preview renders',
    );
  });

  it('refuses a graph whose many small nodes add up past 4 MiB', () => {
    const r = renderTS(
      makeDoc(
        Array.from({ length: 20_000 }, (_, i) => ({
          id: `n${i}`,
          component: 'prometheus.relabel',
          label: `${'l'.repeat(200)}${i}`,
        })),
      ),
      shippedSchema,
    );
    expect(r.content).toBe('');
    expect(codes(r)).toEqual(['output_too_large']);
  });

  it('counts the cap in UTF-8 bytes, as render.go does', () => {
    // 1.5M × "€" is 1.5M UTF-16 units but 4.5 MB of UTF-8.
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'nonexistent.component',
          label: 'big',
          props: { s: '€'.repeat(1_500_000) },
        },
      ]),
      shippedSchema,
    );
    expect(codes(r)).toEqual(['unknown_component', 'output_too_large']);
    expect(r.content).toBe('');
  });

  it('still renders a graph under the cap', () => {
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'nonexistent.component',
          label: 'big',
          props: { s: 'a'.repeat(3 << 20) },
        },
      ]),
      shippedSchema,
    );
    expect(codes(r)).toEqual(['unknown_component']);
    expect(r.content.length).toBeGreaterThan(3 << 20);
    expect(r.nodeMap.n1).toEqual({ startLine: 3, endLine: 5 });
  });

  it('refuses a label over 256 bytes, measured in UTF-8', () => {
    const r = renderTS(
      makeDoc([
        { id: 'ok', component: 'prometheus.scrape', label: 'a'.repeat(256) },
        // 86 × "€" is 86 UTF-16 units and 258 bytes.
        { id: 'wide', component: 'prometheus.scrape', label: '€'.repeat(86) },
        { id: 'long', component: 'prometheus.scrape', label: 'b'.repeat(257), disabled: true },
      ]),
      shippedSchema,
    );
    expect(MAX_LABEL_BYTES).toBe(256);
    expect(r.content).toBe('');
    expect(r.diagnostics.map((d) => [d.code, d.severity, d.node_id])).toEqual([
      ['label_too_long', 'error', 'wide'],
      ['label_too_long', 'error', 'long'],
    ]);
    expect(r.diagnostics[0].message).toBe('the label is 258 bytes; the limit is 256');
  });

  it('refuses a component name over 256 bytes', () => {
    const r = renderTS(
      makeDoc([{ id: 'n1', component: `x.${'y'.repeat(256)}`, label: 'a' }]),
      shippedSchema,
    );
    expect(MAX_COMPONENT_BYTES).toBe(256);
    expect(r.content).toBe('');
    expect(codes(r)).toEqual(['component_too_long']);
    expect(r.diagnostics[0].message).toBe('the component name is 258 bytes; the limit is 256');
  });

  it('keeps 800 diagnostics and counts the rest into one error', () => {
    const r = renderTS(makeDoc(unknownNodes(1000)), shippedSchema);
    expect(MAX_DIAGNOSTICS).toBe(800);
    expect(r.diagnostics).toHaveLength(801);
    const last = r.diagnostics[800];
    expect(last.code).toBe('too_many_diagnostics');
    expect(last.severity).toBe('error');
    expect(last.message).toBe(
      '200 more diagnostics were not reported (at most 800, 262144 bytes, are)',
    );
  });

  it('caps the diagnostic list at 256 KiB', () => {
    // A node id is not clipped (it is the diagnostic's address), so ten
    // 30 KB ids pass 256 KiB of diagnostics long before 800 entries.
    const r = renderTS(
      makeDoc(unknownNodes(10, (i) => `${i}${'i'.repeat(30_000)}`)),
      shippedSchema,
    );
    expect(MAX_DIAGNOSTIC_BYTES).toBe(256 << 10);
    expect(codes(r)).toEqual([...Array(8).fill('unknown_component'), 'too_many_diagnostics']);
    expect(r.diagnostics[8].message).toMatch(/^2 more diagnostics/);
  });

  it('reports a list of dropped warnings as a warning', () => {
    const r = renderTS(
      makeDoc([
        {
          id: 'n1',
          component: 'prometheus.scrape',
          label: 'app',
          props: Object.fromEntries(Array.from({ length: 900 }, (_, i) => [`p${i}`, 1])),
        },
      ]),
      shippedSchema,
    );
    expect(r.diagnostics.at(-1)).toMatchObject({
      code: 'too_many_diagnostics',
      severity: 'warning',
    });
    expect(r.content).toContain('prometheus.scrape "app" {');
  });

  it("names at most ten of a cycle's components", () => {
    const nodes: TestNode[] = Array.from({ length: 15 }, (_, i) => ({
      id: `n${i}`,
      component: 'prometheus.relabel',
      label: `r${String(i).padStart(2, '0')}`,
    }));
    const edges: TestEdge[] = nodes.map((n, i) => ({
      id: `e${i}`,
      from: [n.id, 'forward_to'],
      to: [nodes[(i + 1) % nodes.length].id, 'receiver'],
    }));
    const cycle = renderTS(makeDoc(nodes, edges), shippedSchema).diagnostics.find(
      (d) => d.code === 'cycle',
    );
    const named = nodes.slice(0, 10).map((n) => `prometheus.relabel "${n.label}"`);
    expect(cycle?.message).toBe(
      `graph has a cycle through ${named.join(', ')}, 5 more; the emission order of those components is arbitrary`,
    );
  });

  it('clips a client-supplied string a message quotes to 64 bytes', () => {
    const r = renderTS(
      makeDoc(
        [
          { id: 'n1', component: 'prometheus.scrape', label: 'a' },
          { id: 'n2', component: 'prometheus.scrape', label: 'b' },
        ],
        [{ id: `e${'€'.repeat(100)}`, from: ['n1', 'nope'], to: ['n2', 'p'.repeat(500)] }],
      ),
      shippedSchema,
    );
    const msg = r.diagnostics.find((d) => d.code === 'edge_unresolved')?.message;
    // "e" + 20 × "€" is 61 bytes; the ellipsis (3 bytes) takes the clip to 64.
    expect(msg).toBe(
      `edge e${'€'.repeat(20)}…: prometheus.scrape.nope → prometheus.scrape.${'p'.repeat(61)}… does not connect an argument to a matching export; the reference was not emitted`,
    );
  });
});
