import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { canConnectPorts, portsCompatible, resolvePorts, sanitizeLabel, validateGraph } from './l1';
import type { GraphBinding, GraphDocument, GraphEdge, GraphNode, SchemaPayload } from './types';

/**
 * These tests run against the SHIPPED schema — the same artifact the server
 * embeds, deep-merged with the same overlay — not a hand-written fixture. The
 * review that prompted this rewrite measured L1's rules against a fixture whose
 * port model was the mirror image of the real one, so every rule passed while
 * a correctly wired `prometheus.scrape` showed two blocking errors in the app.
 * Loading the real payload is what makes these assertions mean anything.
 */
const artifactsDir = join(__dirname, '../../../internal/schema/artifacts');
const readJson = (file: string) => JSON.parse(readFileSync(join(artifactsDir, file), 'utf-8'));
const isObj = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);
/** The server's deepMerge (internal/schema/registry.go): overlay over artifact. */
function deepMerge(base: unknown, over: unknown): unknown {
  if (!isObj(base) || !isObj(over)) return over;
  const out: Record<string, unknown> = { ...base };
  for (const [k, v] of Object.entries(over)) out[k] = k in base ? deepMerge(base[k], v) : v;
  return out;
}
const artifactFile = readdirSync(artifactsDir)
  .filter((f) => f.startsWith('alloy-') && f.endsWith('.json'))
  .sort()
  .at(-1) as string;
const schema = deepMerge(readJson(artifactFile), readJson('overlay.json')) as SchemaPayload;

const doc = (
  nodes: GraphNode[] = [],
  edges: GraphEdge[] = [],
  bindings: GraphBinding[] = [],
): GraphDocument => ({
  kind: 'alloy-graph/v1',
  schema_version: 'x',
  nodes,
  edges,
  bindings,
  viewport: { x: 0, y: 0, zoom: 1 },
  meta: { created_with: 'test' },
});
const node = (
  id: string,
  component: string,
  props: Record<string, unknown> = {},
  extra: Partial<GraphNode> = {},
): GraphNode => ({
  id,
  component,
  label: id,
  position: { x: 0, y: 0 },
  props,
  disabled: false,
  notes: '',
  ...extra,
});
const wire = (
  id: string,
  from: string,
  fromPort: string,
  to: string,
  toPort: string,
): GraphEdge => ({
  id,
  from: { node: from, port: fromPort },
  to: { node: to, port: toPort },
});
const codes = (d: ReturnType<typeof validateGraph>) => d.map((x) => x.code);
const errors = (d: ReturnType<typeof validateGraph>) => d.filter((x) => x.severity === 'error');

/**
 * The reference pipeline, built the way the canvas builds it under D1: data
 * flows left to right, so every edge runs from a "produces" port to an
 * "accepts" port — including `prometheus.scrape.forward_to` →
 * `prometheus.remote_write.receiver`, which Alloy expresses the other way
 * round and the renderer inverts.
 */
const referencePipeline = () =>
  doc(
    [
      node('n1', 'discovery.kubernetes', { role: 'pod' }),
      node('n2', 'prometheus.scrape', { job_name: 'app' }),
      node('n3', 'prometheus.remote_write', {
        endpoint: [{ url: 'http://mimir:9009/api/v1/push' }],
      }),
    ],
    [
      wire('e1', 'n1', 'targets', 'n2', 'targets'),
      wire('e2', 'n2', 'forward_to', 'n3', 'receiver'),
    ],
  );

