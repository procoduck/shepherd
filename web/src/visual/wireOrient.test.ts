import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import type { GraphDocument, GraphNode, SchemaPayload } from './types';
import { orientConnection, rfEndpointsForEdge } from './wireOrient';

/**
 * B1 (docs/reviews/README.md): CanvasPane's onConnect stored React Flow's
 * source->target verbatim, which is backwards for every receiver-kind wire
 * (prometheus.remote_write.receiver, otelcol.*.input, ...) — this is what a
 * live dev-stack drag from `prometheus.scrape.forward_to` to
 * `prometheus.remote_write.receiver` measured: the stored edge ran
 * accepts->produces, `port_role_invalid` fired, and the renderer silently
 * dropped `forward_to`. These tests run against the SHIPPED schema, same as
 * l1.test.ts, for the same reason: a hand-written fixture is exactly what let
 * this regression through undetected the first time.
 */
const artifactsDir = join(__dirname, '../../../internal/schema/artifacts');
const readJson = (file: string) => JSON.parse(readFileSync(join(artifactsDir, file), 'utf-8'));
const isObj = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);
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

const node = (id: string, component: string): GraphNode => ({
  id,
  component,
  label: id,
  position: { x: 0, y: 0 },
  props: {},
  disabled: false,
  notes: '',
});
const doc = (nodes: GraphNode[]): Pick<GraphDocument, 'nodes'> => ({ nodes });

describe('orientConnection (B1)', () => {
  it('stores a receiver-kind wire produces->accepts when RF drags export->argument', () => {
    // React Flow's fixed classification: remote_write.receiver is an export
    // (RF `source` handle); scrape.forward_to is an argument (RF `target`
    // handle) — dragging from the receiver TO forward_to is the only gesture
    // RF's strict connectionMode allows for this pair.
    const nodes = [node('scrape', 'prometheus.scrape'), node('rw', 'prometheus.remote_write')];
    const oriented = orientConnection(schema, doc(nodes), {
      source: 'rw',
      sourceHandle: 'receiver',
      target: 'scrape',
      targetHandle: 'forward_to',
    });
    expect(oriented).not.toBeNull();
    expect(oriented?.from).toEqual({ node: 'scrape', port: 'forward_to' });
    expect(oriented?.to).toEqual({ node: 'rw', port: 'receiver' });
  });

  it('stores a data wire produces->accepts unchanged when RF drags export->argument', () => {
    // discovery.kubernetes.targets is a data export (role "produces");
    // scrape.targets is the ordinary consumer argument (role "accepts") — RF's
    // source->target already matches dataflow direction here, so orientation
    // must be a no-op.
    const nodes = [node('k8s', 'discovery.kubernetes'), node('scrape', 'prometheus.scrape')];
    const oriented = orientConnection(schema, doc(nodes), {
      source: 'k8s',
      sourceHandle: 'targets',
      target: 'scrape',
      targetHandle: 'targets',
    });
    expect(oriented).not.toBeNull();
    expect(oriented?.from).toEqual({ node: 'k8s', port: 'targets' });
    expect(oriented?.to).toEqual({ node: 'scrape', port: 'targets' });
  });

  it('orients the otel receiver-kind hop the same way', () => {
    // otelcol.receiver.otlp's `output.metrics` is a schema ARGUMENT (an
    // otel.Consumer reference list, like forward_to — RF `target` handle,
    // role "produces"); otelcol.processor.batch's `input` is the schema
    // EXPORT it's pointed at (RF `source` handle, role "accepts"). RF's
    // strict connectionMode therefore only allows dragging FROM batch.input
    // TO otlp.output.metrics — the reverse of the dataflow arrow (otlp emits,
    // batch receives) the canvas draws, exactly like the remote_write case.
    const nodes = [node('otlp', 'otelcol.receiver.otlp'), node('batch', 'otelcol.processor.batch')];
    const oriented = orientConnection(schema, doc(nodes), {
      source: 'batch',
      sourceHandle: 'input',
      target: 'otlp',
      targetHandle: 'output.metrics',
    });
    expect(oriented).not.toBeNull();
    expect(oriented?.from).toEqual({ node: 'otlp', port: 'output.metrics' });
    expect(oriented?.to).toEqual({ node: 'batch', port: 'input' });
  });

  it('rejects a self-connection', () => {
    const nodes = [node('scrape', 'prometheus.scrape')];
    expect(
      orientConnection(schema, doc(nodes), {
        source: 'scrape',
        sourceHandle: 'forward_to',
        target: 'scrape',
        targetHandle: 'forward_to',
      }),
    ).toBeNull();
  });

  it('rejects two ports that resolve to the same role', () => {
    // Both ends are `receiver` exports (role "accepts" under D1) — with no
    // "produces" side at all, there is no valid pairing in either direction.
    const nodes = [node('rw1', 'prometheus.remote_write'), node('rw2', 'prometheus.remote_write')];
    expect(
      orientConnection(schema, doc(nodes), {
        source: 'rw1',
        sourceHandle: 'receiver',
        target: 'rw2',
        targetHandle: 'receiver',
      }),
    ).toBeNull();
  });

  it('rejects an unknown handle id', () => {
    const nodes = [node('scrape', 'prometheus.scrape'), node('rw', 'prometheus.remote_write')];
    expect(
      orientConnection(schema, doc(nodes), {
        source: 'rw',
        sourceHandle: 'not_a_real_port',
        target: 'scrape',
        targetHandle: 'forward_to',
      }),
    ).toBeNull();
  });

  it('returns null without a schema', () => {
    const nodes = [node('scrape', 'prometheus.scrape'), node('rw', 'prometheus.remote_write')];
    expect(
      orientConnection(null, doc(nodes), {
        source: 'rw',
        sourceHandle: 'receiver',
        target: 'scrape',
        targetHandle: 'forward_to',
      }),
    ).toBeNull();
  });
});

