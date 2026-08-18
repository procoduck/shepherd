// Personas — exact /api/me payloads per spec §7.2
export interface MeResponse {
  user_oid: string;
  email: string;
  display_name: string;
  is_app_admin: boolean;
  auth_method: string;
  orgs: Array<{ id: string; name: string; display_name: string; role: string }>;
}

export const appAdmin: MeResponse = {
  user_oid: 'u-appadmin',
  email: 'appadmin@example.com',
  display_name: 'App Admin',
  is_app_admin: true,
  auth_method: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', display_name: 'Production Org', role: 'admin' }],
};

export const orgAdmin: MeResponse = {
  user_oid: 'u-orgadmin',
  email: 'orgadmin@example.com',
  display_name: 'Org Admin',
  is_app_admin: false,
  auth_method: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', display_name: 'Production Org', role: 'admin' }],
};

export const reader: MeResponse = {
  user_oid: 'u-reader',
  email: 'reader@example.com',
  display_name: 'Reader',
  is_app_admin: false,
  auth_method: 'oidc',
  orgs: [{ id: 'org-0001', name: 'prod-org', display_name: 'Production Org', role: 'reader' }],
};

export const nobody: MeResponse = {
  user_oid: 'u-nobody',
  email: 'nobody@example.com',
  display_name: 'Nobody',
  is_app_admin: false,
  auth_method: 'oidc',
  orgs: [],
};

export const localAdmin: MeResponse = {
  user_oid: 'local:admin',
  email: '',
  display_name: 'admin',
  is_app_admin: true,
  orgs: [],
  auth_method: 'local',
};