describe('L1 against the shipped schema', () => {
  it('the schema under test is the real one', () => {
    expect(Object.keys(schema.components).length).toBe(schema._meta.components_total);
    expect(Object.keys(schema.components).length).toBeGreaterThan(100);
    // Ports carry D1 roles; without them every rule below is meaningless.
    expect(resolvePorts(schema.components['prometheus.remote_write']).map((p) => p.role)).toEqual([
      'accepts',
    ]);
  });

  it('a correctly built discovery → scrape → remote_write pipeline is clean', () => {
    const diags = validateGraph(referencePipeline(), schema);
    expect(errors(diags)).toEqual([]);
    expect(diags).toEqual([]);
  });

  it('a wired required port is not reported missing (the 42-component false positive)', () => {
    const diags = validateGraph(referencePipeline(), schema);
    expect(codes(diags)).not.toContain('required_attr_missing');
    expect(codes(diags)).not.toContain('dangling_input');
    expect(codes(diags)).not.toContain('output_nowhere');
  });

  it('no component reports a port-backed attribute as a missing attribute', () => {
    // The sweep the review's measurement would have caught: for every one of the
    // 184 components, a bare node must never raise required_attr_missing for an
    // attribute that is reachable as a port — that is what a wire is for.
    const offenders: string[] = [];
    for (const name of Object.keys(schema.components)) {
      const portPaths = new Set(resolvePorts(schema.components[name]).map((p) => p.path.join('.')));
      const diags = validateGraph(doc([node('n', name)]), schema, { allowExperimental: true });
      for (const d of diags)
        if (d.code === 'required_attr_missing' && portPaths.has((d.path ?? []).join('.')))
          offenders.push(`${name}.${(d.path ?? []).join('.')}`);
    }
    expect(offenders).toEqual([]);
  });

  it('validates every component in the schema without throwing', () => {
    for (const name of Object.keys(schema.components))
      expect(() =>
        validateGraph(doc([node('n', name)]), schema, { allowExperimental: true }),
      ).not.toThrow();
  });
});

describe('L1 required-ness', () => {
  it('a genuinely unset required attribute is reported with its path', () => {
    const diags = validateGraph(doc([node('n', 'discovery.kubernetes')]), schema);
    const missing = diags.find((d) => d.code === 'required_attr_missing');
    expect(missing?.node_id).toBe('n');
    expect(missing?.path).toEqual(['role']);
    expect(missing?.severity).toBe('error');
  });

  it('a required attribute inside a block is reported against the node with a focusable path', () => {
    const diags = validateGraph(
      doc([node('n', 'prometheus.remote_write', { endpoint: [{ name: 'primary' }] })]),
      schema,
    );
    const missing = diags.find((d) => d.code === 'required_attr_missing');
    // `endpoint` is repeatable, so the instance index is part of the path.
    expect(missing?.path).toEqual(['endpoint', '0', 'url']);
    expect(missing?.message).toContain('url');
    expect(missing?.message).toContain('endpoint');
  });

  it('a block the user never opened does not report its required attributes', () => {
    // prometheus.remote_write's `endpoint` block is optional; its `url` is only
    // required once an endpoint exists.
    const diags = validateGraph(doc([node('n', 'prometheus.remote_write')]), schema);
    expect(codes(diags)).not.toContain('required_attr_missing');
  });

  it('a required block with nothing in it is reported', () => {
    const diags = validateGraph(doc([node('n', 'otelcol.exporter.otlp')]), schema);
    const missing = diags.find((d) => d.code === 'required_block_missing');
    expect(missing?.path).toEqual(['client']);
    expect(missing?.severity).toBe('error');
  });

  it('a required block that is filled in is not reported', () => {
    const diags = validateGraph(
      doc([node('n', 'otelcol.exporter.otlp', { client: { endpoint: 'otel:4317' } })]),
      schema,
    );
    expect(codes(diags)).not.toContain('required_block_missing');
  });

  it('a required block carrying only ports is satisfied by a wire, not by props', () => {
    // otelcol's `output` block is required and holds nothing but consumer
    // ports, so an unwired receiver is missing it and a wired one is not.
    const bare = validateGraph(doc([node('n', 'otelcol.receiver.otlp')]), schema);
    expect(bare.some((d) => d.code === 'required_block_missing' && d.path?.[0] === 'output')).toBe(
      true,
    );
    const wired = validateGraph(
      doc(
        [node('n', 'otelcol.receiver.otlp'), node('x', 'otelcol.exporter.otlp', { client: {} })],
        [wire('e', 'n', 'output.metrics', 'x', 'input')],
      ),
      schema,
    );
    expect(wired.some((d) => d.node_id === 'n' && d.code === 'required_block_missing')).toBe(false);
  });

  it('a binding satisfies a required attribute', () => {
    const withBinding = validateGraph(
      doc(
        [node('n', 'discovery.kubernetes')],
        [],
        [{ node: 'n', prop: 'role', ref: { node: 'c', export: 'content', expr: 'local.file.c' } }],
      ),
      schema,
    );
    expect(codes(withBinding)).not.toContain('required_attr_missing');
    const emptyBinding = validateGraph(
      doc(
        [node('n', 'discovery.kubernetes')],
        [],
        [{ node: 'n', prop: 'role', ref: { node: 'c', export: 'content', expr: '  ' } }],
      ),
      schema,
    );
    expect(codes(emptyBinding)).toContain('required_attr_missing');
  });

  it('an empty string does not satisfy a required attribute but false does', () => {
    expect(
      codes(validateGraph(doc([node('n', 'discovery.kubernetes', { role: '' })]), schema)),
    ).toContain('required_attr_missing');
    const boolAttr = Object.entries(schema.components).find(([, c]) =>
      c.attributes.some((a) => a.required && a.type === 'bool'),
    );
    if (boolAttr) {
      const [name, def] = boolAttr;
      const attr = def.attributes.find((a) => a.required && a.type === 'bool') as { name: string };
      const diags = validateGraph(doc([node('n', name, { [attr.name]: false })]), schema, {
        allowExperimental: true,
      });
      expect(
        diags.some((d) => d.code === 'required_attr_missing' && d.path?.[0] === attr.name),
      ).toBe(false);
    }
  });
});

