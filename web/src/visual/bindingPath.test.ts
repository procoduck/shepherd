import { describe, expect, it } from 'vitest';
import { shippedSchema } from '../../tests/fixtures/schema-fixture';
import {
  bindingMatchesPath,
  bindingPropOf,
  formatBindingPath,
  isNestedBindingProp,
  overlayBindings,
  parseBindingPath,
  resolveBindingPath,
} from './bindingPath';
import { validateGraph } from './l1';
import { renderTS } from './renderTS';
import type { GraphDocument } from './types';

// The TS twin of internal/visual/bindingpath_test.go (#426), against the
// shipped schema artifact + overlay, the same payload renderTS.test.ts uses.
const schema = shippedSchema;
const comp = (name: string) => schema.components[name];

describe('parseBindingPath', () => {
  it('splits names and indices', () => {
    expect(parseBindingPath('endpoint[1].basic_auth.password')).toEqual({
      ok: true,
      value: [
        { name: 'endpoint', index: 1, indexed: true },
        { name: 'basic_auth', index: 0, indexed: false },
        { name: 'password', index: 0, indexed: false },
      ],
    });
  });

  it('tells a top-level prop from a nested path', () => {
    expect(isNestedBindingProp('bearer_token')).toBe(false);
    expect(isNestedBindingProp('endpoint[0].url')).toBe(true);
    expect(isNestedBindingProp('client.endpoint')).toBe(true);
  });

  it.each([
    '',
    '.url',
    'endpoint.',
    'endpoint..url',
    'endpoint[.url',
    'endpoint[x].url',
    'endpoint[-1].url',
    'endpoint[0].url[0]',
    'end-point.url',
    'endpoint[01].url',
  ])('refuses %j', (prop) => {
    expect(parseBindingPath(prop).ok).toBe(false);
  });
});

describe('resolveBindingPath', () => {
  it('resolves a dotted block name by the schema', () => {
    const r = resolveBindingPath(comp('loki.process'), 'stage.tenant[0].value');
    expect(r.ok && formatBindingPath(r.value.steps, r.value.attr.name)).toBe(
      'stage.tenant[0].value',
    );
  });

  it('reads an omitted index on a repeatable block as instance 0', () => {
    const r = resolveBindingPath(comp('prometheus.remote_write'), 'endpoint.basic_auth.password');
    expect(r.ok && formatBindingPath(r.value.steps, r.value.attr.name)).toBe(
      'endpoint[0].basic_auth.password',
    );
  });

  it.each([
    ['prometheus.remote_write', 'nowhere[0].url'],
    ['prometheus.remote_write', 'endpoint[0].nope'],
    ['prometheus.remote_write', 'endpoint[0].basic_auth'],
    ['otelcol.exporter.otlp', 'client[1].endpoint'],
    ['loki.process', 'stage[0].tenant.value'],
  ])('refuses %s %s', (name, prop) => {
    expect(resolveBindingPath(comp(name), prop).ok).toBe(false);
  });
});

describe('inspector instance paths', () => {
  it('fold each index onto its block name', () => {
    expect(bindingPropOf(['endpoint', '0', 'basic_auth', 'password'])).toBe(
      'endpoint[0].basic_auth.password',
    );
    expect(bindingPropOf(['client', 'tls', 'ca_pem'])).toBe('client.tls.ca_pem');
    expect(bindingPropOf(['bearer_token'])).toBe('bearer_token');
  });

  it('match a canonical prop, or an index-free one only at instance 0', () => {
    expect(bindingMatchesPath('endpoint[1].url', ['endpoint', '1', 'url'])).toBe(true);
    expect(bindingMatchesPath('endpoint.url', ['endpoint', '0', 'url'])).toBe(true);
    expect(bindingMatchesPath('endpoint.url', ['endpoint', '1', 'url'])).toBe(false);
    expect(bindingMatchesPath('endpoint[0].url', ['endpoint', '1', 'url'])).toBe(false);
  });
});

describe('overlayBindings', () => {
  const bind = (prop: string, expr = 'local.file.f.content') => ({
    node: 'n',
    prop,
    ref: { node: 'f', export: 'content', expr },
  });

  it('places nested bindings without touching the original props', () => {
    const props = { endpoint: [{ url: 'https://a' }, { url: 'https://b' }] };
    const before = JSON.stringify(props);
    const r = overlayBindings(comp('prometheus.remote_write'), props, [
      bind('endpoint[1].bearer_token'),
      bind('headers'),
    ]);
    expect(JSON.stringify(props)).toBe(before);
    expect(r.issues).toEqual([]);
    expect(r.top.map((b) => b.prop)).toEqual(['headers']);
    expect(r.props).toEqual({
      endpoint: [
        { url: 'https://a' },
        { url: 'https://b', bearer_token: { $expr: 'local.file.f.content' } },
      ],
    });
  });

  it('reports what it cannot place, and a binding that overrides a literal', () => {
    const r = overlayBindings(comp('otelcol.exporter.otlp'), { client: { endpoint: 'x:4317' } }, [
      bind('client.endpoint'),
      bind('nowhere.endpoint'),
      bind('client.tls.ca_pem', '  '),
    ]);
    expect(r.issues.map((i) => i.code)).toEqual([
      'binding_overrides_prop',
      'binding_path_invalid',
      'empty_binding_expr',
    ]);
  });
});

describe('a nested binding through L1 and renderTS', () => {
  const doc: GraphDocument = {
    kind: 'alloy-graph/v1',
    schema_version: 'alloy-v1.21.2',
    nodes: [
      {
        id: 'f',
        component: 'local.file',
        label: 'ep',
        position: { x: 0, y: 0 },
        props: { filename: '/etc/ep' },
        disabled: false,
        notes: '',
      },
      {
        id: 'o',
        component: 'otelcol.exporter.otlp',
        label: 'out',
        position: { x: 0, y: 0 },
        props: {},
        disabled: false,
        notes: '',
      },
    ],
    edges: [],
    bindings: [
      {
        node: 'o',
        prop: 'client.endpoint',
        ref: { node: 'f', export: 'content', expr: 'local.file.ep.content' },
      },
    ],
    viewport: { x: 0, y: 0, zoom: 1 },
    meta: { created_with: 'test' },
  };

  it('satisfies the required block and attribute it fills', () => {
    const codes = validateGraph(doc, schema)
      .filter((d) => d.node_id === 'o')
      .map((d) => d.code);
    expect(codes).not.toContain('required_block_missing');
    expect(codes).not.toContain('required_attr_missing');
    const unbound = validateGraph({ ...doc, bindings: [] }, schema)
      .filter((d) => d.node_id === 'o')
      .map((d) => d.code);
    expect(unbound).toContain('required_block_missing');
  });

  it('renders inside the block', () => {
    expect(renderTS(doc, schema).content).toContain(
      '  client {\n    endpoint = local.file.ep.content\n  }\n',
    );
  });
});
