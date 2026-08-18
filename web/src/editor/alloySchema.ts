// alloySchema.ts — hand-curated Alloy component schema for autocomplete.
// Source: https://grafana.com/docs/alloy/latest/reference/components/
// See docs/spec.md §13.6 and §D.4 for maintenance guidance.

export interface AttrDef {
  name: string;
  type: 'string' | 'bool' | 'list' | 'map' | 'number';
  required: boolean;
  doc?: string;
  values?: string[]; // enum values for string attributes
}

export interface BlockDef {
  name: string;
  repeatable?: boolean;
  attributes?: AttrDef[];
  blocks?: BlockDef[];
}

export interface ComponentDef {
  doc: string;
  hasLabel: boolean;
  exports: string[];
  attributes?: AttrDef[];
  blocks?: BlockDef[];
}

export type AlloySchema = Record<string, ComponentDef>;

export const alloySchema: AlloySchema = {
  'prometheus.remote_write': {
    doc: 'Send metrics to a Prometheus-compatible remote_write endpoint.',
    hasLabel: true,
    exports: ['receiver'],
    attributes: [
      {
        name: 'external_labels',
        type: 'map',
        required: false,
        doc: 'Labels added to all metrics.',
      },
    ],
    blocks: [
      {
        name: 'endpoint',
        repeatable: true,
        attributes: [
          { name: 'url', type: 'string', required: true, doc: 'Remote write URL.' },
          { name: 'headers', type: 'map', required: false },
          { name: 'name', type: 'string', required: false },
        ],
        blocks: [
          {
            name: 'basic_auth',
            attributes: [
              { name: 'username', type: 'string', required: true },
              { name: 'password', type: 'string', required: true },
            ],
          },
          {
            name: 'oauth2',
            attributes: [
              { name: 'client_id', type: 'string', required: true },
              { name: 'client_secret', type: 'string', required: false },
            ],
          },
          {
            name: 'tls_config',
            attributes: [{ name: 'insecure_skip_verify', type: 'bool', required: false }],
          },
          {
            name: 'write_relabel_config',
            repeatable: true,
            attributes: [
              { name: 'source_labels', type: 'list', required: false },
              { name: 'regex', type: 'string', required: false },
              { name: 'target_label', type: 'string', required: false },
              {
                name: 'action',
                type: 'string',
                required: false,
                values: [
                  'keep',
                  'drop',
                  'replace',
                  'labelmap',
                  'labeldrop',
                  'labelkeep',
                  'hashmod',
                  'lowercase',
                  'uppercase',
                ],
              },
            ],
          },
        ],
      },
    ],
  },
  'prometheus.scrape': {
    doc: 'Scrape Prometheus metrics from targets.',
    hasLabel: true,
    exports: [],
    attributes: [
      { name: 'targets', type: 'list', required: true, doc: 'List of targets to scrape.' },
      {
        name: 'forward_to',
        type: 'list',
        required: true,
        doc: 'List of receivers to forward metrics to.',
      },
      {
        name: 'scrape_interval',
        type: 'string',
        required: false,
        doc: 'Scrape interval (e.g. "60s").',
      },
      { name: 'scrape_timeout', type: 'string', required: false },
      { name: 'job_name', type: 'string', required: false },
      { name: 'metrics_path', type: 'string', required: false },
      { name: 'scheme', type: 'string', required: false, values: ['http', 'https'] },
    ],
  },
  'prometheus.relabel': {
    doc: 'Relabel and filter Prometheus metrics.',
    hasLabel: true,
    exports: ['receiver', 'output'],
    attributes: [{ name: 'forward_to', type: 'list', required: true }],
    blocks: [
      {
        name: 'rule',
        repeatable: true,
        attributes: [
          { name: 'source_labels', type: 'list', required: false },
          { name: 'regex', type: 'string', required: false },
          { name: 'target_label', type: 'string', required: false },
          { name: 'replacement', type: 'string', required: false },
          {
            name: 'action',
            type: 'string',
            required: false,
            values: [
              'keep',
              'drop',
              'replace',
              'labelmap',
              'labeldrop',
              'labelkeep',
              'hashmod',
              'lowercase',
              'uppercase',
            ],
          },
        ],
      },
    ],
  },
  'prometheus.exporter.self': {
    doc: 'Expose Alloy internal metrics.',
    hasLabel: true,
    exports: ['targets'],
    attributes: [],
  },
  'discovery.kubernetes': {
    doc: 'Discover targets from Kubernetes.',
    hasLabel: true,
    exports: ['targets'],
    attributes: [
      {
        name: 'role',
        type: 'string',
        required: true,
        values: ['node', 'pod', 'service', 'endpoints', 'ingress', 'endpointslice'],
      },
      { name: 'api_server', type: 'string', required: false },
    ],
    blocks: [
      { name: 'namespaces', attributes: [{ name: 'names', type: 'list', required: false }] },
      {
        name: 'selectors',
        repeatable: true,
        attributes: [
          { name: 'role', type: 'string', required: true },
          { name: 'label', type: 'string', required: false },
          { name: 'field', type: 'string', required: false },
        ],
      },
    ],
  },
  'discovery.relabel': {
    doc: 'Rewrite label sets of targets.',
    hasLabel: true,
    exports: ['output', 'rules'],
    attributes: [{ name: 'targets', type: 'list', required: true }],
    blocks: [
      {
        name: 'rule',
        repeatable: true,
        attributes: [
          { name: 'source_labels', type: 'list', required: false },
          { name: 'regex', type: 'string', required: false },
          { name: 'target_label', type: 'string', required: false },
          { name: 'replacement', type: 'string', required: false },
          {
            name: 'action',
            type: 'string',
            required: false,
            values: [
              'keep',
              'drop',
              'replace',
              'labelmap',
              'labeldrop',
              'labelkeep',
              'hashmod',
              'lowercase',
              'uppercase',
            ],
          },
        ],
      },
    ],
  },
  'loki.source.kubernetes': {
    doc: 'Collect logs from Kubernetes pods.',
    hasLabel: true,
    exports: [],
    attributes: [
      { name: 'targets', type: 'list', required: true },
      { name: 'forward_to', type: 'list', required: true },
    ],
  },
  'loki.source.file': {
    doc: 'Tail log files.',
    hasLabel: true,
    exports: [],
    attributes: [
      { name: 'targets', type: 'list', required: true },
      { name: 'forward_to', type: 'list', required: true },
    ],
  },
  'loki.process': {
    doc: 'Process and transform log entries.',
    hasLabel: true,
    exports: ['receiver'],
    attributes: [{ name: 'forward_to', type: 'list', required: true }],
    blocks: [
      { name: 'stage.json', attributes: [{ name: 'expressions', type: 'map', required: false }] },
      { name: 'stage.labels', attributes: [{ name: 'values', type: 'map', required: false }] },
      {
        name: 'stage.template',
        attributes: [
          { name: 'source', type: 'string', required: true },
          { name: 'template', type: 'string', required: true },
        ],
      },
      { name: 'stage.label_drop', attributes: [{ name: 'values', type: 'list', required: true }] },
      {
        name: 'stage.multiline',
        attributes: [
          { name: 'firstline', type: 'string', required: true },
          { name: 'max_wait_time', type: 'string', required: false },
          { name: 'max_lines', type: 'number', required: false },
        ],
      },
    ],
  },
  'loki.write': {
    doc: 'Send log entries to a Loki instance.',
    hasLabel: true,
    exports: ['receiver'],
    attributes: [],
    blocks: [
      {
        name: 'endpoint',
        attributes: [
          { name: 'url', type: 'string', required: true },
          { name: 'tenant_id', type: 'string', required: false },
        ],
      },
    ],
  },
  'otelcol.receiver.otlp': {
    doc: 'Receive OpenTelemetry data over OTLP.',
    hasLabel: true,
    exports: ['output'],
    attributes: [],
    blocks: [
      { name: 'grpc', attributes: [{ name: 'endpoint', type: 'string', required: false }] },
      { name: 'http', attributes: [{ name: 'endpoint', type: 'string', required: false }] },
      {
        name: 'output',
        attributes: [
          { name: 'metrics', type: 'list', required: false },
          { name: 'logs', type: 'list', required: false },
          { name: 'traces', type: 'list', required: false },
        ],
      },
    ],
  },
  'otelcol.processor.batch': {
    doc: 'Batch OpenTelemetry data before forwarding.',
    hasLabel: true,
    exports: ['input'],
    attributes: [
      { name: 'timeout', type: 'string', required: false },
      { name: 'send_batch_size', type: 'number', required: false },
    ],
    blocks: [
      {
        name: 'output',
        attributes: [
          { name: 'metrics', type: 'list', required: false },
          { name: 'logs', type: 'list', required: false },
          { name: 'traces', type: 'list', required: false },
        ],
      },
    ],
  },
  'otelcol.exporter.otlp': {
    doc: 'Export OpenTelemetry data via OTLP/gRPC.',
    hasLabel: true,
    exports: ['input'],
    attributes: [{ name: 'client', type: 'map', required: false }],
    blocks: [
      { name: 'client', attributes: [{ name: 'endpoint', type: 'string', required: true }] },
    ],
  },
  'otelcol.exporter.otlphttp': {
    doc: 'Export OpenTelemetry data via OTLP/HTTP.',
    hasLabel: true,
    exports: ['input'],
    attributes: [],
    blocks: [
      { name: 'client', attributes: [{ name: 'endpoint', type: 'string', required: true }] },
    ],
  },
  'remote.kubernetes.secret': {
    doc: 'Read a Kubernetes secret at runtime.',
    hasLabel: true,
    exports: ['data'],
    attributes: [
      { name: 'name', type: 'string', required: true, doc: 'Secret name.' },
      { name: 'namespace', type: 'string', required: true, doc: 'Secret namespace.' },
    ],
  },
  'local.file': {
    doc: 'Read a local file.',
    hasLabel: true,
    exports: ['content'],
    attributes: [
      { name: 'filename', type: 'string', required: true },
      { name: 'detector', type: 'string', required: false, values: ['fsnotify', 'poll'] },
      { name: 'poll_frequency', type: 'string', required: false },
      { name: 'is_secret', type: 'bool', required: false },
    ],
  },
  declare: {
    doc: 'Declare a reusable pipeline block.',
    hasLabel: true,
    exports: [],
    attributes: [],
  },
};

// Drift guard: list of all components required by spec §13.6.
// A Vitest test asserts every entry is present in alloySchema.
export const specRequiredComponents = [
  'discovery.kubernetes',
  'discovery.relabel',
  'prometheus.scrape',
  'prometheus.relabel',
  'prometheus.remote_write',
  'prometheus.exporter.self',
  'loki.source.kubernetes',
  'loki.process',
  'loki.write',
  'otelcol.receiver.otlp',
  'otelcol.processor.batch',
  'otelcol.exporter.otlp',
  'otelcol.exporter.otlphttp',
  'remote.kubernetes.secret',
  'local.file',
  'declare',
] as const;
