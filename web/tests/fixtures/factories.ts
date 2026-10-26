// These fixture shapes intentionally stay snake_case — the pre-migration
// REST/domain shape — independent of the generated (camelCase) Connect
// types. tests/mocks/handlers.ts converts fixture -> wire; see the
// "Wire-shape converters" section there for the field mapping.
interface Org {
  id: string;
  name: string;
  display_name: string;
  admin_group_id: string;
  viewer_group_id?: string;
  // The tenants and prefixes the org owns (org_tenants, D11); the mock
  // converts them to Org.tenants and GetMe's membership (#261).
  tenants?: { tenant: string; kind: 'exact' | 'prefix'; is_default?: boolean }[];
  // Strict tenant mode (0040) and, for the mock's SetOrgStrictTenants and
  // TenantPreflight, what the server's preflight would find.
  strict_tenants?: boolean;
  strict_violations?: { kind: string; name: string; tenant?: string; message: string }[];
  created_at: string;
  updated_at: string;
}
interface Cluster {
  id: string;
  name: string;
  org_id?: string | null;
  created_at: string;
}
interface Collector {
  id: string;
  cluster_id?: string;
  cluster: string;
  role: string;
  org_id?: string;
  remote_config_status?: string;
  remote_config_error?: string;
  last_seen?: string;
  alloy_version?: string;
  local_attributes?: Record<string, string>;
  labels?: Record<string, string>;
  instances?: unknown[];
  /** 0041: the latest refusal while newer than the last successful poll. */
  last_refused_reason?: 'token_unbound' | 'claim_denied' | 'role_denied';
  last_refused_at?: string;
  /** 0044 (#375): the served config's version check, in wire (camelCase) form. */
  version_warning?: { findings: string[]; mismatch: boolean; since?: string };
}
interface Pipeline {
  id: string;
  org_id?: string;
  name: string;
  contents: string;
  matchers: string[];
  enabled: boolean;
  source: string;
  created_by: string;
  updated_by: string;
  created_at: string;
  updated_at: string;
  revisions?: PipelineRevision[];
  /** The owning team's id; absent/empty means unowned. */
  owner_team_id?: string;
  /** Wizard form state; a wizard names its destinations in `<signal>_dest_name` keys. */
  wizard_state?: Record<string, unknown>;
  /**
   * #301: false marks a wizard pipeline hand-edited since its wizard last
   * wrote it. Absent means a wizard pipeline still follows its wizard.
   */
  follows_wizard?: boolean;
}
interface PipelineRevision {
  id?: string;
  pipeline_id?: string;
  revision: number;
  contents?: string;
  matchers?: string[];
  enabled?: boolean;
  changed_by: string;
  changed_at: string;
  change_note: string;
}
interface Destination {
  id: string;
  org_id?: string;
  name: string;
  type: string;
  url: string;
  tenant_id?: string;
  secret_name?: string;
  secret_namespace?: string;
  auth_mode: string;
  created_at: string;
  updated_at: string;
}
interface AgentToken {
  id: string;
  name: string;
  created_by: string;
  status: string;
  created_at: string;
  /** '' = unbound (a token from before tokens were org-bound). */
  org_id: string;
  /** Used when org_id names no seeded org. */
  org_name?: string;
  clusters: string[];
  roles: string[];
}

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
  viewer_group_id: '',
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
  org_id: 'org-0001',
  org_name: 'prod-org',
  clusters: [],
  roles: [],
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
