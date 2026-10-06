import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { errorText, formError, sentenceCase } from './formError';

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
