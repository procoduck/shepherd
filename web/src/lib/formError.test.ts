import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { errorText, formError, formErrors, sentenceCase } from './formError';

describe('sentenceCase', () => {
  it.each([
    ['gateway name is required', 'Gateway name is required'],
    ['  matcher "x" is not valid ', 'Matcher "x" is not valid'],
    ['Already capitalised', 'Already capitalised'],
    ['"quoted" first', '"quoted" first'],
    ['', ''],
  ])('%j -> %j', (input, want) => {
    expect(sentenceCase(input)).toBe(want);
  });
});

describe('errorText / formError', () => {
  it('uses the server message without the Connect code prefix, sentence-cased', () => {
    const e = new ConnectError('name is already in use', Code.AlreadyExists);
    expect(errorText(e, 'Failed')).toBe('Name is already in use');
  });

  it('strips the Go package prefix the transport strips', () => {
    const e = new ConnectError(
      'auth: password must be at least 8 characters',
      Code.InvalidArgument,
    );
    expect(errorText(e, 'Failed')).toBe('Password must be at least 8 characters');
  });

  it('falls back when the refusal carries no text', () => {
    expect(errorText(new ConnectError('', Code.Internal), 'Save failed')).toBe('Save failed');
  });

  it('is null when there is no error, so mutation.error can be passed straight in', () => {
    expect(formError(null, 'Failed')).toBeNull();
    expect(formError(undefined, 'Failed')).toBeNull();
    expect(formError(new Error('render failed'), 'Failed')).toBe('Render failed');
  });
});

describe('formErrors', () => {
  const fields = { name: 'gateway name ', namespace: 'gateway namespace ' };

  it('places an invalid_argument naming a field beside that field, not at the form', () => {
    const e = new ConnectError(
      'gateway namespace "a.b" is not a valid Kubernetes namespace',
      Code.InvalidArgument,
    );
    expect(formErrors(e, 'Failed', fields)).toEqual({
      form: null,
      field: { namespace: 'Gateway namespace "a.b" is not a valid Kubernetes namespace' },
    });
  });

  it('does not mistake a longer field name for a shorter one sharing its prefix', () => {
    const e = new ConnectError('gateway name "Bad Name!" is not valid', Code.InvalidArgument);
    expect(formErrors(e, 'Failed', fields).field).toEqual({
      name: 'Gateway name "Bad Name!" is not valid',
    });
  });

  it('keeps everything else at the form level', () => {
    const conflict = new ConnectError('gateway name already used', Code.AlreadyExists);
    expect(formErrors(conflict, 'Failed', fields)).toEqual({
      form: 'Gateway name already used',
      field: {},
    });
    const other = new ConnectError('org not found', Code.InvalidArgument);
    expect(formErrors(other, 'Failed', fields).form).toBe('Org not found');
    expect(formErrors(null, 'Failed', fields)).toEqual({ form: null, field: {} });
  });
});
