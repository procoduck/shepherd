import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Pencil, Plus, Save, Tags, Trash2, X } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { AdminConfirmDialog } from '@/components/admin/AdminConfirmDialog';
import { Field, Input } from '@/components/ui/Field';
import type { Collector } from '@/gen/shepherd/mgmt/v1/fleet_pb';

export function CollectorLabelsButton({
  canEdit,
  onClick,
}: {
  canEdit: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type='button'
      onClick={onClick}
      className='inline-flex items-center gap-2 rounded-md border border-border-strong px-3 py-2 text-sm hover:bg-card'
    >
      <Tags size={16} />
      {canEdit ? 'Manage labels' : 'View labels'}
    </button>
  );
}

export function CollectorAttributes({
  orgId,
  collector,
  canEdit,
}: {
  orgId: string;
  collector: Collector;
  canEdit: boolean;
}) {
  return (
    <div className='space-y-6'>
      <CollectorLabels
        orgId={orgId}
        collectorId={collector.id}
        labels={collector.labels}
        canEdit={canEdit}
      />
      <section className='space-y-3' aria-label='Alloy attributes'>
        <h2 className='text-sm font-medium'>Alloy attributes</h2>
        {collector.instances.length ? (
          collector.instances.map((instance) => (
            <div key={instance.name} className='space-y-2'>
              <h3 className='break-all text-xs text-muted'>{instance.name}</h3>
              <ReportedAttributes attributes={instance.localAttributes ?? {}} />
            </div>
          ))
        ) : (
          <ReportedAttributes attributes={collector.localAttributes ?? {}} />
        )}
      </section>
    </div>
  );
}

