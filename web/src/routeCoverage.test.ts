import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

// Spec-drift guard (docs/frontend-testing.md §3.1 / §10): every endpoint listed
// in docs/spec.md §12 must have a corresponding default handler in
// web/tests/mocks/handlers.ts, or be explicitly, deliberately excused below.
//
// The app has migrated to Connect RPC with REST shims (docs/archive/api-contract-design.md):
// the mock layer keys on Connect procedure paths (`/shepherd.mgmt.v1.<Service>/<Method>`),
// not the legacy REST paths spec §12 documents. So this test parses spec §12's REST
// endpoint list, maps each one to the Connect procedure that actually serves it today,
// and checks THAT procedure has a mock handler. A handful of §12 endpoints are pure REST
// (no Connect procedure at all -- /auth/*, /api/schema/*) and are checked directly.

const specPath = path.resolve(__dirname, '../../docs/spec.md');
const handlersPath = path.resolve(__dirname, '../tests/mocks/handlers.ts');

interface SpecEndpoint {
  /** HTTP method from spec, or a synthetic CRUD sub-operation (LIST/CREATE/GET/UPDATE/DELETE). */
  method: string;
  path: string;
  /** original spec line, for failure messages */ raw: string;
}

const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'];

// A `CRUD <path>` line in spec §12 is shorthand for these five operations.
// LIST/CREATE act on the collection path; GET/UPDATE/DELETE act on `<path>/{id}`.
const CRUD_OPS: { method: string; onItem: boolean }[] = [
  { method: 'LIST', onItem: false },
  { method: 'CREATE', onItem: false },
  { method: 'GET', onItem: true },
  { method: 'UPDATE', onItem: true },
  { method: 'DELETE', onItem: true },
];

/** Parses the fenced endpoint list out of spec.md's "## 12. Management REST API" section. */
function parseSpecSection12(specSource: string): SpecEndpoint[] {
  const heading = '## 12. Management REST API';
  const headingIdx = specSource.indexOf(heading);
  if (headingIdx === -1) {
    throw new Error(`spec.md heading "${heading}" not found -- has §12 been renamed/moved?`);
  }
  const fenceStart = specSource.indexOf('```', headingIdx);
  const fenceEnd = specSource.indexOf('```', fenceStart + 3);
  if (fenceStart === -1 || fenceEnd === -1) {
    throw new Error('spec.md §12 endpoint code block not found (expected a ``` fenced list)');
  }
  const block = specSource.slice(fenceStart + 3, fenceEnd);

  const endpoints: SpecEndpoint[] = [];
  for (const rawLine of block.split('\n')) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue; // blank line or "# Admin" / "# Org-scoped" divider

    const [methodToken, pathToken] = line.split(/\s+/, 2);
    if (!methodToken || !pathToken) continue;
    const cleanPath = pathToken.split('?')[0]; // drop query strings like ?unclaimed=true

    if (methodToken === 'CRUD') {
      for (const op of CRUD_OPS) {
        endpoints.push({
          method: op.method,
          path: cleanPath + (op.onItem ? '/{id}' : ''),
          raw: line,
        });
      }
      continue;
    }

    if (!HTTP_METHODS.includes(methodToken)) continue; // not an endpoint line

    if (cleanPath.includes('|')) {
      // e.g. ".../pipelines/{id}/enable|disable" -> two endpoints
      const lastSlash = cleanPath.lastIndexOf('/');
      const prefix = cleanPath.slice(0, lastSlash + 1);
      for (const alt of cleanPath.slice(lastSlash + 1).split('|')) {
        endpoints.push({ method: methodToken, path: prefix + alt, raw: line });
      }
      continue;
    }

    endpoints.push({ method: methodToken, path: cleanPath, raw: line });
  }
  return endpoints;
}

/** Every `router.register('METHOD', 'path', ...)` call in handlers.ts, as "METHOD path" keys. */
function registeredRouteKeys(handlersSource: string): Set<string> {
  const keys = new Set<string>();
  const call = /router\.register\(\s*'([A-Z*]+)'\s*,\s*'([^']+)'/g;
  for (const m of handlersSource.matchAll(call)) {
    keys.add(`${m[1]} ${m[2]}`);
  }
  return keys;
}

