import { describe, expect, it } from 'vitest';
import { ORG_ROLES, Role, roleAtLeast, roleFromValue, roleLabel, roleValue } from './roles';

describe('roles', () => {
  it('orders viewer < editor < admin, and UNSPECIFIED clears nothing', () => {
    expect(roleAtLeast(Role.ADMIN, Role.EDITOR)).toBe(true);
    expect(roleAtLeast(Role.EDITOR, Role.EDITOR)).toBe(true);
    expect(roleAtLeast(Role.VIEWER, Role.EDITOR)).toBe(false);
    expect(roleAtLeast(Role.UNSPECIFIED, Role.VIEWER)).toBe(false);
    expect(roleAtLeast(Role.ADMIN, Role.UNSPECIFIED)).toBe(false);
  });

  it('round-trips every assignable role through its form value', () => {
    for (const r of ORG_ROLES) expect(roleFromValue(roleValue(r))).toBe(r);
    expect(roleFromValue('admin')).toBe(Role.UNSPECIFIED);
    expect(roleFromValue('')).toBe(Role.UNSPECIFIED);
  });

  it('labels roles the way the UI always has', () => {
    expect(ORG_ROLES.map(roleLabel)).toEqual(['Admin', 'Editor', 'Viewer']);
  });
});