export function CollectorLabels({
  orgId,
  collectorId,
  labels,
  canEdit,
}: {
  orgId: string;
  collectorId: string;
  labels: Record<string, string>;
  canEdit: boolean;
}) {
  const qc = useQueryClient();
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  const [editing, setEditing] = useState(false);
  const [confirmation, setConfirmation] = useState<{ key: string; value?: string } | null>(null);
  const normalizedKey = key.trim().toLowerCase();
  const keyInvalid = !!key && !/^[a-z0-9._/-]{1,128}$/.test(normalizedKey);
  const valueBytes = new TextEncoder().encode(value).length;
  const valueInvalid = valueBytes > 512 || /[\p{Cc}\p{Cf}]/u.test(value);
  const reset = () => {
    setKey('');
    setValue('');
    setEditing(false);
  };
  const change = useMutation({
    mutationFn: (input: { key: string; value?: string }) =>
      input.value === undefined
        ? clients.fleet.deleteCollectorLabel({ orgId, collectorId, key: input.key })
        : clients.fleet.setCollectorLabel({
            orgId,
            collectorId,
            key: input.key,
            value: input.value,
          }),
    onSuccess: async () => {
      setConfirmation(null);
      reset();
      await Promise.all([
        qc.invalidateQueries({ queryKey: ['collector', orgId, collectorId] }),
        qc.invalidateQueries({ queryKey: ['collectors', orgId] }),
      ]);
      toast.success('Labels saved');
    },
    onError: (e) => toast.error(toApiError(e).message),
  });

  return (
    <section className='space-y-3' aria-label='Collector labels'>
      <h2 className='text-sm font-medium'>Labels</h2>
      {Object.keys(labels).length === 0 ? (
        <p className='text-sm text-muted'>No labels</p>
      ) : (
        <dl className='divide-y divide-border'>
          {Object.entries(labels)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([labelKey, labelValue]) => (
              <div key={labelKey} className='flex items-start gap-3 py-2 text-sm'>
                <dt className='w-1/3 min-w-0 break-all font-mono'>{labelKey}</dt>
                <dd className='min-w-0 flex-1 break-all'>{labelValue}</dd>
                {canEdit && (
                  <div className='flex shrink-0 gap-1'>
                    <button
                      type='button'
                      title={`Edit label ${labelKey}`}
                      aria-label={`Edit label ${labelKey}`}
                      disabled={change.isPending}
                      className='p-2 text-muted hover:text-zinc-200 disabled:opacity-50'
                      onClick={() => {
                        setKey(labelKey);
                        setValue(labelValue);
                        setEditing(true);
                      }}
                    >
                      <Pencil size={14} />
                    </button>
                    <button
                      type='button'
                      title={`Delete label ${labelKey}`}
                      aria-label={`Delete label ${labelKey}`}
                      disabled={change.isPending}
                      className='p-2 text-muted hover:text-red-400 disabled:opacity-50'
                      onClick={() => setConfirmation({ key: labelKey })}
                    >
                      <Trash2 size={14} />
                    </button>
                  </div>
                )}
              </div>
            ))}
        </dl>
      )}
      {canEdit && (
        <form
          className='flex flex-wrap items-end gap-2'
          onSubmit={(e) => {
            e.preventDefault();
            if (!normalizedKey || !value || keyInvalid || valueInvalid || change.isPending) return;
            const input = { key: normalizedKey, value };
            if (!editing && Object.hasOwn(labels, normalizedKey)) {
              setConfirmation(input);
            } else {
              change.mutate(input);
            }
          }}
        >
          <Field label='Key' className='min-w-0 flex-1 basis-40'>
            <Input
              value={key}
              required
              aria-invalid={keyInvalid}
              maxLength={128}
              disabled={editing || change.isPending}
              onChange={(e) => setKey(e.target.value)}
            />
          </Field>
          <Field label='Value' className='min-w-0 flex-1 basis-40'>
            <Input
              value={value}
              required
              aria-invalid={valueInvalid}
              maxLength={512}
              disabled={change.isPending}
              onChange={(e) => setValue(e.target.value)}
            />
          </Field>
          <button
            type='submit'
            title={editing ? 'Save label' : 'Add label'}
            aria-label={editing ? 'Save label' : 'Add label'}
            disabled={
              !key.trim() ||
              !value ||
              keyInvalid ||
              valueInvalid ||
              change.isPending ||
              (!editing && Object.keys(labels).length >= 64)
            }
            className='inline-flex items-center gap-2 rounded-md bg-indigo-600 p-2.5 text-sm text-white disabled:opacity-50'
          >
            {editing ? <Save size={16} /> : <Plus size={16} />}
            {editing ? 'Save label' : 'Add label'}
          </button>
          {editing && (
            <button
              type='button'
              title='Cancel edit'
              aria-label='Cancel edit'
              onClick={reset}
              disabled={change.isPending}
              className='p-2.5 text-muted'
            >
              <X size={16} />
            </button>
          )}
        </form>
      )}
      {canEdit && keyInvalid && (
        <p role='alert' className='text-xs text-red-400'>
          Key must contain 1-128 ASCII letters, digits, dots, underscores, slashes or hyphens.
        </p>
      )}
      {canEdit && valueInvalid && (
        <p role='alert' className='text-xs text-red-400'>
          {valueBytes > 512
            ? `Value exceeds 512 UTF-8 bytes (${valueBytes} bytes).`
            : 'Value must not contain control or invisible format characters.'}
        </p>
      )}
      {canEdit && confirmation && (
        <AdminConfirmDialog
          title={
            confirmation.value === undefined ? 'Delete inventory label' : 'Replace inventory label'
          }
          body={
            confirmation.value === undefined
              ? `Delete label "${confirmation.key}"?`
              : `Replace the existing value of label "${confirmation.key}"?`
          }
          confirmLabel={confirmation.value === undefined ? 'Delete label' : 'Replace label'}
          pendingLabel='Saving...'
          pending={change.isPending}
          onConfirm={() => change.mutate(confirmation)}
          onCancel={() => {
            if (!change.isPending) setConfirmation(null);
          }}
        />
      )}
    </section>
  );
}

export function ReportedAttributes({ attributes }: { attributes: Record<string, unknown> }) {
  const entries = Object.entries(attributes).sort(([a], [b]) => a.localeCompare(b));
  if (!entries.length) return <p className='text-sm text-muted'>No reported attributes</p>;
  return (
    <dl className='divide-y divide-border'>
      {entries.map(([key, value]) => (
        <div key={key} className='grid grid-cols-2 gap-4 py-2 text-xs font-mono'>
          <dt className='min-w-0 break-all text-muted'>{key}</dt>
          <dd className='min-w-0 break-all'>
            {typeof value === 'string' ? value : JSON.stringify(value)}
          </dd>
        </div>
      ))}
    </dl>
  );
}
