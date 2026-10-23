import { describe, expect, it } from 'vitest';
import { OrgTenantKind } from '@/gen/shepherd/mgmt/v1/common_pb';
import { routeTenantError, routeTenantRequest } from './RouteTenantField';

const tenants = [
  { tenant: 'acme', kind: OrgTenantKind.EXACT, isDefault: true },
  { tenant: 'acme-team-', kind: OrgTenantKind.PREFIX, isDefault: false },
];
const under = '__under_prefix__';

describe('the route tenant pick', () => {
  it('sends empty for the default, the exact tenant, or the typed one under a prefix', () => {
    expect(routeTenantRequest({ choice: '', underPrefix: 'ignored' })).toBe('');
    expect(routeTenantRequest({ choice: 'acme', underPrefix: '' })).toBe('acme');
    expect(routeTenantRequest({ choice: under, underPrefix: ' acme-team-a ' })).toBe('acme-team-a');
  });

  it('refuses a typed tenant outside every prefix, and nothing else', () => {
    expect(routeTenantError(tenants, { choice: '', underPrefix: '' })).toBe('');
    expect(routeTenantError(tenants, { choice: under, underPrefix: 'acme-team-a' })).toBe('');
    expect(routeTenantError(tenants, { choice: under, underPrefix: 'globex' })).toContain(
      'acme-team-*',
    );
    expect(routeTenantError(tenants, { choice: under, underPrefix: 'acme-team-a/b' })).not.toBe('');
  });
});
