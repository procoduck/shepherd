// Typed API client for the Shepherd management REST API.

export interface ListResponse<T> {
  items: T[];
  total: number;
}

export interface ApiError {
  code: string;
  message: string;
  details?: unknown[];
}

export async function getSchema(
  version = 'current',
): Promise<import('@/visual/types').SchemaPayload> {
  return request<import('@/visual/types').SchemaPayload>(`/api/schema/${version}`);
}

export interface VisualRenderResult {
  content: string;
  node_map: Record<string, { start_line: number; end_line: number }>;
  diagnostics: unknown[];
}

export async function renderVisual(
  orgId: string,
  graph: import('@/visual/types').GraphDocument,
): Promise<VisualRenderResult> {
  return request<VisualRenderResult>(`/api/orgs/${orgId}/visual/render`, {
    method: 'POST',
    body: JSON.stringify({ graph }),
  });
}

export type DiffClass =
  | 'component_removed'
  | 'attr_removed'
  | 'attr_added_required'
  | 'enum_value_removed'
  | 'port_type_changed'
  | 'stability_changed'
  | 'migration_available';

export interface UpgradeItem {
  node_id: string;
  node_label: string;
  component: string;
  class: DiffClass;
  detail?: string;
}

export interface UpgradeCheckResult {
  old_version: string;
  new_version: string;
  items: UpgradeItem[];
  needs_upgrade: boolean;
}

export async function upgradeCheck(
  orgId: string,
  graph: import('@/visual/types').GraphDocument,
): Promise<UpgradeCheckResult> {
  return request<UpgradeCheckResult>(`/api/orgs/${orgId}/visual/upgrade-check`, {
    method: 'POST',
    body: JSON.stringify({ graph }),
  });
}

export function stampSchemaVersion(
  graph: import('@/visual/types').GraphDocument,
  version: string,
): import('@/visual/types').GraphDocument {
  return { ...graph, schema_version: version };
}

export async function validateVisual(
  orgId: string,
  graph: import('@/visual/types').GraphDocument,
): Promise<{ diagnostics: unknown[] }> {
  return request<{ diagnostics: unknown[] }>(`/api/orgs/${orgId}/visual/validate`, {
    method: 'POST',
    body: JSON.stringify({ graph }),
  });
}

export interface RelabelStep {
  rule_index: number;
  action: string;
  before: Record<string, string>;
  after?: Record<string, string>;
  kept: boolean;
}
export interface TargetTrace {
  input: Record<string, string>;
  steps: RelabelStep[];
  output?: Record<string, string>;
  kept: boolean;
}
export interface SimulateRelabelResult {
  traces: TargetTrace[];
}
export interface StageEffect {
  stage_index: number;
  stage_type: string;
  simulated: boolean;
  line_before: string;
  line_after: string;
  labels_before: Record<string, string>;
  labels_after: Record<string, string>;
  dropped?: boolean;
  note?: string;
}
export interface LineTrace {
  input: string;
  steps: StageEffect[];
  output?: string;
  dropped: boolean;
}
export interface SimulateLogsResult {
  traces: LineTrace[];
}