describe('rfEndpointsForEdge (B1, the render-side half of the fix)', () => {
  it('swaps a receiver-kind stored edge back for RF: source must be the export', () => {
    // The stored GraphEdge is role-oriented (forward_to "produces" ->
    // receiver "accepts"), but React Flow hard-requires `source`+
    // `sourceHandle` to name a `source`-type handle (the export, `receiver`)
    // and `target`+`targetHandle` a `target`-type handle (the argument,
    // `forward_to`) — the opposite of the stored order. Handing RF the
    // stored order verbatim resolves no handle and the edge silently never
    // renders, which is exactly what a live drag measured (Edges: 2 in the
    // graph document, only 1 `.react-flow__edge` in the DOM).
    const nodes = [node('scrape', 'prometheus.scrape'), node('rw', 'prometheus.remote_write')];
    const rf = rfEndpointsForEdge(schema, doc(nodes), {
      from: { node: 'scrape', port: 'forward_to' },
      to: { node: 'rw', port: 'receiver' },
    });
    expect(rf).toEqual({
      source: 'rw',
      sourceHandle: 'receiver',
      target: 'scrape',
      targetHandle: 'forward_to',
    });
  });

  it('leaves a data-kind stored edge unchanged for RF: from already is the export', () => {
    const nodes = [node('k8s', 'discovery.kubernetes'), node('scrape', 'prometheus.scrape')];
    const rf = rfEndpointsForEdge(schema, doc(nodes), {
      from: { node: 'k8s', port: 'targets' },
      to: { node: 'scrape', port: 'targets' },
    });
    expect(rf).toEqual({
      source: 'k8s',
      sourceHandle: 'targets',
      target: 'scrape',
      targetHandle: 'targets',
    });
  });

  it('round-trips through orientConnection back to the original RF gesture', () => {
    // Whatever RF connection gesture produced a stored edge, converting that
    // edge back to RF endpoints must reproduce the SAME gesture — otherwise
    // the wire the user just drew disappears the instant it's drawn.
    const nodes = [node('scrape', 'prometheus.scrape'), node('rw', 'prometheus.remote_write')];
    const gesture = {
      source: 'rw',
      sourceHandle: 'receiver',
      target: 'scrape',
      targetHandle: 'forward_to',
    };
    const oriented = orientConnection(schema, doc(nodes), gesture);
    expect(oriented).not.toBeNull();
    const rf = rfEndpointsForEdge(schema, doc(nodes), oriented!);
    expect(rf).toEqual(gesture);
  });
});
