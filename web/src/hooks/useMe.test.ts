import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { mayOwePasswordChange } from './useMe';

// #309: every signed-out page load asked GetMe twice — the password-change
// probe re-fetched it after any failure, so a plain 401 logged two console
// errors. Only a refusal the middleware's 403 can explain is worth the probe.
describe('mayOwePasswordChange', () => {
  it('is false for a signed-out 401', () => {
    expect(mayOwePasswordChange(new ConnectError('nope', Code.Unauthenticated))).toBe(false);
  });

  it('is true for a 403 the password-change middleware may have sent', () => {
    expect(mayOwePasswordChange(new ConnectError('', Code.PermissionDenied))).toBe(true);
  });

  it('is false for a transport failure', () => {
    expect(mayOwePasswordChange(new ConnectError('down', Code.Unavailable))).toBe(false);
    expect(mayOwePasswordChange(new TypeError('Failed to fetch'))).toBe(false);
  });
});
