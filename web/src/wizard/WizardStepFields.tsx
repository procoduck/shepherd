import type { StepField } from '@/gen/shepherd/mgmt/v1/wizard_pb';

export type WizardFieldValue = string | number | boolean;
export type WizardFormState = Record<string, WizardFieldValue>;

/** Converts a schema field's google.protobuf.Value default into a plain JS scalar. */
export function defaultFieldValue(field: StepField): WizardFieldValue | undefined {
  const v = field.default;
  if (!v) return field.type === 'toggle' ? false : undefined;
  switch (v.kind.case) {
    case 'stringValue':
    case 'numberValue':
    case 'boolValue':
      return v.kind.value;
    default:
      return undefined;
  }
}

function hasValue(v: WizardFieldValue | undefined): boolean {
  return v !== undefined && v !== null && v !== '';
}

/** A step is valid when every field it marks `required` has a non-empty value. */
export function isStepValid(fields: StepField[], state: WizardFormState): boolean {
  return fields.every((f) => !f.required || hasValue(state[f.name]));
}

const inputClasses =
  'mt-1 w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-indigo-500';

interface WizardStepFieldsProps {
  fields: StepField[];
  state: WizardFormState;
  onChange: (name: string, value: WizardFieldValue) => void;
}

/**
 * Renders one wizard step's fields purely from the schema — the field list
 * (name/type/required/options/…) is data from GetWizardSchema, never
 * hardcoded per wizard kind. Supports the field types the schema declares:
 * text, select, toggle, number.
 */
export function WizardStepFields({ fields, state, onChange }: WizardStepFieldsProps) {
  return (
    <div className='space-y-4'>
      {fields.map((field) => {
        const value = state[field.name];
        if (field.type === 'toggle') {
          return (
            <label key={field.name} className='flex items-center gap-2 text-sm text-zinc-200'>
              <input
                type='checkbox'
                checked={!!value}
                onChange={(e) => onChange(field.name, e.target.checked)}
                className='h-4 w-4 rounded border-border-strong'
              />
              {field.label}
            </label>
          );
        }

        return (
          <label key={field.name} className='block text-xs font-medium text-muted'>
            {field.label}
            {field.required && <span className='ml-0.5 text-red-400'>*</span>}
            {field.type === 'select' ? (
              <select
                value={(value as string) ?? ''}
                onChange={(e) => onChange(field.name, e.target.value)}
                className={inputClasses}
              >
                <option value='' disabled>
                  Select…
                </option>
                {field.options.map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </select>
            ) : field.type === 'number' ? (
              <input
                type='number'
                value={value === undefined ? '' : (value as number)}
                onChange={(e) => onChange(field.name, e.target.valueAsNumber)}
                placeholder={field.placeholder}
                className={inputClasses + ' font-mono'}
              />
            ) : (
              <input
                type='text'
                value={(value as string) ?? ''}
                onChange={(e) => onChange(field.name, e.target.value)}
                placeholder={field.placeholder}
                className={inputClasses}
              />
            )}
            {field.description && (
              <span className='mt-1 block text-xs text-muted-2'>{field.description}</span>
            )}
          </label>
        );
      })}
    </div>
  );
}
