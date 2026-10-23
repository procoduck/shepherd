import { Field, Input, Select } from '@/components/ui/Field';
import {
  defaultTenant,
  exactTenants,
  type OrgTenantLike,
  ownsTenant,
  tenantPrefixes,
} from '@/lib/orgTenants';

// The picker's value for "a tenant under one of the org's prefixes", typed
// into the field beside it.
const UNDER_PREFIX = '__under_prefix__';

/** The tenant-route form's tenant choice. */
export interface RouteTenantPick {
  /** '' = the org's default; an exact tenant; or "a tenant under a prefix". */
  choice: string;
  /** The tenant typed under a prefix. */
  underPrefix: string;
}

export const EMPTY_ROUTE_TENANT: RouteTenantPick = { choice: '', underPrefix: '' };

/** The CreateTenantRoute tenant to send; '' asks the server for the default. */
export function routeTenantRequest(pick: RouteTenantPick): string {
  return pick.choice === UNDER_PREFIX ? pick.underPrefix.trim() : pick.choice;
}

/**
 * Why the pick cannot be sent, '' when it can: a typed tenant must sit under
 * one of the org's prefixes (gateway.OrgOwnsTenant — the server checks again).
 */
export function routeTenantError(
  tenants: readonly OrgTenantLike[] | undefined,
  pick: RouteTenantPick,
): string {
  if (pick.choice !== UNDER_PREFIX || ownsTenant(tenants, routeTenantRequest(pick))) return '';
  const prefixes = tenantPrefixes(tenants).map((p) => `${p}*`);
  return `Enter a tenant under ${prefixes.join(', ')}, using letters, digits and ! - _ . * ' ( ).`;
}

/**
 * The tenant a new route sends as X-Scope-OrgID: one the organisation owns —
 * the default (preselected), another exact tenant, or a tenant under one of
 * its prefixes. Only an application administrator changes which those are.
 */
export function RouteTenantField({
  tenants,
  pick,
  error,
  onChange,
}: {
  tenants: readonly OrgTenantLike[] | undefined;
  pick: RouteTenantPick;
  error: string;
  onChange: (next: RouteTenantPick) => void;
}) {
  const orgDefault = defaultTenant(tenants);
  const otherExact = exactTenants(tenants).filter((t) => t !== orgDefault);
  const prefixes = tenantPrefixes(tenants);
  return (
    <Field
      label='Tenant'
      hint='The tenant this route sends as X-Scope-OrgID: one this organisation owns.'
      error={
        error && (
          <span data-testid='route-tenant-error' role='alert'>
            {error}
          </span>
        )
      }
    >
      <Select
        value={pick.choice}
        onChange={(e) => onChange({ ...pick, choice: e.target.value })}
        data-testid='route-tenant'
      >
        <option value=''>
          {orgDefault ? `${orgDefault} (default)` : 'The default tenant (none set)'}
        </option>
        {otherExact.map((t) => (
          <option key={t} value={t}>
            {t}
          </option>
        ))}
        {prefixes.length > 0 && (
          <option value={UNDER_PREFIX}>
            A tenant under {prefixes.map((p) => `${p}*`).join(', ')}…
          </option>
        )}
      </Select>
      {pick.choice === UNDER_PREFIX && (
        <Input
          value={pick.underPrefix}
          onChange={(e) => onChange({ ...pick, underPrefix: e.target.value })}
          mono
          className='mt-2'
          aria-label='Tenant under a prefix'
          aria-invalid={!!error}
          placeholder={`${prefixes[0] ?? ''}team`}
          data-testid='route-tenant-under-prefix'
        />
      )}
    </Field>
  );
}