describe('L1 dataflow rules (D1 port roles)', () => {
  it('a destination nobody feeds raises dangling_input, terminal_ok notwithstanding', () => {
    // The F5 regression: `prometheus.remote_write` is terminal_ok, and its
    // `receiver` export is where data ENTERS. Nothing supplying it is exactly
    // the signal the old rule suppressed.
    expect(schema.components['prometheus.remote_write'].terminal_ok).toBe(true);
    const diags = validateGraph(
      doc([node('n', 'prometheus.remote_write', { endpoint: [{ url: 'http://m/push' }] })]),
      schema,
    );
    const dangling = diags.find((d) => d.code === 'dangling_input');
    expect(dangling?.node_id).toBe('n');
    expect(dangling?.severity).toBe('warning');
    expect(codes(diags)).not.toContain('output_nowhere');
  });

  it('a source nobody consumes raises output_nowhere', () => {
    const diags = validateGraph(doc([node('n', 'discovery.kubernetes', { role: 'pod' })]), schema);
    const nowhere = diags.find((d) => d.code === 'output_nowhere');
    expect(nowhere?.node_id).toBe('n');
    expect(nowhere?.severity).toBe('warning');
    expect(codes(diags)).not.toContain('dangling_input');
  });

  it('terminal_ok suppresses the produces warning but never a required port', () => {
    // prometheus.exporter.self is terminal_ok and legitimately exports targets
    // nobody consumes; otelcol.exporter.prometheus is terminal_ok too but its
    // `forward_to` is a required attribute, so it still blocks.
    expect(
      codes(validateGraph(doc([node('n', 'prometheus.exporter.self')]), schema)),
    ).not.toContain('output_nowhere');
    const diags = validateGraph(doc([node('n', 'otelcol.exporter.prometheus')]), schema);
    const required = diags.find((d) => d.code === 'output_nowhere');
    expect(required?.severity).toBe('error');
    expect(required?.port).toBe('forward_to');
  });

  it('an unwired required port blocks on the side the data would move', () => {
    const diags = validateGraph(doc([node('n', 'prometheus.scrape')]), schema);
    const dangling = diags.find((d) => d.code === 'dangling_input');
    const nowhere = diags.find((d) => d.code === 'output_nowhere');
    expect([dangling?.severity, dangling?.port]).toEqual(['error', 'targets']);
    expect([nowhere?.severity, nowhere?.port]).toEqual(['error', 'forward_to']);
  });

  it('a required port can also be satisfied by a literal value', () => {
    const diags = validateGraph(
      doc([
        node('n', 'prometheus.scrape', {
          targets: [{ __address__: 'localhost:9100' }],
          forward_to: [{ $expr: 'prometheus.remote_write.sink.receiver' }],
        }),
      ]),
      schema,
    );
    expect(codes(diags)).not.toContain('dangling_input');
    expect(codes(diags)).not.toContain('output_nowhere');
  });

  it('one node reports at most one idle-produces warning, and not when partly wired', () => {
    const bare = validateGraph(
      doc([
        node('n', 'otelcol.processor.batch'),
        node('d', 'otelcol.exporter.otlp', { client: {} }),
      ]),
      schema,
    );
    // Three produces ports (metrics/logs/traces) — but they live in the
    // required `output` block, which is reported once instead.
    expect(bare.filter((d) => d.node_id === 'n' && d.code === 'output_nowhere')).toHaveLength(0);
    expect(
      bare.filter((d) => d.node_id === 'n' && d.code === 'required_block_missing'),
    ).toHaveLength(1);
    const partly = validateGraph(
      doc(
        [
          node('n', 'otelcol.processor.batch'),
          node('d', 'otelcol.exporter.otlp', { client: { endpoint: 'otel:4317' } }),
        ],
        [wire('e', 'n', 'output.metrics', 'd', 'input')],
      ),
      schema,
    );
    // Wiring metrics alone is a complete, valid pipeline: logs and traces are
    // optional, so nothing further is reported for that node's outputs.
    expect(partly.filter((d) => d.node_id === 'n' && d.code === 'output_nowhere')).toHaveLength(0);
  });

  it('a disabled node neither satisfies nor is checked', () => {
    const diags = validateGraph(
      doc(
        [
          node('n1', 'discovery.kubernetes', { role: 'pod' }),
          node('n2', 'discovery.relabel', {}, { disabled: true }),
          node('n3', 'prometheus.scrape'),
          node('n4', 'prometheus.remote_write', { endpoint: [{ url: 'http://m/push' }] }),
        ],
        [
          wire('e1', 'n1', 'targets', 'n2', 'targets'),
          wire('e2', 'n2', 'output', 'n3', 'targets'),
          wire('e3', 'n3', 'forward_to', 'n4', 'receiver'),
        ],
      ),
      schema,
    );
    expect(diags.some((d) => d.code === 'dangling_input' && d.node_id === 'n3')).toBe(true);
    expect(diags.some((d) => d.node_id === 'n2')).toBe(false);
  });
});

