import type { Route } from '@playwright/test';
import { connectError, json, list, type MockState, Router } from './router';

const mockIdCounters: Record<string, number> = {};
function mockId(prefix: string): string {
  mockIdCounters[prefix] = (mockIdCounters[prefix] ?? 0) + 1;
  return `${prefix}-${String(mockIdCounters[prefix]).padStart(4, '0')}`;
}

export function resetMockState() {
  for (const key of Object.keys(mockIdCounters)) delete mockIdCounters[key];
}

// ─────────────────────────────────────────────────────────────────────────
// Wire-shape converters.
//
// Fixtures (factories.ts, and specs' inline seed objects) stay snake_case —
// the same shape the pre-migration REST JSON used, and the shape the
// visual-builder's local domain model (web/src/visual/types.ts) still
// speaks for the resources it owns (graph/simulate). Real Connect JSON
// wire responses are camelCase (protojson canonical encoding, see
// docs/archive/api-contract-design.md). These helpers translate fixture -> wire
// so `api.seed({ pipelines: [...] })` etc. keeps reading exactly as it did
// before the cutover. Unknown/extra JSON keys are safely ignored by the
// generated client (connect-es defaults ignoreUnknownFields: true), so
// these only need to get the *known* field names and types right.
// ─────────────────────────────────────────────────────────────────────────

type Obj = Record<string, unknown>;
const s = (o: Obj, k: string): string => (o[k] as string | undefined) ?? '';
const n = (o: Obj, k: string): number => (o[k] as number | undefined) ?? 0;
const b = (o: Obj, k: string): boolean => !!o[k];
const arr = <T = unknown>(o: Obj, k: string): T[] => (o[k] as T[] | undefined) ?? [];

function orgToWire(o: Obj) {
  return {
    id: s(o, 'id'),
    name: s(o, 'name'),
    displayName: s(o, 'display_name'),
    adminGroupId: s(o, 'admin_group_id'),
    readerGroupId: s(o, 'reader_group_id'),
    createdAt: o['created_at'],
    updatedAt: o['updated_at'],
  };
}

function clusterToWire(c: Obj) {
  return {
    id: s(c, 'id'),
    name: s(c, 'name'),
    orgId: s(c, 'org_id'),
    createdAt: c['created_at'],
  };
}

function tokenToWire(t: Obj) {
  return {
    id: s(t, 'id'),
    name: s(t, 'name'),
    createdBy: s(t, 'created_by'),
    status: s(t, 'status'),
    createdAt: t['created_at'],
  };
}

function collectorInstanceToWire(i: Obj) {
  return {
    name: s(i, 'name'),
    alloyVersion: s(i, 'alloy_version'),
    os: s(i, 'os'),
    lastSeen: i['last_seen'],
    remoteConfigStatus: s(i, 'remote_config_status'),
    remoteConfigError: s(i, 'remote_config_error'),
    localAttributes: i['local_attributes'] ?? {},
  };
}

function collectorToWire(c: Obj) {
  return {
    id: s(c, 'id'),
    clusterId: s(c, 'cluster_id'),
    cluster: s(c, 'cluster'),
    role: s(c, 'role'),
    orgId: s(c, 'org_id'),
    remoteConfigStatus: s(c, 'remote_config_status'),
    remoteConfigError: s(c, 'remote_config_error'),
    lastSeen: c['last_seen'],
    alloyVersion: s(c, 'alloy_version'),
    localAttributes: c['local_attributes'] ?? {},
    instances: arr<Obj>(c, 'instances').map(collectorInstanceToWire),
  };
}

function pipelineRevisionToWire(r: Obj) {
  // NOTE: shepherd.mgmt.v1.PipelineRevision only carries
  // revision/changed_by/changed_at/change_note — the legacy REST shape's
  // id/pipeline_id/contents/matchers/enabled were dropped when the proto
  // was authored. Mirrored here rather than "fixed", since the fixture
  // surface must match the real wire contract.
  return {
    revision: n(r, 'revision'),
    changedBy: s(r, 'changed_by'),
    changedAt: r['changed_at'],
    changeNote: s(r, 'change_note'),
  };
}

function pipelineToWire(p: Obj) {
  return {
    id: s(p, 'id'),
    orgId: s(p, 'org_id'),
    name: s(p, 'name'),
    contents: s(p, 'contents'),
    matchers: arr<string>(p, 'matchers'),
    enabled: b(p, 'enabled'),
    source: s(p, 'source'),
    revision: n(p, 'revision'),
    createdBy: s(p, 'created_by'),
    updatedBy: s(p, 'updated_by'),
    createdAt: p['created_at'],
    updatedAt: p['updated_at'],
    revisions: arr<Obj>(p, 'revisions').map(pipelineRevisionToWire),
  };
}

function destinationToWire(d: Obj) {
  return {
    id: s(d, 'id'),
    orgId: s(d, 'org_id'),
    name: s(d, 'name'),
    type: s(d, 'type'),
    url: s(d, 'url'),
    tenantId: s(d, 'tenant_id'),
    secretName: s(d, 'secret_name'),
    secretNamespace: s(d, 'secret_namespace'),
    authMode: s(d, 'auth_mode'),
    extra: d['extra'] ?? {},
    createdAt: d['created_at'],
    updatedAt: d['updated_at'],
  };
}