export async function simulateRelabel(
  orgId: string,
  body: { rules: unknown[]; sample_targets: unknown[] },
): Promise<SimulateRelabelResult> {
  return request(`/api/orgs/${orgId}/simulate/relabel`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
}
export async function simulateLogs(
  orgId: string,
  body: { stages: unknown[]; sample_lines: string[] },
): Promise<SimulateLogsResult> {
  return request(`/api/orgs/${orgId}/simulate/logs`, {
    method: 'POST',
    body: JSON.stringify(body),
  });
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: {
      'Content-Type': 'application/json',
      'X-Requested-With': 'XMLHttpRequest',
      ...init?.headers,
    },
    ...init,
  });
  if (!res.ok) {
    let err: ApiError = { code: 'unknown', message: res.statusText };
    try {
      const body = await res.json();
      err = body.error ?? err;
    } catch (_) {
      /* ignore */
    }
    throw err;
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// ---- Types ----

export interface Org {
  id: string;
  name: string;
  display_name: string;
  admin_group_id: string;
  reader_group_id?: string;
  created_at: string;
  updated_at: string;
}

export interface Cluster {
  id: string;
  name: string;
  org_id?: string;
  created_at: string;
}

export interface Collector {
  id: string;
  cluster_id: string;
  cluster: string;
  role: string;
}

export interface CollectorInstance {
  id: string;
  collector_id: string;
  name: string;
  alloy_version?: string;
  os?: string;
  remote_config_status?: string;
  remote_config_error?: string;
  last_seen?: string;
  unregistered_at?: string;
}

export interface Pipeline {
  id: string;
  org_id: string;
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
}

export interface PipelineRevision {
  id: string;
  pipeline_id: string;
  revision: number;
  contents: string;
  matchers: string[];
  enabled: boolean;
  changed_by: string;
  changed_at: string;
  change_note: string;
}

export interface Destination {
  id: string;
  org_id: string;
  name: string;
  type: string;
  url: string;
  tenant_id: string;
  secret_name: string;
  secret_namespace: string;
  auth_mode: string;
  created_at: string;
  updated_at: string;
}

export interface AgentToken {
  id: string;
  name: string;
  created_by: string;
  status: string;
  created_at: string;
}

export interface ValidationResult {
  valid: boolean;
  diagnostics: Diagnostic[];
}

export interface Diagnostic {
  line: number;
  col: number;
  message: string;
  stage: number;
}

// ---- Admin API ----

export const adminApi = {
  listOrgs: () => request<ListResponse<Org>>('/api/admin/orgs'),
  createOrg: (body: Partial<Org>) =>
    request<Org>('/api/admin/orgs', { method: 'POST', body: JSON.stringify(body) }),
  updateOrg: (id: string, body: Partial<Org>) =>
    request<Org>(`/api/admin/orgs/${id}`, { method: 'PATCH', body: JSON.stringify(body) }),
  deleteOrg: (id: string) => request<void>(`/api/admin/orgs/${id}`, { method: 'DELETE' }),

  listClusters: (unclaimed = false) =>
    request<ListResponse<Cluster>>(`/api/admin/clusters${unclaimed ? '?unclaimed=true' : ''}`),
  claimCluster: (cluster: string, orgId: string) =>
    request<void>(`/api/admin/clusters/${cluster}/claim`, {
      method: 'POST',
      body: JSON.stringify({ org_id: orgId }),
    }),
  unclaimCluster: (cluster: string) =>
    request<void>(`/api/admin/clusters/${cluster}/unclaim`, { method: 'POST' }),

  listTokens: () => request<ListResponse<AgentToken>>('/api/admin/agent-tokens'),
  createToken: (name: string) =>
    request<{ id: string; name: string; secret: string }>('/api/admin/agent-tokens', {
      method: 'POST',
      body: JSON.stringify({ name }),
    }),
  revokeToken: (id: string) => request<void>(`/api/admin/agent-tokens/${id}`, { method: 'DELETE' }),

  searchGroups: (q: string) =>
    request<ListResponse<{ id: string; displayName: string }>>(
      `/api/admin/groups/search?q=${encodeURIComponent(q)}`,
    ),
};

// ---- Org-scoped API ----

export const orgApi = {
  listCollectors: (orgId: string) =>
    request<ListResponse<Collector>>(`/api/orgs/${orgId}/collectors`),
  getCollector: (orgId: string, id: string) =>
    request<Collector>(`/api/orgs/${orgId}/collectors/${id}`),
  getServedConfig: (orgId: string, id: string) =>
    request<{ content: string; hash: string; computed_at: string }>(
      `/api/orgs/${orgId}/collectors/${id}/served-config`,
    ),

  listPipelines: (orgId: string) => request<ListResponse<Pipeline>>(`/api/orgs/${orgId}/pipelines`),
  getPipeline: (orgId: string, id: string) =>
    request<Pipeline>(`/api/orgs/${orgId}/pipelines/${id}`),
  createPipeline: (orgId: string, body: Partial<Pipeline>) =>
    request<Pipeline>(`/api/orgs/${orgId}/pipelines`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updatePipeline: (orgId: string, id: string, body: Partial<Pipeline>) =>
    request<Pipeline>(`/api/orgs/${orgId}/pipelines/${id}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    }),
  listRevisions: (orgId: string, pipelineId: string) =>
    request<ListResponse<PipelineRevision>>(`/api/orgs/${orgId}/pipelines/${pipelineId}/revisions`),
  listRepoLinks: (orgId: string) =>
    request<
      ListResponse<{
        id: string;
        repository: string;
        branch: string;
        project: string;
        sync_status?: string;
        last_synced_at?: string;
        sync_error?: string;
      }>
    >(`/api/orgs/${orgId}/repo-links`),
  deletePipeline: (orgId: string, id: string) =>
    request<void>(`/api/orgs/${orgId}/pipelines/${id}`, { method: 'DELETE' }),
  enablePipeline: (orgId: string, id: string) =>
    request<Pipeline>(`/api/orgs/${orgId}/pipelines/${id}/enable`, { method: 'POST' }),
  disablePipeline: (orgId: string, id: string) =>
    request<Pipeline>(`/api/orgs/${orgId}/pipelines/${id}/disable`, { method: 'POST' }),
  validatePipeline: (orgId: string, body: { name?: string; contents: string }) =>
    request<ValidationResult>(`/api/orgs/${orgId}/pipelines/validate`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  previewMatches: (orgId: string, id: string) =>
    request<{ collectors: Array<{ cluster: string; role: string; id: string }> }>(
      `/api/orgs/${orgId}/pipelines/${id}/preview-matches`,
    ),

  listDestinations: (orgId: string) =>
    request<ListResponse<Destination>>(`/api/orgs/${orgId}/destinations`),
  createDestination: (orgId: string, body: Partial<Destination>) =>
    request<Destination>(`/api/orgs/${orgId}/destinations`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateDestination: (orgId: string, id: string, body: Partial<Destination>) =>
    request<Destination>(`/api/orgs/${orgId}/destinations/${id}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    }),
  deleteDestination: (orgId: string, id: string) =>
    request<void>(`/api/orgs/${orgId}/destinations/${id}`, { method: 'DELETE' }),

  getAttributes: (orgId: string) =>
    request<Record<string, string[]>>(`/api/orgs/${orgId}/attributes`),
};