describe('L1 port compatibility', () => {
  it('identical wire types connect; unrelated ones do not', () => {
    expect(portsCompatible('targets', 'targets')).toBe(true);
    expect(portsCompatible('prom.metrics', 'loki.logs')).toBe(false);
    expect(portsCompatible('', '')).toBe(false);
  });

  it('otel.any is the only wildcard, and only inside the otel family', () => {
    expect(portsCompatible('otel.metrics', 'otel.any')).toBe(true);
    expect(portsCompatible('otel.any', 'otel.logs')).toBe(true);
    expect(portsCompatible('otel.metrics', 'otel.logs')).toBe(false);
    expect(portsCompatible('otel.any', 'targets')).toBe(false);
  });

  it('the refined signal types reach the graph rules', () => {
    const otlp = resolvePorts(schema.components['otelcol.receiver.otlp']);
    expect(otlp.map((p) => p.type).sort()).toEqual(['otel.logs', 'otel.metrics', 'otel.traces']);
    const diags = validateGraph(
      doc(
        [
          node('n', 'otelcol.receiver.otlp'),
          node('d', 'loki.write', { endpoint: [{ url: 'http://loki/push' }] }),
        ],
        [wire('e', 'n', 'output.logs', 'd', 'receiver')],
      ),
      schema,
    );
    // otel.logs → loki.logs is a different family, however similarly named.
    expect(codes(diags)).toContain('type_mismatch');
  });

  it('type_mismatch names both ends', () => {
    const diags = validateGraph(
      doc(
        [
          node('n1', 'discovery.kubernetes', { role: 'pod' }),
          node('n2', 'prometheus.remote_write', { endpoint: [{ url: 'http://m/push' }] }),
        ],
        [wire('e', 'n1', 'targets', 'n2', 'receiver')],
      ),
      schema,
    );
    const mismatch = diags.find((d) => d.code === 'type_mismatch');
    expect(mismatch?.node_id).toBe('n2');
    expect(mismatch?.node_id2).toBe('n1');
    expect(mismatch?.message).toContain('targets');
  });

  it('a produces port may only connect to an accepts port', () => {
    // Backwards wire: remote_write's `receiver` accepts, so nothing may leave
    // it, and scrape's `targets` accepts, so nothing may land on it from there.
    const diags = validateGraph(
      doc(
        [
          node('n2', 'prometheus.scrape', { job_name: 'a' }),
          node('n3', 'prometheus.remote_write', { endpoint: [{ url: 'http://m/push' }] }),
        ],
        [wire('e', 'n3', 'receiver', 'n2', 'targets')],
      ),
      schema,
    );
    const roleErr = diags.find((d) => d.code === 'port_role_invalid');
    expect(roleErr?.node_id).toBe('n3');
    expect(roleErr?.port).toBe('receiver');
    expect(roleErr?.severity).toBe('error');
  });

  it('canConnectPorts is the connect-time form of the same rule', () => {
    const [targets] = resolvePorts(schema.components['discovery.kubernetes']);
    const scrape = resolvePorts(schema.components['prometheus.scrape']);
    const accepts = scrape.find((p) => p.id === 'targets') as (typeof scrape)[number];
    const produces = scrape.find((p) => p.id === 'forward_to') as (typeof scrape)[number];
    expect(canConnectPorts(targets, accepts)).toBe(true);
    expect(canConnectPorts(accepts, targets)).toBe(false);
    expect(canConnectPorts(targets, produces)).toBe(false);
  });

  it('an edge naming a port the schema does not have is reported, not ignored', () => {
    const diags = validateGraph(
      doc(
        [
          node('n1', 'discovery.kubernetes', { role: 'pod' }),
          node('n2', 'prometheus.remote_write', { endpoint: [{ url: 'http://m/push' }] }),
        ],
        [wire('e', 'n1', 'p0', 'n2', 'receiver')],
      ),
      schema,
    );
    const unknown = diags.find((d) => d.code === 'unknown_port');
    expect(unknown?.node_id).toBe('n1');
    expect(unknown?.port).toBe('p0');
  });
});

