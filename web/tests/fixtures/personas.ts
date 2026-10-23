// Personas — exact shepherd.mgmt.v1.MeService/GetMe response payloads
// (see web/src/gen/shepherd/mgmt/v1/me_pb.ts: GetMeResponse). Field casing
// matches the wire (Connect JSON is camelCase) since these objects are used
// verbatim both as the mocked GetMe response body (tests/mocks/handlers.ts)
// and as window.__initialMe, which useMe() reads without any conversion.
import type { OrgTenantKind } from '@/gen/shepherd/mgmt/v1/common_pb';
import { Role } from '@/lib/roles';

export interface MeResponse {
  userOid: string;
  email: string;
  displayName: string;
  isAppAdmin: boolean;
  authMethod: string;
  orgs: Array<{
    id: string;
    name: string;
    displayName: string;
    role: Role;
    // #114: mirrors the org setting; absent means false (proto3 default).
    allowExperimentalComponents?: boolean;
    // #261: the org's own tenants; the mock GetMe fills them from the seeded org.
    tenants?: { tenant: string; kind: OrgTenantKind; isDefault?: boolean }[];
  }>;
}

export const appAdmin: MeResponse = {
  userOid: 'u-appadmin',
  email: 'appadmin@example.com',
  displayName: 'App Admin',
  isAppAdmin: true,
  authMethod: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', displayName: 'Production Org', role: Role.ADMIN }],
};

export const orgAdmin: MeResponse = {
  userOid: 'u-orgadmin',
  email: 'orgadmin@example.com',
  displayName: 'Org Admin',
  isAppAdmin: false,
  authMethod: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', displayName: 'Production Org', role: Role.ADMIN }],
};

export const viewer: MeResponse = {
  userOid: 'u-viewer',
  email: 'viewer@example.com',
  displayName: 'Viewer',
  isAppAdmin: false,
  authMethod: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', displayName: 'Production Org', role: Role.VIEWER }],
};

export const nobody: MeResponse = {
  userOid: 'u-nobody',
  email: 'nobody@example.com',
  displayName: 'Nobody',
  isAppAdmin: false,
  authMethod: 'oidc',
  orgs: [],
};

export const localAdmin: MeResponse = {
  userOid: 'local:admin',
  email: '',
  displayName: 'admin',
  isAppAdmin: true,
  orgs: [],
  authMethod: 'local',
};

/** An org editor: can author pipelines, wizards and simulations, but cannot
 *  change what the org IS (destinations, tenant routes, git credentials). The
 *  role that did not exist before local users did. */
export const orgEditor: MeResponse = {
  userOid: 'u-orgeditor',
  email: 'orgeditor@example.com',
  displayName: 'Org Editor',
  isAppAdmin: false,
  authMethod: 'local',
  orgs: [{ id: 'org-0001', name: 'prod-org', displayName: 'Production Org', role: Role.EDITOR }],
};