function credentialToWire(c: Obj) {
  return {
    id: s(c, 'id'),
    name: s(c, 'name'),
    kind: s(c, 'kind') || 'pat',
    username: s(c, 'username'),
    adoOrgUrl: s(c, 'ado_org_url'),
    entraTenantId: s(c, 'entra_tenant_id'),
    clientId: s(c, 'client_id'),
    providerConfig: c['provider_config'] ?? {},
    createdAt: c['created_at'],
  };
}

function repoLinkToWire(l: Obj) {
  return {
    id: s(l, 'id'),
    repoUrl: s(l, 'repo_url'),
    branch: s(l, 'branch'),
    path: s(l, 'path'),
    syncStatus: s(l, 'sync_status'),
    lastSyncedAt: l['last_synced_at'],
    collectorId: s(l, 'collector_id'),
    credentialId: s(l, 'credential_id'),
  };
}

function groupSearchResultToWire(g: Obj) {
  return { id: s(g, 'id'), displayName: s(g, 'display_name') };
}

function assignmentToWire(a: Obj) {
  return {
    id: s(a, 'id'),
    groupId: s(a, 'group_id'),
    groupDisplayName: s(a, 'group_display_name'),
    createdAt: a['created_at'],
  };
}

function auditToWire(a: Obj) {
  return {
    id: n(a, 'id'),
    at: a['at'],
    actor: s(a, 'actor'),
    actorType: s(a, 'actor_type'),
    orgId: s(a, 'org_id'),
    action: s(a, 'action'),
    resourceType: s(a, 'resource_type'),
    resourceId: s(a, 'resource_id'),
  };
}

// Visual/simulate results already live in the wire-adjacent-but-snake_case
// shape defined by web/src/api/client.ts's local result types (that's the
// shape web/src/visual/** and specs seed). These convert that shape into
// the camelCase Connect JSON the generated VisualService/SimulateService
// clients expect.
function graphDocToWire(g: Obj | undefined) {
  if (!g) return undefined;
  return {
    kind: s(g, 'kind'),
    schemaVersion: s(g, 'schema_version'),
    nodes: g['nodes'],
    edges: g['edges'],
    bindings: g['bindings'],
    viewport: g['viewport'],
    meta: g['meta'] ? { createdWith: s(g['meta'] as Obj, 'created_with') } : undefined,
  };
}

function upgradeCheckResultToWire(r: Obj) {
  return {
    oldVersion: s(r, 'old_version'),
    newVersion: s(r, 'new_version'),
    needsUpgrade: b(r, 'needs_upgrade'),
    items: arr<Obj>(r, 'items').map((i) => ({
      nodeId: s(i, 'node_id'),
      nodeLabel: s(i, 'node_label'),
      component: s(i, 'component'),
      class: s(i, 'class'),
      detail: s(i, 'detail'),
    })),
  };
}

function visualRenderResultToWire(r: Obj) {
  const nodeMap: Obj = {};
  for (const [key, value] of Object.entries((r['node_map'] as Obj) ?? {})) {
    const v = value as Obj;
    nodeMap[key] = { startLine: n(v, 'start_line'), endLine: n(v, 'end_line') };
  }
  return { content: s(r, 'content'), nodeMap, diagnostics: arr(r, 'diagnostics') };
}

function relabelStepToWire(step: Obj) {
  return {
    ruleIndex: n(step, 'rule_index'),
    action: s(step, 'action'),
    before: step['before'] ?? {},
    after: step['after'] ?? {},
    kept: b(step, 'kept'),
  };
}

function simulateRelabelResultToWire(r: Obj) {
  return {
    traces: arr<Obj>(r, 'traces').map((t) => ({
      input: t['input'] ?? {},
      output: t['output'] ?? {},
      kept: b(t, 'kept'),
      steps: arr<Obj>(t, 'steps').map(relabelStepToWire),
    })),
  };
}

function stageEffectToWire(step: Obj) {
  return {
    stageIndex: n(step, 'stage_index'),
    stageType: s(step, 'stage_type'),
    simulated: b(step, 'simulated'),
    lineBefore: s(step, 'line_before'),
    lineAfter: s(step, 'line_after'),
    labelsBefore: step['labels_before'] ?? {},
    labelsAfter: step['labels_after'] ?? {},
    dropped: b(step, 'dropped'),
    note: s(step, 'note'),
  };
}

function simulateLogsResultToWire(r: Obj) {
  return {
    traces: arr<Obj>(r, 'traces').map((t) => ({
      input: s(t, 'input'),
      output: s(t, 'output'),
      dropped: b(t, 'dropped'),
      steps: arr<Obj>(t, 'steps').map(stageEffectToWire),
    })),
  };
}

// S3 sandbox run (VB-1 §6.4) — mirrors shepherd.mgmt.v1.SimulateRun.
const DEFAULT_FIDELITY_NOTE =
  'S3 stubs discovery and drops every secret before the graph ever runs — no performance, cardinality, or multi-collector fidelity is implied (VB-1 §6.5).';

