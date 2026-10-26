// The Agent Tokens page's table columns and dialog fields (AdminTokensPage).
// Agent tokens are bound to an organisation with optional cluster/role
// allowlists (migration 0038); these render that binding.
import { Copy } from 'lucide-react';
import type { ReactNode } from 'react';
import { toast } from 'sonner';
import { AllowlistCell } from '@/components/admin/CollectorBindingsSection';
import type { DataTableColumn } from '@/components/ui/DataTable';
import { Field, Input, Select } from '@/components/ui/Field';
import type { AgentToken } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { isUnbound, type OrgOption, orgLabel } from '@/lib/agentTokens';

// Shared by the ID column's copy button and the created-token dialog's ID
// copy button below — neither carries the "just copied" checkmark the
// secret's own copy button does, since a mis-typed ID is easy to notice and
// re-copy (unlike the secret, which is never shown again).
export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    toast.error('Copy failed — select and copy the value manually');
  }
}

function OrgCell({ token }: { token: AgentToken }) {
  if (isUnbound(token)) {
    return (
      <span
        className='rounded bg-amber-500/15 px-1.5 py-0.5 text-xs font-medium text-amber-400'
        data-testid={`token-unbound-${token.id}`}
      >
        Unbound
      </span>
    );
  }
  return <span>{token.orgName || token.orgId}</span>;
}

/**
 * A token's cluster or role allowlist. An empty allowlist means "any" only on
 * a bound token: an unbound one is refused on every collector RPC whatever its
 * allowlists say, so it reads as refused, never "any" (0.22 -> 0.23 upgrade
 * walkthrough).
 */
function TokenAllowlistCell({
  token,
  values,
  kind,
}: {
  token: AgentToken;
  values: string[];
  kind: 'clusters' | 'roles';
}) {
  return (
    <span data-testid={`token-${kind}-${token.id}`}>
      {isUnbound(token) ? (
        <span
          className='text-muted-3'
          title='An unbound token is refused on every collector request until it is bound'
        >
          — (unbound: refused)
        </span>
      ) : (
        <AllowlistCell values={values} />
      )}
    </span>
  );
}

export function tokenColumns(
  isAppAdmin: boolean,
  setRevokeToken: (t: AgentToken) => void,
  setBindToken: (t: AgentToken) => void,
): DataTableColumn<AgentToken>[] {
  return [
    { key: 'name', header: 'Name', render: (t) => t.name },
    {
      key: 'id',
      header: 'ID',
      cellClassName: 'px-4 py-2.5 font-mono text-xs',
      render: (t) => (
        <span className='inline-flex items-center gap-1.5'>
          <span className='select-all'>{t.id}</span>
          <button
            type='button'
            onClick={() => copyText(t.id)}
            aria-label='Copy token ID'
            className='shrink-0 text-muted-3 hover:text-indigo-400'
          >
            <Copy size={12} />
          </button>
        </span>
      ),
    },
    { key: 'org', header: 'Organisation', render: (t) => <OrgCell token={t} /> },
    {
      key: 'clusters',
      header: 'Clusters',
      render: (t) => <TokenAllowlistCell token={t} values={t.clusters} kind='clusters' />,
    },
    {
      key: 'roles',
      header: 'Roles',
      render: (t) => <TokenAllowlistCell token={t} values={t.roles} kind='roles' />,
    },
    {
      key: 'status',
      header: 'Status',
      render: (t) => (
        <span
          className={`text-xs font-medium ${t.status === 'active' ? 'text-emerald-500' : 'text-muted-2'}`}
        >
          {t.status}
        </span>
      ),
    },
    {
      key: 'createdBy',
      header: 'Created by',
      cellClassName: 'px-4 py-2.5 text-muted',
      render: (t) => t.createdBy,
    },
    {
      key: 'actions',
      header: '',
      cellClassName: 'px-4 py-2.5 text-right whitespace-nowrap',
      render: (t) =>
        t.status === 'active' && (
          <span className='inline-flex items-center gap-3'>
            {/* Binding moves a token between organisations: app admin only. */}
            {isAppAdmin && (
              <button
                type='button'
                onClick={() => setBindToken(t)}
                data-testid={`token-bind-${t.id}`}
                className={
                  isUnbound(t)
                    ? 'text-xs font-medium text-indigo-400 hover:text-indigo-300'
                    : 'text-xs text-muted hover:text-indigo-400'
                }
              >
                {isUnbound(t) ? 'Bind' : 'Rebind'}
              </button>
            )}
            <button
              type='button'
              onClick={() => setRevokeToken(t)}
              className='text-xs text-muted hover:text-red-400'
            >
              Revoke
            </button>
          </span>
        ),
    },
  ];
}

// OrgField is the organisation picker of the create and bind dialogs: a select
// for an app admin, the org in view (fixed) for an org admin.
export function OrgField({
  isAppAdmin,
  options,
  value,
  onChange,
  loading,
  error,
}: {
  isAppAdmin: boolean;
  options: OrgOption[];
  value: string;
  onChange: (id: string) => void;
  loading: boolean;
  error?: ReactNode;
}) {
  if (!isAppAdmin) {
    const own = options[0];
    return (
      <Field
        label='Organisation'
        hint='Tokens you create are bound to the organisation in view, and serve only clusters it has already claimed.'
      >
        <Input value={own ? orgLabel(own) : ''} readOnly disabled data-testid='token-org' />
      </Field>
    );
  }
  return (
    <Field
      label='Organisation'
      hint='Collectors using this token are served this organisation’s config. A token an app admin creates or first binds may also claim its unclaimed clusters; rebinding a bound token never changes that.'
      error={error}
    >
      <Select
        value={value}
        required
        onChange={(e) => onChange(e.target.value)}
        data-testid='token-org'
      >
        <option value='' disabled>
          {loading ? 'Loading…' : 'Select an organisation…'}
        </option>
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {orgLabel(o)}
          </option>
        ))}
      </Select>
    </Field>
  );
}

export function AllowlistFields({
  clusters,
  setClusters,
  roles,
  setRoles,
  rolesError,
}: {
  clusters: string;
  setClusters: (v: string) => void;
  roles: string;
  setRoles: (v: string) => void;
  rolesError?: string;
}) {
  return (
    <>
      <Field
        label='Clusters'
        optional
        hint='Comma-separated allowlist; empty means any cluster in the organisation.'
      >
        <Input
          value={clusters}
          onChange={(e) => setClusters(e.target.value)}
          placeholder='prod-eu-1, staging-eu-1'
          data-testid='token-clusters'
        />
      </Field>
      <Field
        label='Roles'
        optional
        hint='Comma-separated allowlist of collector roles: metrics, logs, receiver, singleton. Empty means any role.'
        error={
          rolesError && (
            <span data-testid='token-roles-error' role='alert'>
              {rolesError}
            </span>
          )
        }
      >
        <Input
          value={roles}
          onChange={(e) => setRoles(e.target.value)}
          aria-invalid={!!rolesError}
          data-testid='token-roles'
        />
      </Field>
    </>
  );
}
