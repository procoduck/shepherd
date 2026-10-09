import { describe, expect, it } from 'vitest';
import { validateTenant } from './DestinationFormDialog';

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