function simulateRunToWire(run: Obj) {
  return {
    id: s(run, 'id'),
    orgId: s(run, 'org_id'),
    status: s(run, 'status'),
    createdAt: run['created_at'],
    startedAt: run['started_at'],
    finishedAt: run['finished_at'],
    requestedDurationSeconds: n(run, 'requested_duration_seconds'),
    queuePosition: n(run, 'queue_position'),
    rewrites: arr<Obj>(run, 'rewrites').map((rw) => ({
      nodeId: s(rw, 'node_id'),
      nodeLabel: s(rw, 'node_label'),
      component: s(rw, 'component'),
      kind: s(rw, 'kind'),
      detail: s(rw, 'detail'),
    })),
    capturedSeries: arr<Obj>(run, 'captured_series').map((cs) => ({
      name: s(cs, 'name'),
      labels: cs['labels'] ?? {},
      sampleCount: n(cs, 'sample_count'),
    })),
    capturedLogLines: arr<Obj>(run, 'captured_log_lines').map((l) => ({
      labels: l['labels'] ?? {},
      line: s(l, 'line'),
    })),
    componentHealth: arr<Obj>(run, 'component_health').map((h) => ({
      nodeId: s(h, 'node_id'),
      nodeLabel: s(h, 'node_label'),
      component: s(h, 'component'),
      healthState: s(h, 'health_state'),
      message: s(h, 'message'),
    })),
    gateDiagnostics: arr(run, 'gate_diagnostics'),
    stderrTail: s(run, 'stderr_tail'),
    errorCode: s(run, 'error_code'),
    errorMessage: s(run, 'error_message'),
    fidelityNote: s(run, 'fidelity_note') || DEFAULT_FIDELITY_NOTE,
  };
}