// spec §12 "METHOD path" -> the Connect RPC procedure that actually serves it today
// (docs/archive/api-contract-design.md's routes-mirroring table, cross-checked against
// internal/mgmtapi/router.go and web/tests/mocks/handlers.ts). CRUD-expanded keys use
// the synthetic LIST/CREATE/GET/UPDATE/DELETE method from parseSpecSection12 above.
const SPEC_TO_PROCEDURE: Record<string, string> = {
  'GET /api/me': '/shepherd.mgmt.v1.MeService/GetMe',

  'POST /api/admin/orgs': '/shepherd.mgmt.v1.AdminService/CreateOrg',
  'GET /api/admin/orgs': '/shepherd.mgmt.v1.AdminService/ListOrgs',
  'PATCH /api/admin/orgs/{org}': '/shepherd.mgmt.v1.AdminService/UpdateOrg',
  'DELETE /api/admin/orgs/{org}': '/shepherd.mgmt.v1.AdminService/DeleteOrg',
  'GET /api/admin/clusters': '/shepherd.mgmt.v1.AdminService/ListClusters',
  'POST /api/admin/clusters/{cluster}/claim': '/shepherd.mgmt.v1.AdminService/ClaimCluster',
  'POST /api/admin/clusters/{cluster}/unclaim': '/shepherd.mgmt.v1.AdminService/UnclaimCluster',
  'GET /api/admin/agent-tokens': '/shepherd.mgmt.v1.AdminService/ListAgentTokens',
  'POST /api/admin/agent-tokens': '/shepherd.mgmt.v1.AdminService/CreateAgentToken',
  'DELETE /api/admin/agent-tokens/{id}': '/shepherd.mgmt.v1.AdminService/RevokeAgentToken',
  'GET /api/admin/groups/search': '/shepherd.mgmt.v1.AdminService/SearchGroups',

  'GET /api/orgs/{org}/collectors': '/shepherd.mgmt.v1.FleetService/ListCollectors',
  'GET /api/orgs/{org}/collectors/{id}': '/shepherd.mgmt.v1.FleetService/GetCollector',
  'GET /api/orgs/{org}/collectors/{id}/served-config':
    '/shepherd.mgmt.v1.FleetService/GetServedConfig',
  'POST /api/orgs/{org}/collectors/{id}/assignments':
    '/shepherd.mgmt.v1.FleetService/CreateAssignment',
  'DELETE /api/orgs/{org}/collectors/{id}/assignments/{group_id}':
    '/shepherd.mgmt.v1.FleetService/DeleteAssignment',
  'GET /api/orgs/{org}/attributes': '/shepherd.mgmt.v1.FleetService/ListAttributes',

  'GET /api/orgs/{org}/pipelines': '/shepherd.mgmt.v1.PipelineService/ListPipelines',
  'POST /api/orgs/{org}/pipelines': '/shepherd.mgmt.v1.PipelineService/CreatePipeline',
  'GET /api/orgs/{org}/pipelines/{id}': '/shepherd.mgmt.v1.PipelineService/GetPipeline',
  'PUT /api/orgs/{org}/pipelines/{id}': '/shepherd.mgmt.v1.PipelineService/UpdatePipeline',
  'POST /api/orgs/{org}/pipelines/{id}/enable': '/shepherd.mgmt.v1.PipelineService/EnablePipeline',
  'POST /api/orgs/{org}/pipelines/{id}/disable':
    '/shepherd.mgmt.v1.PipelineService/DisablePipeline',
  'DELETE /api/orgs/{org}/pipelines/{id}': '/shepherd.mgmt.v1.PipelineService/DeletePipeline',
  'POST /api/orgs/{org}/pipelines/validate': '/shepherd.mgmt.v1.PipelineService/ValidatePipeline',
  'GET /api/orgs/{org}/pipelines/{id}/preview-matches':
    '/shepherd.mgmt.v1.PipelineService/PreviewMatches',

  'LIST /api/orgs/{org}/destinations': '/shepherd.mgmt.v1.DestinationService/ListDestinations',
  'CREATE /api/orgs/{org}/destinations': '/shepherd.mgmt.v1.DestinationService/CreateDestination',
  'GET /api/orgs/{org}/destinations/{id}': '/shepherd.mgmt.v1.DestinationService/GetDestination',
  'UPDATE /api/orgs/{org}/destinations/{id}':
    '/shepherd.mgmt.v1.DestinationService/UpdateDestination',
  'DELETE /api/orgs/{org}/destinations/{id}':
    '/shepherd.mgmt.v1.DestinationService/DeleteDestination',

  'LIST /api/orgs/{org}/ado-credentials': '/shepherd.mgmt.v1.GitOpsService/ListCredentials',
  'CREATE /api/orgs/{org}/ado-credentials': '/shepherd.mgmt.v1.GitOpsService/CreateCredential',
  'DELETE /api/orgs/{org}/ado-credentials/{id}': '/shepherd.mgmt.v1.GitOpsService/DeleteCredential',
  'POST /api/orgs/{org}/ado-credentials/{id}/test':
    '/shepherd.mgmt.v1.GitOpsService/TestCredential',

  'LIST /api/orgs/{org}/repo-links': '/shepherd.mgmt.v1.GitOpsService/ListRepoLinks',
  'CREATE /api/orgs/{org}/repo-links': '/shepherd.mgmt.v1.GitOpsService/CreateRepoLink',
  'DELETE /api/orgs/{org}/repo-links/{id}': '/shepherd.mgmt.v1.GitOpsService/DeleteRepoLink',

  'GET /api/orgs/{org}/wizards/application-observability/schema':
    '/shepherd.mgmt.v1.WizardService/GetWizardSchema',
  'POST /api/orgs/{org}/wizards/application-observability/render':
    '/shepherd.mgmt.v1.WizardService/RenderWizard',
  'POST /api/orgs/{org}/wizards/application-observability/commit':
    '/shepherd.mgmt.v1.WizardService/CommitWizard',

  'GET /api/orgs/{org}/audit': '/shepherd.mgmt.v1.AuditService/ListAudit',
};

