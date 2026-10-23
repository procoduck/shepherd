import { describe, expect, it } from 'vitest';
import { OrgTenantKind } from '@/gen/shepherd/mgmt/v1/common_pb';
import { validateStrictTenant, validateTenant } from './DestinationFormDialog';

// Mirrors internal/gateway.ValidateTenantID (Grafana Mimir's documented
// tenant rule), which the server applies to a destination's tenant_id (#261).
describe('validateTenant', () => {
  it.each(['', 'acme', 'team-a_prod.1', "a!b*c'(d)"])('accepts %j', (t) => {
    expect(validateTenant(t)).toBe('');
  });

  it.each([
    ['acme/prod', 'slash'],
    ['acme prod', 'space'],
    ['.', 'reserved dot'],
    ['..', 'reserved dot-dot'],
    ['__mimir_cluster', 'reserved'],
    ['x'.repeat(151), 'too long'],
  ])('refuses %j (%s)', (t) => {
    expect(validateTenant(t)).not.toBe('');
  });

  it('accepts exactly 150 bytes', () => {
    expect(validateTenant('x'.repeat(150))).toBe('');
  });
});

// Strict tenant mode (internal/tenancy): the tenant must be one the org owns.
describe('validateStrictTenant', () => {
  const owned = [
    { tenant: 'acme', kind: OrgTenantKind.EXACT, isDefault: true },
    { tenant: 'acme-', kind: OrgTenantKind.PREFIX, isDefault: false },
  ];

  it.each(['', 'acme', 'acme-eu'])('accepts %j', (t) => {
    expect(validateStrictTenant(t, owned)).toBe('');
  });

  it('refuses a tenant the org does not own, naming its tenants', () => {
    expect(validateStrictTenant('globex', owned)).toContain('acme, acme-*');
  });

  it('asks for no tenant when the org owns none', () => {
    expect(validateStrictTenant('acme', [])).toContain('leave it empty');
  });

  it('still applies the charset rule', () => {
    expect(validateStrictTenant('acme/x', owned)).toBe(validateTenant('acme/x'));
  });
});
