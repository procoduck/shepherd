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
// docs/api-contract-design.md). These helpers translate fixture -> wire
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
    adoOrgUrl: s(c, 'ado_org_url'),
    entraTenantId: s(c, 'entra_tenant_id'),
    clientId: s(c, 'client_id'),
    createdAt: c['created_at'],
  };
}

function repoLinkToWire(l: Obj) {
  return {
    id: s(l, 'id'),
    project: s(l, 'project'),
    repository: s(l, 'repository'),
    branch: s(l, 'branch'),
    path: s(l, 'path'),
    syncStatus: s(l, 'sync_status'),
    lastSyncedAt: l['last_synced_at'],
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
    return json(r, 200, {
      id: req['orgId'],
      displayName: req['displayName'],
      adminGroupId: req['adminGroupId'],
      readerGroupId: req['readerGroupId'],
    });
  });
  router.register('POST', '/shepherd.mgmt.v1.AdminService/DeleteOrg', (r) => json(r, 200, {}));

  router.register('POST', '/shepherd.mgmt.v1.AdminService/ListClusters', (r) =>
    json(r, 200, list((st.clusters as Obj[]).map(clusterToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.AdminService/ClaimCluster', (r) =>
    json(r, 200, { status: 'claimed' }),
  );
  router.register('POST', '/shepherd.mgmt.v1.AdminService/UnclaimCluster', (r) =>
    json(r, 200, { status: 'unclaimed' }),
  );

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
  router.register('POST', '/shepherd.mgmt.v1.AdminService/RevokeAgentToken', (r) =>
    json(r, 200, {}),
  );
  router.register('POST', '/shepherd.mgmt.v1.AdminService/SearchGroups', (r) =>
    json(r, 200, { items: [], total: 0 }),
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
  router.register('POST', '/shepherd.mgmt.v1.FleetService/CreateAssignment', (r) =>
    json(r, 200, {}),
  );
  router.register('POST', '/shepherd.mgmt.v1.FleetService/DeleteAssignment', (r) =>
    json(r, 200, {}),
  );
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
    json(r, 200, list((st.adoCredentials as Obj[]).map(credentialToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/CreateCredential', async (r) => {
    const req = await body(r);
    const c: Obj = {
      id: mockId('cred'),
      name: req['name'],
      ado_org_url: req['adoOrgUrl'],
      entra_tenant_id: req['entraTenantId'],
      client_id: req['clientId'],
      created_at: '2026-08-17T09:00:00Z',
    };
    st.adoCredentials.push(c);
    return json(r, 200, credentialToWire(c));
  });
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/DeleteCredential', async (r) => {
    const req = await body(r);
    st.adoCredentials = (st.adoCredentials as Obj[]).filter((x) => x['id'] !== req['id']);
    return json(r, 200, {});
  });
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/ListRepoLinks', (r) =>
    json(r, 200, list((st.repoLinks as Obj[]).map(repoLinkToWire))),
  );
  router.register('POST', '/shepherd.mgmt.v1.GitOpsService/CreateRepoLink', async (r) => {
    const req = await body(r);
    const l: Obj = {
      id: mockId('rl'),
      project: req['project'],
      repository: req['repository'],
      branch: (req['branch'] as string) || 'main',
      path: (req['path'] as string) || '/',
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
  router.register('POST', '/shepherd.mgmt.v1.WizardService/ListWizards', (r) =>
    json(r, 200, {
      items: [{ kind: 'app-observability', title: 'App Observability', steps: [] }],
      total: 1,
    }),
  );
  router.register('POST', '/shepherd.mgmt.v1.WizardService/GetWizardSchema', async (r) => {
    const req = await body(r);
    return json(r, 200, { kind: req['kind'], title: 'App Observability', steps: [] });
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

  // ── AuditService ─────────────────────────────────────────────────────────
  router.register('POST', '/shepherd.mgmt.v1.AuditService/ListAudit', (r) =>
    json(r, 200, list((st.auditRows as Obj[]).map(auditToWire))),
  );

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
  // Stays REST per docs/api-contract-design.md ("out of contract, unchanged").
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
    adoCredentials: [],
    repoLinks: [],
    agentTokens: [],
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
  };
}

// Suppress unused import warning
export type { Route };
