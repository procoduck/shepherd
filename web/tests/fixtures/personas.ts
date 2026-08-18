// Personas — exact shepherd.mgmt.v1.MeService/GetMe response payloads
// (see web/src/gen/shepherd/mgmt/v1/me_pb.ts: GetMeResponse). Field casing
// matches the wire (Connect JSON is camelCase) since these objects are used
// verbatim both as the mocked GetMe response body (tests/mocks/handlers.ts)
// and as window.__initialMe, which useMe() reads without any conversion.
export interface MeResponse {
  userOid: string;
  email: string;
  displayName: string;
  isAppAdmin: boolean;
  authMethod: string;
  orgs: Array<{ id: string; name: string; displayName: string; role: string }>;
}

export const appAdmin: MeResponse = {
  userOid: 'u-appadmin',
  email: 'appadmin@example.com',
  displayName: 'App Admin',
  isAppAdmin: true,
  authMethod: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', displayName: 'Production Org', role: 'admin' }],
};

export const orgAdmin: MeResponse = {
  userOid: 'u-orgadmin',
  email: 'orgadmin@example.com',
  displayName: 'Org Admin',
  isAppAdmin: false,
  authMethod: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', displayName: 'Production Org', role: 'admin' }],
};

export const reader: MeResponse = {
  userOid: 'u-reader',
  email: 'reader@example.com',
  displayName: 'Reader',
  isAppAdmin: false,
  authMethod: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', displayName: 'Production Org', role: 'reader' }],
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