// Default handlers for every shepherd.mgmt.v1 procedure, plus the surviving
// REST surface (/auth/*, /api/schema/*).
export function installDefaultHandlers(router: Router) {
  const st = router.state;
  const body = async (r: Route): Promise<Obj> => {
    try {
      return ((await r.request().postDataJSON()) as Obj) ?? {};
    } catch {
      return {};
    }
  };

  // ── MeService ────────────────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.MeService/GetMe', (r) => {
    if (st.me === null || st.me === undefined) {
      return connectError(r, 401, 'unauthenticated', 'not authenticated');
    }
    return json(r, 200, st.me);
  });

  // ── AdminService ─────────────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.AdminService/ListOrgs', (r) =>
    json(r, 200, list((st.orgs as Obj[]).map(orgToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.AdminService/CreateOrg', async (r) => {
    const req = await body(r);
    const o: Obj = {
      id: mockId('org'),
      admin_group_id: (req['adminGroupId'] as string) || '11111111-1111-1111-1111-111111111111',
      name: req['name'],
      display_name: req['displayName'],
      reader_group_id: req['readerGroupId'],
      created_at: '2026-08-17T09:00:00Z',
      updated_at: '2026-08-17T09:00:00Z',
    };
    st.orgs.push(o);
    return json(r, 200, orgToWire(o));
  });
  router.register('POST', '/shepherd.mgmt.v1.AdminService/UpdateOrg', async (r) => {
    const req = await body(r);
    const o = (st.orgs as Obj[]).find((org) => org['id'] === req['orgId']);
    if (!o) return connectError(r, 404, 'not_found', 'org not found');
    o['display_name'] = req['displayName'];
    o['admin_group_id'] = req['adminGroupId'];
    o['reader_group_id'] = req['readerGroupId'];
    return json(r, 200, orgToWire(o));
  });
  router.register('POST', '/shepherd.mgmt.v1.AdminService/DeleteOrg', async (r) => {
    const req = await body(r);
    const orgId = req['orgId'];
    const clusterCount = (st.clusters as Obj[]).filter((c) => c['org_id'] === orgId).length;
    const pipelineCount = (st.pipelines as Obj[]).filter((p) => p['org_id'] === orgId).length;
    if (clusterCount > 0 || pipelineCount > 0) {
      // 409: connect.CodeAlreadyExists's canonical HTTP mapping, matching the
      // real server (internal/mgmtapi/rpc_admin.go: DeleteOrg) for a
      // non-empty org.
      return connectError(
        r,
        409,
        'already_exists',
        `org has ${clusterCount} clusters, ${pipelineCount} pipelines`,
      );
    }
    st.orgs = (st.orgs as Obj[]).filter((org) => org['id'] !== orgId);
    return json(r, 200, {});
  });

  router.register('POST', '/shepherd.mgmt.v1.AdminService/ListClusters', async (r) => {
    const req = await body(r);
    const items = req['unclaimed']
      ? (st.clusters as Obj[]).filter((c) => !c['org_id'])
      : (st.clusters as Obj[]);
    return json(r, 200, list(items.map(clusterToWire)));
  });
  router.register('POST', '/shepherd.mgmt.v1.AdminService/ClaimCluster', async (r) => {
    const req = await body(r);
    const c = (st.clusters as Obj[]).find((cl) => cl['name'] === req['cluster']);
    if (!c) return connectError(r, 404, 'not_found', 'cluster not found');
    c['org_id'] = req['orgId'];
    return json(r, 200, { status: 'claimed' });
  });
  router.register('POST', '/shepherd.mgmt.v1.AdminService/UnclaimCluster', async (r) => {
    const req = await body(r);
    const c = (st.clusters as Obj[]).find((cl) => cl['name'] === req['cluster']);
    if (!c) return connectError(r, 404, 'not_found', 'cluster not found');
    c['org_id'] = null;
    return json(r, 200, { status: 'unclaimed' });
  });

  router.register('POST', '/shepherd.mgmt.v1.AdminService/ListAgentTokens', (r) =>
    json(r, 200, list((st.agentTokens as Obj[]).map(tokenToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.AdminService/CreateAgentToken', async (r) => {
    const req = await body(r);
    const t: Obj = {
      id: mockId('tok'),
      name: req['name'],
      status: 'active',
      created_by: 'admin',
      created_at: '2026-08-17T09:00:00Z',
    };
    st.agentTokens.push(t);
    return json(r, 200, { id: t['id'], name: t['name'], secret: 'one-time-secret-value' });
  });
  router.register('POST', '/shepherd.mgmt.v1.AdminService/RevokeAgentToken', async (r) => {
    const req = await body(r);
    const t = (st.agentTokens as Obj[]).find((tok) => tok['id'] === req['id']);
    if (!t) return connectError(r, 404, 'not_found', 'token not found');
    t['status'] = 'revoked';
    return json(r, 200, {});
  });
  // Real server: SearchGroups is a stub returning an empty list (no Graph
  // integration yet). st.groupSearchResults lets a spec seed hits to prove
  // the search box itself is wired correctly; the default (empty) exercises
  // the graceful-degradation path (see B5's "search returns nothing").
  router.register('POST', '/shepherd.mgmt.v1.AdminService/SearchGroups', (r) =>
    json(r, 200, list(((st.groupSearchResults as Obj[]) ?? []).map(groupSearchResultToWire))),
  );

  // ── FleetService ─────────────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.FleetService/ListCollectors', (r) =>
    json(r, 200, list((st.collectors as Obj[]).map(collectorToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.FleetService/GetCollector', async (r) => {
    const req = await body(r);
    const c = (st.collectors as Obj[]).find((x) => x['id'] === req['id']);
    return c
      ? json(r, 200, collectorToWire(c))
      : connectError(r, 404, 'not_found', 'collector not found');
  });
  router.register('POST', '/shepherd.mgmt.v1.FleetService/GetServedConfig', (r) => {
    const sc = st.servedConfig as Obj;
    return json(r, 200, {
      content: s(sc, 'content'),
      hash: s(sc, 'hash'),
      computedAt: sc['computed_at'],
    });
  });
  router.register('POST', '/shepherd.mgmt.v1.FleetService/ListAssignments', async (r) => {
    const req = await body(r);
    const rows = (st.assignments as Obj[]).filter((a) => a['collector_id'] === req['collectorId']);
    return json(r, 200, list(rows.map(assignmentToWire)));
  });
  router.register('POST', '/shepherd.mgmt.v1.FleetService/CreateAssignment', async (r) => {
    const req = await body(r);
    const rows = st.assignments as Obj[];
    const existing = rows.find(
      (a) => a['collector_id'] === req['collectorId'] && a['group_id'] === req['groupId'],
    );
    if (existing) {
      existing['group_display_name'] = req['groupDisplayName'];
      return json(r, 200, { id: existing['id'], groupId: existing['group_id'] });
    }
    const a: Obj = {
      id: mockId('assign'),
      collector_id: req['collectorId'],
      group_id: req['groupId'],
      group_display_name: req['groupDisplayName'],
      created_at: '2026-08-19T09:00:00Z',
    };
    rows.push(a);
    return json(r, 200, { id: a['id'], groupId: a['group_id'] });
  });
  router.register('POST', '/shepherd.mgmt.v1.FleetService/DeleteAssignment', async (r) => {
    const req = await body(r);
    st.assignments = (st.assignments as Obj[]).filter(
      (a) => !(a['collector_id'] === req['collectorId'] && a['group_id'] === req['groupId']),
    );
    return json(r, 200, {});
  });
  router.register('POST', '/shepherd.mgmt.v1.FleetService/ListAttributes', (r) =>
    json(r, 200, { attributes: st.attributes }),
  );

  // ── PipelineService ──────────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/ListPipelines', (r) =>
    json(r, 200, list((st.pipelines as Obj[]).map(pipelineToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/CreatePipeline', async (r) => {
    const req = await body(r);
    const p: Obj = {
      id: mockId('pip'),
      org_id: req['orgId'],
      name: req['name'],
      contents: req['contents'],
      matchers: req['matchers'] ?? [],
      enabled: false,
      source: (req['source'] as string) || 'ui',
      created_at: '2026-08-17T09:00:00Z',
      updated_at: '2026-08-17T09:00:00Z',
    };
    st.pipelines.push(p);
    return json(r, 200, pipelineToWire(p));
  });
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/ValidatePipeline', (r) =>
    json(r, 200, st.validateResult),
  );
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/GetPipeline', async (r) => {
    const req = await body(r);
    const p = (st.pipelines as Obj[]).find((x) => x['id'] === req['id']);
    return p
      ? json(r, 200, pipelineToWire(p))
      : connectError(r, 404, 'not_found', 'pipeline not found');
  });
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/UpdatePipeline', async (r) => {
    const req = await body(r);
    const idx = (st.pipelines as Obj[]).findIndex((x) => x['id'] === req['id']);
    if (idx >= 0) {
      Object.assign(st.pipelines[idx] as Obj, {
        name: req['name'],
        contents: req['contents'],
        matchers: req['matchers'] ?? [],
      });
    }
    return json(r, 200, pipelineToWire((st.pipelines[idx] as Obj) ?? req));
  });
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/DeletePipeline', async (r) => {
    const req = await body(r);
    st.pipelines = (st.pipelines as Obj[]).filter((x) => x['id'] !== req['id']);
    return json(r, 200, {});
  });
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/EnablePipeline', async (r) => {
    const req = await body(r);
    const p = (st.pipelines as Obj[]).find((x) => x['id'] === req['id']);
    if (p) p['enabled'] = true;
    return json(r, 200, pipelineToWire(p ?? {}));
  });
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/DisablePipeline', async (r) => {
    const req = await body(r);
    const p = (st.pipelines as Obj[]).find((x) => x['id'] === req['id']);
    if (p) p['enabled'] = false;
    return json(r, 200, pipelineToWire(p ?? {}));
  });
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/PreviewMatches', (r) => {
    const pr = st.previewResult as unknown as { collector_ids: string[] };
    return json(r, 200, {
      collectors: (pr.collector_ids ?? []).map((id) => ({ id, cluster: '', role: '' })),
    });
  });
  router.register('POST', '/shepherd.mgmt.v1.PipelineService/ListRevisions', async (r) => {
    const req = await body(r);
    const p = (st.pipelines as Obj[]).find((x) => x['id'] === req['id']);
    const revisions = arr<Obj>(p ?? {}, 'revisions');
    return json(r, 200, list(revisions.map(pipelineRevisionToWire)));
  });

  // ── DestinationService ───────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.DestinationService/ListDestinations', (r) =>
    json(r, 200, list((st.destinations as Obj[]).map(destinationToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.DestinationService/CreateDestination', async (r) => {
    const req = await body(r);
    const d: Obj = {
      id: mockId('dst'),
      org_id: req['orgId'],
      name: req['name'],
      type: req['type'],
      url: req['url'],
      tenant_id: req['tenantId'],
      secret_name: req['secretName'],
      secret_namespace: req['secretNamespace'],
      auth_mode: req['authMode'],
      created_at: '2026-08-17T09:00:00Z',
      updated_at: '2026-08-17T09:00:00Z',
    };
    st.destinations.push(d);
    return json(r, 200, destinationToWire(d));
  });
  router.register('POST', '/shepherd.mgmt.v1.DestinationService/GetDestination', async (r) => {
    const req = await body(r);
    const d = (st.destinations as Obj[]).find((x) => x['id'] === req['id']);
    return d ? json(r, 200, destinationToWire(d)) : connectError(r, 404, 'not_found', 'not found');
  });
  router.register('POST', '/shepherd.mgmt.v1.DestinationService/UpdateDestination', async (r) => {
    const req = await body(r);
    const idx = (st.destinations as Obj[]).findIndex((x) => x['id'] === req['id']);
    if (idx >= 0) {
      Object.assign(st.destinations[idx] as Obj, {
        name: req['name'],
        type: req['type'],
        url: req['url'],
        tenant_id: req['tenantId'],
        secret_name: req['secretName'],
        secret_namespace: req['secretNamespace'],
        auth_mode: req['authMode'],
      });
    }
    return json(r, 200, destinationToWire((st.destinations[idx] as Obj) ?? req));
  });
  router.register('POST', '/shepherd.mgmt.v1.DestinationService/DeleteDestination', async (r) => {
    const req = await body(r);
    st.destinations = (st.destinations as Obj[]).filter((x) => x['id'] !== req['id']);
    return json(r, 200, {});
  });

  // ── GitOpsService ────────────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/ListCredentials', (r) =>
    json(r, 200, list((st.gitCredentials as Obj[]).map(credentialToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/CreateCredential', async (r) => {
    const req = await body(r);
    const c: Obj = {
      id: mockId('cred'),
      name: req['name'],
      kind: req['kind'] || 'pat',
      username: req['username'],
      ado_org_url: req['adoOrgUrl'],
      entra_tenant_id: req['entraTenantId'],
      client_id: req['clientId'],
      provider_config: req['providerConfig'] ?? {},
      created_at: '2026-08-17T09:00:00Z',
    };
    st.gitCredentials.push(c);
    return json(r, 200, credentialToWire(c));
  });
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/DeleteCredential', async (r) => {
    const req = await body(r);
    st.gitCredentials = (st.gitCredentials as Obj[]).filter((x) => x['id'] !== req['id']);
    return json(r, 200, {});
  });
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/TestCredential', async (r) => {
    const req = await body(r);
    // Mock reachability rule: a repo_url containing "unreachable" fails,
    // everything else (including the empty default) succeeds — lets specs
    // drive both branches deterministically without a real git server.
    const reachable = !String(req['repoUrl'] ?? '').includes('unreachable');
    return json(r, 200, {
      reachable,
      error: reachable ? '' : 'dial tcp: connection refused',
      tokenExchangeRequired: false,
      tokenExchangeOk: false,
    });
  });
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/ListRepoLinks', (r) =>
    json(r, 200, list((st.repoLinks as Obj[]).map(repoLinkToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/CreateRepoLink', async (r) => {
    const req = await body(r);
    const l: Obj = {
      id: mockId('rl'),
      repo_url: req['repoUrl'],
      branch: (req['branch'] as string) || 'main',
      path: (req['path'] as string) || '/',
      collector_id: req['collectorId'],
      credential_id: req['credentialId'],
      created_at: '2026-08-17T09:00:00Z',
    };
    st.repoLinks.push(l);
    return json(r, 200, repoLinkToWire(l));
  });
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/DeleteRepoLink', async (r) => {
    const req = await body(r);
    st.repoLinks = (st.repoLinks as Obj[]).filter((x) => x['id'] !== req['id']);
    return json(r, 200, {});
  });

  // ── WizardService ────────────────────────────────────────────────────────
  // Mirrors internal/wizard/appobservability.Wizard.Schema() (Go) field-for-
  // field -- keep in sync with it, not the earlier steps:[] stub (P2 mock
  // drift fix), so the mocked wizard.spec.ts flow actually exercises the
  // schema-driven stepper instead of trivially falling straight to Review.
  const appObservabilitySteps = [
    {
      id: 'targets',
      title: 'Scrape targets',
      fields: [
        {
          name: 'scrape_url',
          label: 'Metrics endpoint URL',
          type: 'text',
          required: true,
          placeholder: 'http://myapp:9090/metrics',
          description: 'Prometheus /metrics URL exposed by your app.',
        },
        { name: 'scrape_interval', label: 'Scrape interval', type: 'text', default: '60s' },
        {
          name: 'job_name',
          label: 'Job label',
          type: 'text',
          required: true,
          placeholder: 'my-app',
        },
      ],
    },
    {
      id: 'logs',
      title: 'Log collection',
      fields: [
        { name: 'logs_enabled', label: 'Collect logs', type: 'toggle', default: true },
        {
          name: 'log_path',
          label: 'Log file path(s)',
          type: 'text',
          placeholder: '/var/log/my-app/*.log',
          description: 'Glob pattern for log files.',
        },
        {
          name: 'log_format',
          label: 'Log format',
          type: 'select',
          options: ['logfmt', 'json', 'raw'],
          default: 'logfmt',
        },
      ],
    },
    {
      id: 'destinations',
      title: 'Destinations',
      fields: [
        {
          name: 'metrics_dest_name',
          label: 'Metrics destination',
          type: 'text',
          required: true,
          description: 'Name of a Prometheus-type destination in this org.',
        },
        {
          name: 'logs_dest_name',
          label: 'Logs destination (Loki)',
          type: 'text',
          description: 'Name of a Loki-type destination. Leave blank to skip log forwarding.',
        },
      ],
    },
    {
      id: 'matchers',
      title: 'Collector matching',
      fields: [
        {
          name: 'cluster_pattern',
          label: 'Cluster pattern (regex)',
          type: 'text',
          placeholder: 'prod-.*',
          description: 'Applies this pipeline to clusters matching the regex.',
        },
        {
          name: 'role',
          label: 'Collector role',
          type: 'select',
          options: ['metrics', 'logs', 'singleton'],
          default: 'metrics',
        },
      ],
    },
  ];
  router.register('POST', '/shepherd.mgmt.v1.WizardService/ListWizards', (r) =>
    json(r, 200, {
      items: [
        { kind: 'app-observability', title: 'App Observability', steps: appObservabilitySteps },
      ],
      total: 1,
    }),
  );
  router.register('POST', '/shepherd.mgmt.v1.WizardService/GetWizardSchema', async (r) => {
    const req = await body(r);
    return json(r, 200, {
      kind: req['kind'],
      title: 'App Observability',
      steps: appObservabilitySteps,
    });
  });
  router.register('POST', '/shepherd.mgmt.v1.WizardService/RenderWizard', async (r) => {
    const req = await body(r);
    const state = (req['state'] as Obj) ?? {};
    const jobName = s(state, 'job_name') || 'app';
    const matchers: string[] = [];
    if (s(state, 'cluster_pattern')) matchers.push(`cluster=~"${s(state, 'cluster_pattern')}"`);
    if (s(state, 'role')) matchers.push(`role="${s(state, 'role')}"`);
    return json(r, 200, {
      contents: `prometheus.scrape "app" {\n  job_name = "${jobName}"\n  forward_to = [prometheus.remote_write.metrics.receiver]\n}\n`,
      matchers,
      valid: true,
      diagnostics: [],
      matchedCollectors: [],
    });
  });
  router.register('POST', '/shepherd.mgmt.v1.WizardService/CommitWizard', async (r) => {
    const req = await body(r);
    const p: Obj = {
      id: mockId('pip'),
      org_id: req['orgId'],
      name: req['name'],
      enabled: false,
      source: 'wizard',
      created_at: '2026-08-17T09:00:00Z',
      updated_at: '2026-08-17T09:00:00Z',
    };
    st.pipelines.push(p);
    return json(r, 200, pipelineToWire(p));
  });

  // ── VisualService ────────────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.VisualService/Render', (r) =>
    json(
      r,
      200,
      visualRenderResultToWire(
        (st.visualRenderResult as Obj) ?? { content: '// generated by shepherd visual builder\n' },
      ),
    ),
  );
  router.register('POST', '/shepherd.mgmt.v1.VisualService/Validate', (r) =>
    json(r, 200, { diagnostics: [] }),
  );
  router.register('POST', '/shepherd.mgmt.v1.VisualService/UpgradeCheck', (r) =>
    json(
      r,
      200,
      upgradeCheckResultToWire(
        (st.upgradeCheckResult as Obj) ?? {
          old_version: 'alloy-v1.12.0',
          new_version: 'alloy-v1.18.1',
          items: [],
          needs_upgrade: false,
        },
      ),
    ),
  );
  router.register('POST', '/shepherd.mgmt.v1.VisualService/GraphView', (r) => {
    const gv = (st.graphViewResult as Obj) ?? {
      graph: {
        kind: 'alloy-graph/v1',
        schema_version: 'alloy-v1.18.1',
        nodes: [],
        edges: [],
        bindings: [],
        viewport: { x: 0, y: 0, zoom: 1 },
        meta: { created_with: 'shepherd-parser' },
      },
      opaque: false,
      warning: '',
    };
    return json(r, 200, {
      graph: graphDocToWire(gv['graph'] as Obj),
      opaque: b(gv, 'opaque'),
      warning: s(gv, 'warning'),
    });
  });

  // ── SimulateService ──────────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.SimulateService/SimulateRelabel', (r) =>
    json(
      r,
      200,
      simulateRelabelResultToWire(
        (st.simulateRelabelResult as Obj) ?? {
          traces: [
            {
              input: { __meta_kubernetes_pod_name: 'api-server-abc123' },
              steps: [
                {
                  rule_index: 0,
                  action: 'replace',
                  before: { __meta_kubernetes_pod_name: 'api-server-abc123' },
                  after: { instance: 'api-server-abc123' },
                  kept: true,
                },
              ],
              output: { instance: 'api-server-abc123' },
              kept: true,
            },
            {
              input: { __meta_kubernetes_pod_name: 'worker-xyz789' },
              steps: [
                {
                  rule_index: 0,
                  action: 'keep',
                  before: { __meta_kubernetes_pod_name: 'worker-xyz789' },
                  kept: false,
                },
              ],
              kept: false,
            },
          ],
        },
      ),
    ),
  );
  router.register('POST', '/shepherd.mgmt.v1.SimulateService/SimulateLogs', (r) =>
    json(
      r,
      200,
      simulateLogsResultToWire(
        (st.simulateLogsResult as Obj) ?? {
          traces: [
            {
              input: 'sample log line',
              steps: [
                {
                  stage_index: 0,
                  stage_type: 'json',
                  simulated: true,
                  line_before: 'sample log line',
                  line_after: 'sample log line',
                  labels_before: {},
                  labels_after: {},
                },
              ],
              output: 'sample log line',
              dropped: false,
            },
          ],
        },
      ),
    ),
  );

  // CreateRun/GetRun (VB-1 §6.4): CreateRun just mints a run id and resets
  // the poll counter; GetRun advances a queued -> running -> running ->
  // terminal sequence keyed off how many times IT has been called for the
  // current run, not off real elapsed time — so a spec observes the whole
  // progression deterministically regardless of how fast/slow the poller
  // driving it runs. `simulateRunResult` (default: an all-green completed
  // run with no rewrites) seeds the fields that only apply once terminal.
  router.register('POST', '/shepherd.mgmt.v1.SimulateService/CreateRun', async (r) => {
    if (st.simulateCreateRunError) {
      const { status, code, message } = st.simulateCreateRunError;
      return connectError(r, status, code, message);
    }
    st.simulateRunPollCount = 0;
    st.simulateRunId = mockId('sim-run');
    st.simulateRunCreatedAt = new Date().toISOString();
    st.simulateRunStartedAt = undefined;
    return json(r, 200, { runId: st.simulateRunId });
  });

  router.register('POST', '/shepherd.mgmt.v1.SimulateService/GetRun', async (r) => {
    const req = await body(r);
    st.simulateRunPollCount = (st.simulateRunPollCount ?? 0) + 1;
    const count = st.simulateRunPollCount;
    const seeded = (st.simulateRunResult as Obj) ?? { status: 'completed' };
    const terminalStatus = s(seeded, 'status') || 'completed';
    const durationSeconds = n(seeded, 'requested_duration_seconds') || 30;

    let status: string;
    if (count <= 1) {
      status = 'queued';
    } else if (count <= 3) {
      status = 'running';
      if (!st.simulateRunStartedAt) st.simulateRunStartedAt = new Date().toISOString();
    } else {
      status = terminalStatus;
    }
    const isTerminal = status !== 'queued' && status !== 'running';

    return json(
      r,
      200,
      simulateRunToWire({
        id: (req['id'] as string) || st.simulateRunId,
        org_id: req['orgId'],
        status,
        created_at: st.simulateRunCreatedAt,
        started_at: status === 'queued' ? undefined : st.simulateRunStartedAt,
        finished_at: isTerminal ? new Date().toISOString() : undefined,
        requested_duration_seconds: durationSeconds,
        queue_position: status === 'queued' ? 1 : 0,
        rewrites: isTerminal ? arr(seeded, 'rewrites') : [],
        captured_series: isTerminal ? arr(seeded, 'captured_series') : [],
        captured_log_lines: isTerminal ? arr(seeded, 'captured_log_lines') : [],
        component_health: isTerminal ? arr(seeded, 'component_health') : [],
        gate_diagnostics: isTerminal ? arr(seeded, 'gate_diagnostics') : [],
        stderr_tail: isTerminal ? s(seeded, 'stderr_tail') : '',
        error_code: status === 'failed' ? s(seeded, 'error_code') : '',
        error_message: status === 'failed' ? s(seeded, 'error_message') : '',
        fidelity_note: s(seeded, 'fidelity_note'),
      }),
    );
  });

  // ── AuditService ─────────────────────────────────────────────────────────
  // Mirrors internal/mgmtapi/rpc_audit.go/audit_log.sql: actor is a
  // case-insensitive substring match, action is exact, limit defaults to 25
  // (reset, not clamped, outside (0, 200]), offset resets to 0 when negative.
  router.register('POST', '/shepherd.mgmt.v1.AuditService/ListAudit', async (r) => {
    const req = await body(r);
    const orgId = req['orgId'] as string | undefined;
    const actor = ((req['actor'] as string | undefined) ?? '').toLowerCase();
    const action = (req['action'] as string | undefined) ?? '';
    let limit = (req['limit'] as number | undefined) ?? 0;
    if (limit <= 0 || limit > 200) limit = 25;
    let offset = (req['offset'] as number | undefined) ?? 0;
    if (offset < 0) offset = 0;

    const filtered = (st.auditRows as Obj[])
      .filter((row) => {
        if (orgId && s(row, 'org_id') !== orgId) return false;
        if (actor && !s(row, 'actor').toLowerCase().includes(actor)) return false;
        if (action && s(row, 'action') !== action) return false;
        return true;
      })
      // ORDER BY at DESC, matching internal/store/queries/audit_log.sql —
      // ISO 8601 timestamps sort correctly as strings.
      .sort((a, b) => (s(b, 'at') < s(a, 'at') ? -1 : s(b, 'at') > s(a, 'at') ? 1 : 0));
    return json(r, 200, list(filtered.map(auditToWire), limit, offset));
  });

  // ── Auth (unchanged REST — /auth/* stays plain fetch) ───────────────────
  router.register('GET', '/auth/methods', (r) =>
    r.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        oidc: st.authMethods?.oidc ?? true,
        local_admin: st.authMethods?.local_admin ?? false,
      }),
    }),
  );
  router.register('POST', '/api/auth/local/login', async (r) => {
    const req = (await r.request().postDataJSON()) as { username?: string; password?: string };
    const expected = st.localAdminCreds;
    if (expected && req.username === expected.username && req.password === expected.password) {
      if (st.localAdminPersona !== undefined) st.me = st.localAdminPersona;
      return r.fulfill({ status: 200, contentType: 'application/json', body: '{"ok":true}' });
    }
    return r.fulfill({
      status: 401,
      contentType: 'application/json',
      body: JSON.stringify({
        error: { code: 'invalid_credentials', message: 'invalid username or password' },
      }),
    });
  });
  router.register('GET', '/auth/login', (r) =>
    r.fulfill({ status: 302, headers: { location: '/auth/callback?code=mock' } }),
  );
  router.register('GET', '/auth/callback', (r) =>
    r.fulfill({ status: 302, headers: { location: '/' } }),
  );
  router.register('GET', '/auth/logout', (r) =>
    r.fulfill({ status: 302, headers: { location: '/login' } }),
  );

  // Schema endpoint — serves the visual builder schema fixture if seeded.
  // Stays REST per docs/archive/api-contract-design.md ("out of contract, unchanged").
  router.register('GET', '/api/schema/:version', (r) => {
    if (st.schema) return json(r, 200, st.schema);
    return json(r, 404, { error: { code: 'not_found', message: 'schema not seeded in mock' } });
  });
}