describe('L1 rules that must keep working', () => {
  it('sanitizes labels', () => expect(sanitizeLabel('9 My-Node')).toBe('_9_my_node'));

  it('label collision — same component, same sanitized label', () => {
    const diags = validateGraph(
      doc([
        node('a', 'discovery.kubernetes', { role: 'pod' }, { label: 'A-B' }),
        node('b', 'discovery.kubernetes', { role: 'pod' }, { label: 'a_b' }),
      ]),
      schema,
    );
    const collision = diags.filter((d) => d.code === 'label_collision');
    expect(collision.map((d) => d.node_id).sort()).toEqual(['a', 'b']);
  });

  it('a disabled node cannot collide — it is never emitted', () => {
    const diags = validateGraph(
      doc([
        node('a', 'discovery.kubernetes', { role: 'pod' }, { label: 'same' }),
        node('b', 'discovery.kubernetes', { role: 'pod' }, { label: 'same', disabled: true }),
      ]),
      schema,
    );
    expect(codes(diags)).not.toContain('label_collision');
  });

  it('experimental components are gated unless the org toggle is on', () => {
    const experimental = Object.keys(schema.components).find(
      (n) => schema.components[n].stability === 'experimental',
    ) as string;
    expect(codes(validateGraph(doc([node('n', experimental)]), schema))).toContain(
      'experimental_gated',
    );
    expect(
      codes(validateGraph(doc([node('n', experimental)]), schema, { allowExperimental: true })),
    ).not.toContain('experimental_gated');
  });

  it('secret_by_value — top level and inside a block', () => {
    const top = validateGraph(doc([node('n', 'discovery.consul', { token: 'hunter2' })]), schema);
    expect(top.find((d) => d.code === 'secret_by_value')?.path).toEqual(['token']);
    const nested = validateGraph(
      doc([
        node('n', 'prometheus.remote_write', {
          endpoint: [{ url: 'http://m/push', bearer_token: 'hunter2' }],
        }),
      ]),
      schema,
    );
    const diag = nested.find((d) => d.code === 'secret_by_value');
    expect(diag?.path).toEqual(['endpoint', '0', 'bearer_token']);
    expect(diag?.severity).toBe('error');
  });

  it('an expression is not a secret by value', () => {
    const diags = validateGraph(
      doc([
        node('n', 'prometheus.remote_write', {
          endpoint: [{ url: 'http://m/push', bearer_token: { $expr: 'local.file.tok.content' } }],
        }),
      ]),
      schema,
    );
    expect(codes(diags)).not.toContain('secret_by_value');
  });

  it('cycle — a three-node relabel ring', () => {
    const diags = validateGraph(
      doc(
        [
          node('a', 'discovery.relabel'),
          node('b', 'discovery.relabel'),
          node('c', 'discovery.relabel'),
        ],
        [
          wire('1', 'a', 'output', 'b', 'targets'),
          wire('2', 'b', 'output', 'c', 'targets'),
          wire('3', 'c', 'output', 'a', 'targets'),
        ],
      ),
      schema,
    );
    expect(codes(diags)).toContain('cycle');
  });

  it('cycle — two nodes referring to each other', () => {
    const diags = validateGraph(
      doc(
        [node('a', 'discovery.relabel'), node('b', 'discovery.relabel')],
        [wire('1', 'a', 'output', 'b', 'targets'), wire('2', 'b', 'output', 'a', 'targets')],
      ),
      schema,
    );
    expect(codes(diags)).toContain('cycle');
  });

  it('no_destination — and a disabled destination does not count', () => {
    expect(
      codes(validateGraph(doc([node('a', 'discovery.kubernetes', { role: 'pod' })]), schema)),
    ).toContain('no_destination');
    expect(
      codes(
        validateGraph(doc([node('a', 'prometheus.remote_write', {}, { disabled: true })]), schema),
      ),
    ).toContain('no_destination');
    expect(codes(validateGraph(referencePipeline(), schema))).not.toContain('no_destination');
    expect(codes(validateGraph(doc([]), schema))).not.toContain('no_destination');
  });
});

