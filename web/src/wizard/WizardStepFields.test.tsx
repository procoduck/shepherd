// @vitest-environment jsdom
import { create } from '@bufbuild/protobuf';
import { ValueSchema } from '@bufbuild/protobuf/wkt';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { StepFieldSchema } from '@/gen/shepherd/mgmt/v1/wizard_pb';
import {
  defaultFieldValue,
  isStepValid,
  type WizardFormState,
  WizardStepFields,
} from './WizardStepFields';

function field(overrides: Partial<Parameters<typeof create<typeof StepFieldSchema>>[1]> = {}) {
  return create(StepFieldSchema, {
    name: 'field1',
    label: 'Field One',
    type: 'text',
    required: false,
    options: [],
    placeholder: '',
    description: '',
    ...overrides,
  });
}

describe('defaultFieldValue', () => {
  it('returns undefined for a non-toggle field with no default', () => {
    expect(defaultFieldValue(field({ type: 'text' }))).toBeUndefined();
  });

  it('returns false for a toggle field with no default', () => {
    expect(defaultFieldValue(field({ type: 'toggle' }))).toBe(false);
  });

  it('unwraps a stringValue default', () => {
    const f = field({
      default: create(ValueSchema, { kind: { case: 'stringValue', value: 'prom-prod' } }),
    });
    expect(defaultFieldValue(f)).toBe('prom-prod');
  });

  it('unwraps a numberValue default', () => {
    const f = field({
      default: create(ValueSchema, { kind: { case: 'numberValue', value: 9090 } }),
    });
    expect(defaultFieldValue(f)).toBe(9090);
  });

  it('unwraps a boolValue default', () => {
    const f = field({ default: create(ValueSchema, { kind: { case: 'boolValue', value: true } }) });
    expect(defaultFieldValue(f)).toBe(true);
  });

  it('returns undefined for an unsupported default kind (e.g. structValue)', () => {
    const f = field({
      default: create(ValueSchema, { kind: { case: 'structValue', value: { fields: {} } } }),
    });
    expect(defaultFieldValue(f)).toBeUndefined();
  });
});

describe('isStepValid', () => {
  it('is valid when there are no required fields', () => {
    expect(isStepValid([field({ required: false })], {})).toBe(true);
  });

  it('is invalid when a required field has no value in state', () => {
    expect(isStepValid([field({ name: 'n', required: true })], {})).toBe(false);
  });

  it('is invalid when a required field is present but empty string', () => {
    const state: WizardFormState = { n: '' };
    expect(isStepValid([field({ name: 'n', required: true })], state)).toBe(false);
  });

  it('is valid when a required field has a non-empty value', () => {
    const state: WizardFormState = { n: 'prom-prod' };
    expect(isStepValid([field({ name: 'n', required: true })], state)).toBe(true);
  });

  it('is valid when a required boolean field is explicitly false (not empty)', () => {
    const state: WizardFormState = { n: false };
    expect(isStepValid([field({ name: 'n', required: true, type: 'toggle' })], state)).toBe(true);
  });

  it('requires every required field, not just one', () => {
    const fields = [field({ name: 'a', required: true }), field({ name: 'b', required: true })];
    expect(isStepValid(fields, { a: 'x' })).toBe(false);
    expect(isStepValid(fields, { a: 'x', b: 'y' })).toBe(true);
  });
});

