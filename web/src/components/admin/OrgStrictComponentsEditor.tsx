import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Trash2 } from 'lucide-react';
import { type KeyboardEvent, useState } from 'react';
import { clients } from '@/api/transport';
import { Field, Input } from '@/components/ui/Field';
import type { Org, OrgStrictComponent } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { errorText } from '@/lib/formError';
import { strictComponentClassification, strictComponentError } from '@/lib/strictComponents';

/**
 * Admin → Organisations → Edit: the components an app admin allowed for the
 * org although strict tenant mode refuses them (classified refused, or not
 * classified by any shipped schema). Strict tenant mode accepts only
 * classified components; an entry here lifts that refusal only — every other
 * strict check still applies — so each one is marked admin-allowed and
 * unreviewed.
 *
 * Like the import allowlist, each change is its own app-admin RPC
 * (AddOrgStrictComponent, RemoveOrgStrictComponent) and takes effect at once,
 * independent of the dialog's Save. The server refuses a component strict
 * mode already accepts and a removal while an enabled pipeline uses it; its
 * message is shown here.
 */
export function OrgStrictComponentsEditor({ org }: { org: Org }) {
  const qc = useQueryClient();
  const key = ['admin', 'orgStrictComponents', org.id];
  const [component, setComponent] = useState('');
  const [localError, setLocalError] = useState('');

  const listQ = useQuery({
    queryKey: key,
    queryFn: () => clients.admin.listOrgStrictComponents({ orgId: org.id }),
  });

  const refresh = () => qc.invalidateQueries({ queryKey: key });
  const addMut = useMutation({
    mutationFn: () =>
      clients.admin.addOrgStrictComponent({ orgId: org.id, component: component.trim() }),
    onSuccess: () => {
      setComponent('');
      return refresh();
    },
  });
  const removeMut = useMutation({
    mutationFn: (c: OrgStrictComponent) =>
      clients.admin.removeOrgStrictComponent({ orgId: org.id, id: c.id }),
    onSuccess: refresh,
  });
  const resetErrors = () => {
    setLocalError('');
    addMut.reset();
    removeMut.reset();
  };

  const add = () => {
    resetErrors();
    const err = strictComponentError(component.trim());
    if (err) {
      setLocalError(err);
      return;
    }
    addMut.mutate();
  };
  // Inside the org dialog's form: Enter adds instead of submitting it.
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      add();
    }
  };

  const error =
    localError ||
    (addMut.error && errorText(addMut.error, 'Failed to allow the component')) ||
    (removeMut.error && errorText(removeMut.error, 'Failed to remove the component')) ||
    (listQ.error && errorText(listQ.error, 'Failed to load the allowed components')) ||
    '';
  const busy = addMut.isPending || removeMut.isPending;
  const items = listQ.data?.items ?? [];

  return (
    <fieldset className='space-y-2' data-testid='org-strict-components'>
      <legend className='text-xs font-medium text-muted'>Admin-allowed components</legend>
      <p className='text-xs text-muted-2'>
        Strict tenant mode accepts only components with a reviewed classification. A component
        allowed here is accepted for this organisation without one; every other strict tenant check
        still applies to it.
      </p>
      {listQ.isLoading ? (
        <p className='text-xs text-muted-3'>Loading…</p>
      ) : items.length === 0 ? (
        <p className='text-xs italic text-muted-3' data-testid='org-strict-components-empty'>
          None &mdash; the org&rsquo;s pipelines may use only classified components.
        </p>
      ) : (
        <ul className='divide-y divide-border rounded-md border border-border'>
          {items.map((c) => (
            <li
              key={c.id}
              className='flex items-center justify-between gap-2 px-3 py-1.5 text-xs'
              data-testid={`org-strict-component-row-${c.component}`}
            >
              <span className='min-w-0'>
                <span className='block truncate font-mono'>{c.component}</span>
                <span
                  className='block truncate text-amber-400'
                  title={c.reason || undefined}
                  data-testid='org-strict-component-status'
                >
                  admin-allowed, unreviewed ({strictComponentClassification(c)})
                </span>
              </span>
              <span className='flex shrink-0 items-center gap-3'>
                {c.createdBy && <span className='text-muted-3'>by {c.createdBy}</span>}
                <button
                  type='button'
                  disabled={busy}
                  onClick={() => {
                    resetErrors();
                    removeMut.mutate(c);
                  }}
                  className='text-muted-3 hover:text-red-400'
                  aria-label={`Remove ${c.component}`}
                  data-testid={`org-strict-component-remove-${c.component}`}
                >
                  <Trash2 size={13} />
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}
      <div className='flex items-end gap-2'>
        <Field label='Allow component' className='flex-1'>
          <Input
            value={component}
            onChange={(e) => {
              resetErrors();
              setComponent(e.target.value);
            }}
            onKeyDown={onKeyDown}
            mono
            placeholder='otelcol.auth.google'
            aria-invalid={!!error}
            data-testid='org-strict-component-new'
          />
        </Field>
        <button
          type='button'
          onClick={add}
          disabled={busy}
          className='rounded-md border border-border px-3 py-1.5 text-xs hover:border-indigo-500'
          data-testid='org-strict-component-add'
        >
          Allow
        </button>
      </div>
      {error && (
        <p className='text-xs text-red-400' role='alert' data-testid='org-strict-component-error'>
          {error}
        </p>
      )}
    </fieldset>
  );
}
