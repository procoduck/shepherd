import { Input, Select } from '@/components/ui/Field';
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

// Used standalone (not inside ui/Field), so the mt-1 gap Field normally
// supplies is added here explicitly -- see Field.tsx's note on why
// Input/Select/Textarea leave it out by default.
const inputClassName = 'mt-1 focus:outline-none focus:ring-1 focus:ring-indigo-500';

/** The subset of a Destination this component needs -- kept minimal so
 * callers (and tests) don't have to build a full proto Destination. */
export interface WizardDestination {
  name: string;
  type: string;
}

interface WizardStepFieldsProps {
  fields: StepField[];
  state: WizardFormState;
  onChange: (name: string, value: WizardFieldValue) => void;
  /** The org's destinations, used to turn a `*_dest_name` text field into a
   * picker (F11). Optional/omittable by callers that don't have them yet
   * (e.g. existing unit tests) -- those fields just render as plain text. */
  destinations?: WizardDestination[];
}

/** metrics_-prefixed dest fields want a prometheus destination, logs_-
 * prefixed ones want loki; any other `*_dest_name` field is shown every
 * destination regardless of type. */
function wantedDestinationType(fieldName: string): string | undefined {
  if (fieldName.startsWith('metrics_')) return 'prometheus';
  if (fieldName.startsWith('logs_')) return 'loki';
  return undefined;
}

/**
 * Renders one wizard step's fields purely from the schema — the field list
 * (name/type/required/options/…) is data from GetWizardSchema, never
 * hardcoded per wizard kind. Supports the field types the schema declares:
 * text, select, toggle, number.
 */
export function WizardStepFields({
  fields,
  state,
  onChange,
  destinations = [],
}: WizardStepFieldsProps) {
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

        // A destination-picking text field (metrics_dest_name,
        // logs_dest_name, …): render a <Select> of the org's destinations of
        // the matching type when there are any, keeping the wire value a
        // plain destination name. With none of that type, fall back to the
        // free-text input plus a hint pointing at where to create one --
        // never a select with nothing to pick.
        const isDestField = field.type === 'text' && field.name.endsWith('_dest_name');
        const destMatches = isDestField
          ? destinations.filter((d) => {
              const wanted = wantedDestinationType(field.name);
              return !wanted || d.type === wanted;
            })
          : [];

        return (
          <label key={field.name} className='block text-xs font-medium text-muted'>
            {field.label}
            {field.required && <span className='ml-0.5 text-red-400'>*</span>}
            {field.type === 'select' ? (
              <Select
                value={(value as string) ?? ''}
                onChange={(e) => onChange(field.name, e.target.value)}
                className={inputClassName}
              >
                <option value='' disabled>
                  Select…
                </option>
                {field.options.map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </Select>
            ) : field.type === 'number' ? (
              <Input
                type='number'
                mono
                value={value === undefined ? '' : (value as number)}
                onChange={(e) => onChange(field.name, e.target.valueAsNumber)}
                placeholder={field.placeholder}
                className={inputClassName}
              />
            ) : isDestField && destMatches.length > 0 ? (
              <Select
                value={(value as string) ?? ''}
                onChange={(e) => onChange(field.name, e.target.value)}
                className={inputClassName}
              >
                <option value='' disabled>
                  Select…
                </option>
                {destMatches.map((d) => (
                  <option key={d.name} value={d.name}>
                    {d.name}
                  </option>
                ))}
              </Select>
            ) : (
              <Input
                type='text'
                value={(value as string) ?? ''}
                onChange={(e) => onChange(field.name, e.target.value)}
                placeholder={field.placeholder}
                className={inputClassName}
              />
            )}
            {field.description && (
              <span className='mt-1 block text-xs text-muted-2'>{field.description}</span>
            )}
            {isDestField && destMatches.length === 0 && (
              <span className='mt-1 block text-xs text-muted-2'>
                No matching destination in this org yet. Add one on the{' '}
                <a href='/destinations' className='text-indigo-400 hover:text-indigo-300'>
                  Destinations
                </a>{' '}
                page.
              </span>
            )}
          </label>
        );
      })}
    </div>
  );
}
