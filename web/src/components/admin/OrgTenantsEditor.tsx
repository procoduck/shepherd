import { useMutation } from '@tanstack/react-query';
import { Star, Trash2 } from 'lucide-react';
import { type KeyboardEvent, useState } from 'react';
import { clients } from '@/api/transport';
import { Field, Input, Select } from '@/components/ui/Field';
import type { Org } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { type OrgTenant, OrgTenantKind } from '@/gen/shepherd/mgmt/v1/common_pb';
import { errorText } from '@/lib/formError';
import { tenantIdError, tenantLabel, tenantPrefixError } from '@/lib/orgTenants';

/**
 * Admin → Organisations → Edit: the tenants and tenant prefixes an org owns.
 *
 * Each change is its own app-admin RPC (AddOrgTenant, RemoveOrgTenant,
 * SetDefaultOrgTenant) and takes effect at once, independent of the dialog's
 * Save — so a refusal (an overlap with another org, or a removal while a
 * route, destination or binding still uses the tenant) is shown right here,
 * with the server's list of what uses it.
 */
export function OrgTenantsEditor({ org, onChange }: { org: Org; onChange: (org: Org) => void }) {
  const [tenant, setTenant] = useState('');
  const [kind, setKind] = useState<OrgTenantKind>(OrgTenantKind.EXACT);
  // The first exact tenant becomes the default unless unticked.
  const hasDefault = org.tenants.some((t) => t.isDefault);
  const [makeDefault, setMakeDefault] = useState(!hasDefault);
  const [localError, setLocalError] = useState('');

  const addMut = useMutation({
    mutationFn: () =>
      clients.admin.addOrgTenant({
        orgId: org.id,
        tenant: tenant.trim(),
        kind,
        makeDefault: kind === OrgTenantKind.EXACT && makeDefault,
      }),
    onSuccess: (saved) => {
      onChange(saved);
      setTenant('');
      setMakeDefault(false);
    },
  });
  const removeMut = useMutation({
    mutationFn: (t: OrgTenant) =>
      clients.admin.removeOrgTenant({ orgId: org.id, tenant: t.tenant, kind: t.kind }),
    onSuccess: onChange,
  });
  const defaultMut = useMutation({
    mutationFn: (t: OrgTenant) =>
      clients.admin.setDefaultOrgTenant({ orgId: org.id, tenant: t.tenant }),
    onSuccess: onChange,
  });
  const resetErrors = () => {
    setLocalError('');
    addMut.reset();
    removeMut.reset();
    defaultMut.reset();
  };

  const add = () => {
    resetErrors();
    const value = tenant.trim();
    const err = kind === OrgTenantKind.PREFIX ? tenantPrefixError(value) : tenantIdError(value);
    if (err) {
      setLocalError(err);
      return;
    }
    addMut.mutate();
  };
  // The editor sits inside the org dialog's form: Enter adds the tenant
  // instead of submitting the whole dialog.
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      add();
    }
  };

  const error =
    localError ||
    (addMut.error && errorText(addMut.error, 'Failed to add the tenant')) ||
    (removeMut.error && errorText(removeMut.error, 'Failed to remove the tenant')) ||
    (defaultMut.error && errorText(defaultMut.error, 'Failed to set the default tenant')) ||
    '';
  const busy = addMut.isPending || removeMut.isPending || defaultMut.isPending;

  return (
    <fieldset className='space-y-2' data-testid='org-tenants'>
      <legend className='text-xs font-medium text-muted'>Tenants</legend>
      <p className='text-xs text-muted-2'>
        The tenants this org&rsquo;s routes may send as X-Scope-OrgID: exact tenants, and prefixes
        under which the org owns every tenant. A tenant belongs to at most one org. The default is
        what a route uses when it names none, and what chart values and new destinations start from.
      </p>
      {org.tenants.length === 0 ? (
        <p className='text-xs italic text-muted-3' data-testid='org-tenants-empty'>
          None yet &mdash; the org cannot have tenant routes until it has a tenant.
        </p>
      ) : (
        <ul className='divide-y divide-border rounded-md border border-border'>
          {org.tenants.map((t) => (
            <li
              key={`${t.kind}:${t.tenant}`}
              className='flex items-center justify-between gap-2 px-3 py-1.5 text-xs'
              data-testid={`org-tenant-row-${t.tenant}`}
            >
              <span className='font-mono'>{tenantLabel(t)}</span>
              <span className='flex items-center gap-3'>
                <span className='text-muted-2'>
                  {t.kind === OrgTenantKind.PREFIX ? 'prefix' : 'exact'}
                </span>
                {t.isDefault ? (
                  <span className='rounded bg-indigo-500/15 px-1.5 py-0.5 text-indigo-400'>
                    default
                  </span>
                ) : (
                  t.kind === OrgTenantKind.EXACT && (
                    <button
                      type='button'
                      disabled={busy}
                      onClick={() => {
                        resetErrors();
                        defaultMut.mutate(t);
                      }}
                      className='text-muted-3 hover:text-indigo-400'
                      aria-label={`Make ${t.tenant} the default`}
                      data-testid={`org-tenant-default-${t.tenant}`}
                    >
                      <Star size={13} />
                    </button>
                  )
                )}
                <button
                  type='button'
                  disabled={busy}
                  onClick={() => {
                    resetErrors();
                    removeMut.mutate(t);
                  }}
                  className='text-muted-3 hover:text-red-400'
                  aria-label={`Remove ${tenantLabel(t)}`}
                  data-testid={`org-tenant-remove-${t.tenant}`}
                >
                  <Trash2 size={13} />
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}
      <div className='flex items-end gap-2'>
        <Field label='Add' className='flex-1'>
          <Input
            value={tenant}
            onChange={(e) => {
              resetErrors();
              setTenant(e.target.value);
            }}
            onKeyDown={onKeyDown}
            mono
            placeholder={kind === OrgTenantKind.PREFIX ? 'acme-team-' : 'acme'}
            aria-invalid={!!error}
            data-testid='org-tenant-new'
          />
        </Field>
        <Select
          value={String(kind)}
          onChange={(e) => {
            resetErrors();
            setKind(Number(e.target.value) as OrgTenantKind);
          }}
          className='w-28'
          aria-label='Tenant kind'
          data-testid='org-tenant-new-kind'
        >
          <option value={String(OrgTenantKind.EXACT)}>Exact</option>
          <option value={String(OrgTenantKind.PREFIX)}>Prefix</option>
        </Select>
        <button
          type='button'
          onClick={add}
          disabled={busy}
          className='rounded-md border border-border px-3 py-1.5 text-xs hover:border-indigo-500'
          data-testid='org-tenant-add'
        >
          Add
        </button>
      </div>
      {kind === OrgTenantKind.EXACT && (
        <label className='flex items-center gap-2 text-xs text-muted'>
          <input
            type='checkbox'
            checked={makeDefault}
            onChange={(e) => setMakeDefault(e.target.checked)}
            data-testid='org-tenant-new-default'
          />
          Make it the default
        </label>
      )}
      {error && (
        <p className='text-xs text-red-400' role='alert' data-testid='org-tenant-error'>
          {error}
        </p>
      )}
    </fieldset>
  );
}
