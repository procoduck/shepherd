import type {
  AgentToken,
  Cluster,
  Collector,
  Destination,
  Org,
  Pipeline,
  PipelineRevision,
} from '../../src/api/client';

// Sequential ID generator
const counters: Record<string, number> = {};
function seq(prefix: string): string {
  counters[prefix] = (counters[prefix] ?? 0) + 1;
  return `${prefix}-${String(counters[prefix]).padStart(4, '0')}`;
}
export function resetSeq() {
  Object.keys(counters).forEach((k) => delete counters[k]);
}

export const org = (o: Partial<Org> = {}): Org => ({
  id: seq('org'),
  name: 'prod-org',
  display_name: 'Production Org',
  admin_group_id: '11111111-1111-1111-1111-111111111111',
  reader_group_id: '',
  created_at: '2026-08-17T09:00:00Z',
  updated_at: '2026-08-17T09:00:00Z',
  ...o,
});

export const cluster = (o: Partial<Cluster> = {}): Cluster => ({
  id: seq('cl'),
  name: 'prod-eu-1',
  org_id: 'org-0001',
  created_at: '2026-08-17T09:00:00Z',
  ...o,
});

export const collector = (o: Partial<Collector> = {}): Collector => ({
  id: seq('col'),
  cluster: 'prod-eu-1',
  role: 'metrics',
  ...o,
});

export const pipeline = (o: Partial<Pipeline> = {}): Pipeline => ({
  id: seq('pip'),
  name: 'my-pipeline',
  contents: `prometheus.exporter.self "e2e" { }`,
  matchers: [`cluster="prod-eu-1"`],
  enabled: true,
  source: 'ui',
  created_by: 'test@example.com',
  updated_by: 'test@example.com',
  created_at: '2026-08-17T09:00:00Z',
  updated_at: '2026-08-17T09:00:00Z',
  ...o,
});

export const revision = (o: Partial<PipelineRevision> = {}): PipelineRevision => ({
  id: seq('rev'),
  pipeline_id: 'pip-0001',
  revision: 1,
  contents: `// revision 1`,
  matchers: [],
  enabled: false,
  changed_by: 'test@example.com',
  changed_at: '2026-08-17T09:00:00Z',
  change_note: '',
  ...o,
});

export const destination = (o: Partial<Destination> = {}): Destination => ({
  id: seq('dst'),
  name: 'prom-prod',
  type: 'prometheus',
  url: 'https://prometheus.example.com/api/v1/write',
  tenant_id: '',
  secret_name: 'prom-secret',
  secret_namespace: 'monitoring',
  auth_mode: 'none',
  created_at: '2026-08-17T09:00:00Z',
  updated_at: '2026-08-17T09:00:00Z',
  ...o,
});

export const agentToken = (o: Partial<AgentToken> = {}): AgentToken => ({
  id: seq('tok'),
  name: 'my-token',
  created_by: 'admin',
  status: 'active',
  created_at: '2026-08-17T09:00:00Z',
  ...o,
});

export const diagnostic = (
  o: Partial<{ line: number; col: number; message: string; stage: number }> = {},
) => ({
  line: 3,
  col: 5,
  message: 'unexpected token',
  stage: 1,
  ...o,
});

// A coherent state: 1 org, 4 collectors, 3 pipelines, 2 destinations
export const basicScenario = () => {
  resetSeq();
  const o = org({ id: 'org-0001', name: 'prod-org' });
  const cols = [
    collector({ id: 'col-0001', role: 'metrics' }),
    collector({ id: 'col-0002', role: 'logs' }),
    collector({ id: 'col-0003', role: 'singleton' }),
    collector({ id: 'col-0004', role: 'receiver' }),
  ];
  const pipes = [
    pipeline({ id: 'pip-0001', name: 'ui-enabled', source: 'ui', enabled: true }),
    pipeline({ id: 'pip-0002', name: 'wizard-disabled', source: 'wizard', enabled: false }),
    pipeline({ id: 'pip-0003', name: 'git-pipe', source: 'git', enabled: true }),
  ];
  const dests = [
    destination({ id: 'dst-0001', name: 'prom-prod' }),
    destination({ id: 'dst-0002', name: 'loki-prod', type: 'loki' }),
  ];
  return { org: o, collectors: cols, pipelines: pipes, destinations: dests };
};