describe('WizardStepFields', () => {
  it('renders a checkbox for a toggle field and reports the label', () => {
    const onChange = vi.fn();
    render(
      <WizardStepFields
        fields={[field({ name: 'enabled', label: 'Enabled', type: 'toggle' })]}
        state={{}}
        onChange={onChange}
      />,
    );
    const checkbox = screen.getByRole('checkbox') as HTMLInputElement;
    expect(checkbox.checked).toBe(false);
    expect(screen.getByText('Enabled')).toBeTruthy();
    fireEvent.click(checkbox);
    expect(onChange).toHaveBeenCalledWith('enabled', true);
  });

  it('renders a select with a disabled placeholder option plus the schema options', () => {
    const onChange = vi.fn();
    render(
      <WizardStepFields
        fields={[
          field({ name: 'dest', label: 'Destination', type: 'select', options: ['a', 'b'] }),
        ]}
        state={{}}
        onChange={onChange}
      />,
    );
    const select = screen.getByRole('combobox') as HTMLSelectElement;
    const optionValues = Array.from(select.options).map((o) => o.value);
    expect(optionValues).toEqual(['', 'a', 'b']);
    expect(select.options[0].disabled).toBe(true);
    fireEvent.change(select, { target: { value: 'b' } });
    expect(onChange).toHaveBeenCalledWith('dest', 'b');
  });

  it('renders a number input and reports a numeric value on change', () => {
    const onChange = vi.fn();
    render(
      <WizardStepFields
        fields={[field({ name: 'port', label: 'Port', type: 'number', placeholder: '9090' })]}
        state={{}}
        onChange={onChange}
      />,
    );
    const input = screen.getByPlaceholderText('9090') as HTMLInputElement;
    expect(input.type).toBe('number');
    fireEvent.change(input, { target: { value: '9090' } });
    expect(onChange).toHaveBeenCalledWith('port', 9090);
  });

  it('renders a text input for any other field type and reports a string value on change', () => {
    const onChange = vi.fn();
    render(
      <WizardStepFields
        fields={[
          field({ name: 'name', label: 'Name', type: 'text', placeholder: 'pipeline name' }),
        ]}
        state={{}}
        onChange={onChange}
      />,
    );
    const input = screen.getByPlaceholderText('pipeline name') as HTMLInputElement;
    expect(input.type).toBe('text');
    fireEvent.change(input, { target: { value: 'self-monitoring' } });
    expect(onChange).toHaveBeenCalledWith('name', 'self-monitoring');
  });

  it('marks a required field label with a visible "*" and leaves optional fields without one', () => {
    render(
      <WizardStepFields
        fields={[
          field({ name: 'req', label: 'Required Field', type: 'text', required: true }),
          field({ name: 'opt', label: 'Optional Field', type: 'text', required: false }),
        ]}
        state={{}}
        onChange={vi.fn()}
      />,
    );
    const requiredLabel = screen.getByText('Required Field').closest('label');
    expect(requiredLabel?.textContent).toBe('Required Field*');
    const optionalLabel = screen.getByText('Optional Field').closest('label');
    expect(optionalLabel?.textContent).toBe('Optional Field');
  });

  it('renders the field description when present', () => {
    render(
      <WizardStepFields
        fields={[
          field({
            name: 'n',
            label: 'N',
            type: 'text',
            description: 'Used to match collectors.',
          }),
        ]}
        state={{}}
        onChange={vi.fn()}
      />,
    );
    expect(screen.getByText('Used to match collectors.')).toBeTruthy();
  });

  it('renders a *_dest_name text field as a select of the org’s matching-type destinations', () => {
    const onChange = vi.fn();
    render(
      <WizardStepFields
        fields={[
          field({
            name: 'metrics_dest_name',
            label: 'Metrics destination',
            type: 'text',
            required: true,
          }),
        ]}
        state={{}}
        onChange={onChange}
        destinations={[
          { name: 'prom-prod', type: 'prometheus' },
          { name: 'loki-prod', type: 'loki' },
        ]}
      />,
    );
    const select = screen.getByRole('combobox') as HTMLSelectElement;
    const optionValues = Array.from(select.options).map((o) => o.value);
    // metrics_ -> prometheus only: loki-prod must not appear.
    expect(optionValues).toEqual(['', 'prom-prod']);
    fireEvent.change(select, { target: { value: 'prom-prod' } });
    expect(onChange).toHaveBeenCalledWith('metrics_dest_name', 'prom-prod');
  });

  it('falls back to a text input plus a hint when the org has no destination of the matching type', () => {
    render(
      <WizardStepFields
        fields={[field({ name: 'metrics_dest_name', label: 'Metrics destination', type: 'text' })]}
        state={{}}
        onChange={vi.fn()}
        destinations={[{ name: 'loki-prod', type: 'loki' }]}
      />,
    );
    expect(screen.queryByRole('combobox')).toBeNull();
    expect(screen.getByRole('textbox')).toBeTruthy();
    expect(screen.getByRole('link', { name: /destinations/i }).getAttribute('href')).toBe(
      '/destinations',
    );
  });
});