describe('L1 degrades gracefully (D4)', () => {
  /** A payload in the pre-D1 shape: no roles, no port paths, a scalar port. */
  const legacy = {
    _meta: { alloy_version: 'legacy', components_total: 2 },
    wire_types: schema.wire_types,
    components: {
      'legacy.source': {
        stability: 'ga',
        doc: '',
        category: 'sources',
        attributes: [],
        blocks: [],
        inputs: [],
        outputs: [{ export: 'targets', type: 'targets' }],
        default_snippet: '',
      },
      'legacy.sink': {
        stability: 'ga',
        doc: '',
        category: 'destinations',
        attributes: [],
        blocks: [],
        inputs: [{ prop: 'targets', type: 'targets', cardinality: 'scalar' }],
        outputs: [],
        default_snippet: '',
      },
      // Accepts targets and re-exports them under the same name — the shape
      // `database_observability.*` has in the real schema.
      'legacy.dual': {
        stability: 'ga',
        doc: '',
        category: 'transform',
        attributes: [{ name: 'targets', type: 'list', required: true }],
        blocks: [],
        inputs: [{ prop: 'targets', type: 'targets', cardinality: 'list', role: 'accepts' }],
        outputs: [{ export: 'targets', type: 'targets', role: 'produces' }],
        default_snippet: '',
      },
    },
  } as unknown as SchemaPayload;

  it('a schema without port roles still validates with the old convention', () => {
    const diags = validateGraph(
      doc(
        [node('a', 'legacy.source'), node('b', 'legacy.sink')],
        [wire('e', 'a', 'targets', 'b', 'targets')],
      ),
      legacy,
    );
    expect(diags).toEqual([]);
  });

  it('scalar_input_multi_wire — a port that takes one value with two wires', () => {
    const diags = validateGraph(
      doc(
        [node('a', 'legacy.source'), node('b', 'legacy.source'), node('c', 'legacy.sink')],
        [wire('e1', 'a', 'targets', 'c', 'targets'), wire('e2', 'b', 'targets', 'c', 'targets')],
      ),
      legacy,
    );
    const scalar = diags.find((d) => d.code === 'scalar_input_multi_wire');
    expect(scalar?.node_id).toBe('c');
    expect(scalar?.severity).toBe('warning');
  });

  it('one port name used for both roles is judged twice, once per role', () => {
    const diags = validateGraph(doc([node('n', 'legacy.dual')]), legacy);
    // The required argument is unset and unwired: an error on the accepts side.
    const accepts = diags.find((d) => d.code === 'dangling_input');
    expect([accepts?.severity, accepts?.port]).toEqual(['error', 'targets']);
    // The export of the same name is a separate port, and nothing consumes it.
    const produces = diags.find((d) => d.code === 'output_nowhere');
    expect([produces?.severity, produces?.port]).toEqual(['warning', 'targets']);
  });

  it('an export is never satisfied by a prop of the same name — only by a wire', () => {
    const diags = validateGraph(
      doc([node('n', 'legacy.dual', { targets: [{ __address__: 'localhost:9100' }] })]),
      legacy,
    );
    expect(codes(diags)).not.toContain('dangling_input');
    expect(codes(diags)).toContain('output_nowhere');
  });

  it('a component the schema does not describe is skipped, not crashed on', () => {
    expect(() =>
      validateGraph(
        doc(
          [node('a', 'not.a.component'), node('b', 'prometheus.remote_write')],
          [wire('e', 'a', 'x', 'b', 'receiver')],
        ),
        schema,
      ),
    ).not.toThrow();
  });

  it('an edge naming a node that no longer exists is ignored', () => {
    expect(() =>
      validateGraph(
        doc(
          [node('a', 'discovery.kubernetes', { role: 'pod' })],
          [wire('e', 'a', 'targets', 'gone', 'targets')],
        ),
        schema,
      ),
    ).not.toThrow();
  });
});
