import { describe, expect, it } from 'vitest';
import { OrgTenantKind } from '@/gen/shepherd/mgmt/v1/common_pb';
import {
  defaultTenant,
  exactTenants,
  ownsTenant,
  tenantIdError,
  tenantLabel,
  tenantPrefixError,
  tenantPrefixes,
} from './orgTenants';

const tenants = [
  { tenant: 'acme-team-', kind: OrgTenantKind.PREFIX, isDefault: false },
  { tenant: 'acme-eu', kind: OrgTenantKind.EXACT, isDefault: false },
  { tenant: 'acme', kind: OrgTenantKind.EXACT, isDefault: true },
];

describe('org tenants', () => {
  it('finds the default, the exact tenants (default first) and the prefixes', () => {
    expect(defaultTenant(tenants)).toBe('acme');
    expect(defaultTenant([])).toBe('');
    expect(defaultTenant(undefined)).toBe('');
    expect(exactTenants(tenants)).toEqual(['acme', 'acme-eu']);
    expect(tenantPrefixes(tenants)).toEqual(['acme-team-']);
  });

  // Mirrors gateway.OrgOwnsTenant (internal/gateway/org_tenants_test.go).
  it.each([
    ['acme', true],
    ['acme-eu', true],
    ['acme-team-payments', true],
    ['acme-team-', true],
    ['acme-tea', false],
    ['globex', false],
    ['', false],
    ['acme-team-a/b', false],
  ])('owns %j: %s', (tenant, want) => {
    expect(ownsTenant(tenants, tenant)).toBe(want);
  });

  it('labels a prefix with a trailing star', () => {
    expect(tenantLabel(tenants[0])).toBe('acme-team-*');
    expect(tenantLabel(tenants[2])).toBe('acme');
  });

  it('validates a tenant and a prefix the way the server does', () => {
    expect(tenantIdError('acme')).toBe('');
    expect(tenantIdError('__mimir_cluster')).not.toBe('');
    expect(tenantIdError('a b')).not.toBe('');
    expect(tenantIdError('x'.repeat(151))).not.toBe('');
    expect(tenantPrefixError('acme-')).toBe('');
    expect(tenantPrefixError('')).not.toBe('');
    expect(tenantPrefixError('x'.repeat(150))).not.toBe('');
    expect(tenantPrefixError('a/')).not.toBe('');
  });
});