// Drift guard: list every registered route pattern
export function registeredRoutes(router: Router): string[] {
  // Access private routes via type cast for introspection
  return (router as unknown as { routes: Array<{ method: string; pattern: RegExp }> }).routes.map(
    (r) => `${r.method} ${r.pattern.source}`,
  );
}

// Helper: build a default MockState
export function defaultState(): MockState {
  resetMockState();
  return {
    me: null,
    orgs: [],
    clusters: [],
    collectors: [],
    pipelines: [],
    revisions: new Map(),
    destinations: [],
    gitCredentials: [],
    repoLinks: [],
    agentTokens: [],
    assignments: [],
    groupSearchResults: [],
    auditRows: [],
    attributes: { cluster: ['prod-eu-1'], role: ['metrics', 'logs', 'singleton', 'receiver'] },
    previewResult: { count: 2, collector_ids: ['col-0001', 'col-0002'] },
    validateResult: { valid: true, diagnostics: [] },
    servedConfig: {
      content: '',
      hash: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    },
    unmatched: [],
    authMethods: { oidc: true, local_admin: false },
    localAdminCreds: undefined,
    localAdminPersona: undefined,
    schema: undefined,
    visualRenderResult: undefined,
    graphViewResult: undefined,
    upgradeCheckResult: undefined,
    simulateRelabelResult: undefined,
    simulateLogsResult: undefined,
    simulateRunResult: undefined,
    simulateRunPollCount: 0,
    simulateRunId: undefined,
    simulateRunCreatedAt: undefined,
    simulateRunStartedAt: undefined,
    simulateCreateRunError: undefined,
  };
}

// Suppress unused import warning
export type { Route };
