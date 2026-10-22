// The one org role vocabulary (spec §7.2): viewer < editor < admin, each
// including the one below it — the generated shepherd.mgmt.v1.Role enum, the
// same type GetMe, the user admin and service accounts carry on the wire.
// App admin is not a Role: it is GetMeResponse.isAppAdmin, above every org.
import { Role } from '@/gen/shepherd/mgmt/v1/common_pb';

export { Role };

/** The roles a person can hold, highest first — the order the pickers list
 *  them in. ROLE_UNSPECIFIED is not one: it grants nothing. */
export const ORG_ROLES: readonly Role[] = [Role.ADMIN, Role.EDITOR, Role.VIEWER];

/** Rank for comparing a role against a floor; 0 (UNSPECIFIED) clears nothing. */
export function roleRank(role: Role): number {
  switch (role) {
    case Role.ADMIN:
      return 3;
    case Role.EDITOR:
      return 2;
    case Role.VIEWER:
      return 1;
    case Role.UNSPECIFIED:
      return 0;
    default: {
      const unknown: never = role;
      return unknown;
    }
  }
}

/** Whether `have` clears the floor `need`. UNSPECIFIED on either side never does. */
export function roleAtLeast(have: Role, need: Role): boolean {
  const h = roleRank(have);
  const n = roleRank(need);
  return h > 0 && n > 0 && h >= n;
}

/** The user-facing name of a role. */
export function roleLabel(role: Role): string {
  switch (role) {
    case Role.ADMIN:
      return 'Admin';
    case Role.EDITOR:
      return 'Editor';
    case Role.VIEWER:
      return 'Viewer';
    case Role.UNSPECIFIED:
      return 'No role';
    default: {
      const unknown: never = role;
      return unknown;
    }
  }
}

/** A role as a form value (`<select>` options are strings): its enum name,
 *  e.g. 'EDITOR'. */
export function roleValue(role: Role): string {
  return Role[role];
}

/** The inverse of roleValue; anything that is not a role name reads as
 *  UNSPECIFIED, which the server refuses on write. */
export function roleFromValue(value: string): Role {
  const role = Role[value as keyof typeof Role];
  return typeof role === 'number' ? role : Role.UNSPECIFIED;
}
