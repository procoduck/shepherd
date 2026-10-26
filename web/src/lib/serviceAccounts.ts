// Service-account grant rules the create form and the table share. A service
// account holds any org role as its tier (#362, migration 0042): what it may
// reach is what a person with that role may reach. Capability (propose or
// apply) decides whether it may write once there, so only a tier that can
// write carries one — a viewer never writes and has none.
import { ORG_ROLES, Role } from '@/lib/roles';

/** The tiers a service account can hold, in picker order (highest first). */
export const SERVICE_ACCOUNT_TIERS: readonly Role[] = ORG_ROLES;

/** Whether a service account at this tier carries a capability. */
export function tierHasCapability(role: Role): boolean {
  switch (role) {
    case Role.ADMIN:
    case Role.EDITOR:
      return true;
    case Role.VIEWER:
    case Role.UNSPECIFIED:
      return false;
    default: {
      const unknown: never = role;
      return unknown;
    }
  }
}

/** The capability CreateServiceAccount is sent for a tier: the chosen one for
 *  editor and admin, and none for a viewer (the server refuses a viewer that
 *  names one). */
export function requestCapability(role: Role, capability: string): string {
  return tierHasCapability(role) ? capability : '';
}