// Spec §12 endpoints with NO handler today because the backend genuinely doesn't
// implement them yet (verified against internal/mgmtapi/router.go and the proto
// service definitions) -- real, tracked gaps rather than test bugs. Each cites the
// docs/project-status.md ledger item that owns closing it. Delete an entry the same
// change that gives it a real handler, so this list can't quietly go stale.
const KNOWN_GAPS: Record<string, string> = {
  'GET /api/orgs/{org}/ado-credentials/{id}':
    'F4 -- no single-credential read procedure exists yet',
  'UPDATE /api/orgs/{org}/ado-credentials/{id}': 'F4 -- "update a credential" is an open item',
  'GET /api/orgs/{org}/repo-links/{id}': 'F4 -- no single-repo-link read procedure exists yet',
  'UPDATE /api/orgs/{org}/repo-links/{id}': 'F4 -- repo-link update is an open item',
  'POST /api/orgs/{org}/repo-links/{id}/sync':
    'F4 -- "force immediate sync" endpoint is an open item',
};

// Endpoints spec §12 documents that never got a Connect procedure by design --
// they remain plain REST/browser-redirect routes (docs/archive/api-contract-design.md
// "Out of contract, unchanged"). Checked directly against handlers.ts's REST registrations.
const REST_ONLY: Record<string, string> = {};

describe('spec §12 / mock route coverage (spec-drift guard)', () => {
  const specSource = readFileSync(specPath, 'utf-8');
  const handlersSource = readFileSync(handlersPath, 'utf-8');
  const specEndpoints = parseSpecSection12(specSource);
  const registered = registeredRouteKeys(handlersSource);

  it('parses a non-trivial endpoint list from spec.md §12 (parser sanity check)', () => {
    // Guards against the parser silently matching nothing if §12's heading or fence
    // format changes -- an empty list would make the coverage test below vacuously pass.
    expect(specEndpoints.length).toBeGreaterThan(20);
  });

  it('every spec §12 endpoint has a mock handler, or is an explicit tracked gap', () => {
    const missing: string[] = [];

    for (const ep of specEndpoints) {
      const key = `${ep.method} ${ep.path}`;
      if (key in KNOWN_GAPS) continue;

      const restKey = REST_ONLY[key];
      if (restKey) {
        if (!registered.has(restKey)) {
          missing.push(`${key} -- no REST handler registered for "${restKey}" (raw: "${ep.raw}")`);
        }
        continue;
      }

      const procedure = SPEC_TO_PROCEDURE[key];
      if (!procedure) {
        missing.push(
          `${key} -- no SPEC_TO_PROCEDURE mapping in this test; add one, or list it in KNOWN_GAPS ` +
            `with a ledger reference if it's a real, tracked gap (raw: "${ep.raw}")`,
        );
        continue;
      }
      if (!registered.has(`POST ${procedure}`)) {
        missing.push(
          `${key} -- expected handler for Connect procedure ${procedure}, none registered in handlers.ts`,
        );
      }
    }

    expect(missing, `Unmocked spec §12 endpoints:\n${missing.join('\n')}`).toEqual([]);
  });
});
