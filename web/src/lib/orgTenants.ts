import { OrgTenantKind } from '@/gen/shepherd/mgmt/v1/common_pb';

/**
 * The parts of an OrgTenant these helpers read (Org.tenants,
 * OrgMembership.tenants). An organisation owns several tenants and tenant
 * prefixes; only an application administrator changes them.
 */
export interface OrgTenantLike {
  tenant: string;
  kind: OrgTenantKind;
  isDefault: boolean;
}

const TENANT_CHARSET = /^[0-9a-zA-Z!\-_.*'()]+$/;
const MAX_TENANT_LEN = 150;

/**
 * The exact-tenant rule the server applies (gateway.ValidateTenantID, Grafana
 * Mimir's documented charset), for a non-empty tenant. '' when valid.
 */
export function tenantIdError(tenant: string): string {
  if (tenant === '') return 'Enter a tenant';
  if (new TextEncoder().encode(tenant).length > MAX_TENANT_LEN) return 'At most 150 characters';
  if (tenant === '.' || tenant === '..' || tenant === '__mimir_cluster') {
    return `"${tenant}" is reserved`;
  }
  if (!TENANT_CHARSET.test(tenant)) {
    return "Letters, digits and ! - _ . * ' ( ) only — no slashes or spaces";
  }
  return '';
}

/** The prefix rule (gateway.ValidateTenantPrefix). '' when valid. */
export function tenantPrefixError(prefix: string): string {
  if (prefix === '') return 'Enter a prefix';
  if (new TextEncoder().encode(prefix).length > MAX_TENANT_LEN - 1) return 'At most 149 characters';
  if (!TENANT_CHARSET.test(prefix)) {
    return "Letters, digits and ! - _ . * ' ( ) only — no slashes or spaces";
  }
  return '';
}

/** The org's default tenant, '' when it has none. */
export function defaultTenant(tenants: readonly OrgTenantLike[] | undefined): string {
  return tenants?.find((t) => t.isDefault)?.tenant ?? '';
}

/** The org's exact tenants, default first. */
export function exactTenants(tenants: readonly OrgTenantLike[] | undefined): string[] {
  return (tenants ?? [])
    .filter((t) => t.kind === OrgTenantKind.EXACT)
    .sort((a, b) => Number(b.isDefault) - Number(a.isDefault) || a.tenant.localeCompare(b.tenant))
    .map((t) => t.tenant);
}

/** The org's prefixes. */
export function tenantPrefixes(tenants: readonly OrgTenantLike[] | undefined): string[] {
  return (tenants ?? []).filter((t) => t.kind === OrgTenantKind.PREFIX).map((t) => t.tenant);
}

/**
 * Whether the org owns tenant: one of its exact tenants, or a valid tenant
 * under one of its prefixes — the same rule as gateway.OrgOwnsTenant, which
 * the server applies and which stays authoritative.
 */
export function ownsTenant(tenants: readonly OrgTenantLike[] | undefined, tenant: string): boolean {
  if (tenantIdError(tenant) !== '') return false;
  return (tenants ?? []).some((t) =>
    t.kind === OrgTenantKind.PREFIX ? tenant.startsWith(t.tenant) : tenant === t.tenant,
  );
}

/** How a tenant reads in a list: a prefix gets a trailing `*`. */
export function tenantLabel(t: OrgTenantLike): string {
  return t.kind === OrgTenantKind.PREFIX ? `${t.tenant}*` : t.tenant;
}
