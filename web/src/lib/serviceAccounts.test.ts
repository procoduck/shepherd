import { describe, expect, it } from 'vitest';
import { Role } from '@/lib/roles';
import { requestCapability, SERVICE_ACCOUNT_TIERS, tierHasCapability } from '@/lib/serviceAccounts';

describe('service-account grants (#362)', () => {
  it('offers every org role as a tier, viewer included', () => {
    expect(SERVICE_ACCOUNT_TIERS).toEqual([Role.ADMIN, Role.EDITOR, Role.VIEWER]);
  });

  it('gives a capability only to a tier that can write', () => {
    expect(tierHasCapability(Role.ADMIN)).toBe(true);
    expect(tierHasCapability(Role.EDITOR)).toBe(true);
    expect(tierHasCapability(Role.VIEWER)).toBe(false);
    expect(tierHasCapability(Role.UNSPECIFIED)).toBe(false);
  });

  it('never sends a capability for a viewer', () => {
    expect(requestCapability(Role.VIEWER, 'apply')).toBe('');
    expect(requestCapability(Role.VIEWER, 'propose')).toBe('');
    expect(requestCapability(Role.EDITOR, 'propose')).toBe('propose');
    expect(requestCapability(Role.ADMIN, 'apply')).toBe('apply');
  });
});
