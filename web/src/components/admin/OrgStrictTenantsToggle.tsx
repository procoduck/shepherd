import { useMutation } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import type { Org, TenantViolation } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { errorText } from '@/lib/formError';

/**
 * Admin → Organisations → Edit: the org's strict tenant mode. In strict mode
 * the org's destinations, destination bindings, tenant routes and pipelines
 * may only use tenants it owns. Like the tenants editor, the change is its
 * own app-admin RPC (SetOrgStrictTenants) and takes effect at once. Turning it
 * on is refused while anything of the org breaks the rule; the refusal is
 * followed by TenantPreflight, so the admin sees each object to fix.
 */
export function OrgStrictTenantsToggle({
  org,
  onChange,
}: {
  org: Org;
  onChange: (org: Org) => void;
}) {
  const setMut = useMutation({
    mutationFn: async (on: boolean) => {
      try {
        return {
          org: await clients.admin.setOrgStrictTenants({ orgId: org.id, strictTenants: on }),
        };
      } catch (err) {
        // The refusal's message lists the violations as one line; the
        // preflight returns them as data for the list below.
        const pre = on
          ? await clients.admin.tenantPreflight({ orgId: org.id }).catch(() => undefined)
          : undefined;
        return { error: err, violations: pre?.violations ?? [] };
      }
    },
    onSuccess: (res) => {
      if (res.org) onChange(res.org);
    },
  });
  const result = setMut.data;
  const violations: TenantViolation[] =
    result && 'violations' in result ? (result.violations ?? []) : [];
  const failure = result && 'error' in result ? result.error : setMut.error;
  const serverWide = org.tenantsRequired && !org.strictTenants;

  return (
    <fieldset className='space-y-2' data-testid='org-strict-tenants'>
      <legend className='text-xs font-medium text-muted'>Strict tenant mode</legend>
      <label className='flex items-center gap-2 text-xs text-muted'>
        <input
          type='checkbox'
          checked={org.strictTenants}
          disabled={setMut.isPending}
          onChange={(e) => setMut.mutate(e.target.checked)}
          data-testid='org-strict-tenants-toggle'
        />
        Destinations, bindings, tenant routes and pipelines may only use this organisation&rsquo;s
        own tenants
      </label>
      {serverWide && (
        <p className='text-xs text-warn' data-testid='org-strict-tenants-server-wide'>
          Already on for every organisation: the server sets <code>tenants.require_owned</code>.
        </p>
      )}
      <p className='text-2xs text-muted-3'>
        Pipeline text is checked too: a literal <code>X-Scope-OrgID</code> header or tenant
        attribute naming another tenant is refused; a tenant computed at run time (for example from{' '}
        <code>sys.env</code>) is only warned about, since it cannot be checked.
      </p>
      {failure !== undefined && failure !== null && (
        <div className='space-y-1' role='alert' data-testid='org-strict-tenants-error'>
          {violations.length > 0 ? (
            <>
              <p className='text-xs text-red-400'>
                Strict tenant mode cannot be turned on until these are fixed:
              </p>
              <ul className='list-disc space-y-0.5 pl-5 text-xs text-red-400'>
                {violations.map((v) => (
                  <li key={`${v.kind}:${v.name}`} data-testid='org-strict-tenants-violation'>
                    {v.message}
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <p className='text-xs text-red-400'>
              {errorText(failure, 'Failed to change strict tenant mode')}
            </p>
          )}
        </div>
      )}
    </fieldset>
  );
}
